package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	liveHeaderTimeout = 5 * time.Second
	liveMaxLine       = 64 * 1024
)

type liveChange struct {
	Epoch string `json:"epoch"`
	Seq   string `json:"seq"`
}

type liveTick struct {
	ServerTime string `json:"serverTime"`
}

type liveSession struct {
	IdleExpiresAt     string `json:"idleExpiresAt"`
	AbsoluteExpiresAt string `json:"absoluteExpiresAt"`
}

type liveSessionEnded struct {
	Reason string `json:"reason"`
}

type liveUpstream struct {
	Available bool `json:"available"`
}

type liveEvent struct {
	Name         string
	ReceivedAt   time.Time
	Change       *liveChange
	Tick         *liveTick
	Session      *liveSession
	SessionEnded *liveSessionEnded
	Upstream     *liveUpstream
}

type liveFrame struct {
	Retry     string
	Heartbeat bool
	Event     *liveEvent
}

type liveStream struct {
	cancel    context.CancelFunc
	body      io.ReadCloser
	frames    chan liveFrame
	readDone  chan struct{}
	readerErr error
	closeOnce sync.Once
	closeErr  error
}

func openLiveStream(parent context.Context, client *http.Client, endpoint string, session *operatorSession) (*liveStream, error) {
	if client == nil || client.Timeout != 0 {
		return nil, errors.New("live stream requires a client without a total timeout")
	}
	ctx, cancel := context.WithCancel(parent)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/api/live", nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("build live stream request: %w", err)
	}
	session.apply(request)
	headerTimer := time.AfterFunc(liveHeaderTimeout, cancel)
	response, err := client.Do(request)
	headerTimedOut := !headerTimer.Stop()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open live stream: %w", err)
	}
	if headerTimedOut || ctx.Err() != nil {
		cancel()
		return nil, errors.Join(errors.New("live stream response headers timed out"), response.Body.Close())
	}
	if response.StatusCode != http.StatusOK {
		cancel()
		closeErr := response.Body.Close()
		return nil, errors.Join(fmt.Errorf("live stream returned HTTP %d", response.StatusCode), closeErr)
	}
	mediaType, parameters, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || mediaType != "text/event-stream" || !strings.EqualFold(parameters["charset"], "utf-8") ||
		response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Accel-Buffering") != "no" {
		cancel()
		return nil, errors.Join(errors.New("live stream response headers do not match the documented contract"), response.Body.Close())
	}
	stream := &liveStream{cancel: cancel, body: response.Body, frames: make(chan liveFrame, 16), readDone: make(chan struct{})}
	go stream.read()
	return stream, nil
}

func (s *liveStream) read() {
	defer close(s.readDone)
	defer close(s.frames)
	s.readerErr = readLiveFrames(s.body, s.frames, s.cancel)
}

func (s *liveStream) Next(ctx context.Context) (liveFrame, error) {
	select {
	case frame, ok := <-s.frames:
		if ok {
			return frame, nil
		}
	default:
	}
	select {
	case frame, ok := <-s.frames:
		if ok {
			return frame, nil
		}
		<-s.readDone
		if s.readerErr != nil {
			return liveFrame{}, s.readerErr
		}
		return liveFrame{}, io.EOF
	case <-s.readDone:
		select {
		case frame, ok := <-s.frames:
			if ok {
				return frame, nil
			}
		default:
		}
		if s.readerErr != nil {
			return liveFrame{}, s.readerErr
		}
		return liveFrame{}, io.EOF
	case <-ctx.Done():
		select {
		case frame, ok := <-s.frames:
			if ok {
				return frame, nil
			}
		default:
		}
		return liveFrame{}, ctx.Err()
	}
}

func (s *liveStream) initialEvents(ctx context.Context) ([]liveEvent, error) {
	first, err := s.Next(ctx)
	if err != nil {
		return nil, err
	}
	if first.Retry != "3000" || first.Event != nil || first.Heartbeat {
		return nil, errors.New("live stream did not begin with retry: 3000")
	}
	want := []string{"change", "tick", "session", "upstream"}
	events := make([]liveEvent, 0, len(want))
	for _, name := range want {
		frame, nextErr := s.Next(ctx)
		if nextErr != nil {
			return nil, nextErr
		}
		if frame.Event == nil || frame.Event.Name != name {
			return nil, fmt.Errorf("initial live event = %q, want %q", eventName(frame.Event), name)
		}
		events = append(events, *frame.Event)
	}
	return events, nil
}

func eventName(event *liveEvent) string {
	if event == nil {
		return ""
	}
	return event.Name
}

func (s *liveStream) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.closeErr = s.body.Close()
		<-s.readDone
		if errors.Is(s.readerErr, context.Canceled) || errors.Is(s.readerErr, net.ErrClosed) || errors.Is(s.readerErr, io.ErrClosedPipe) {
			s.readerErr = nil
		}
		s.closeErr = errors.Join(s.closeErr, s.readerErr)
	})
	return s.closeErr
}

func readLiveFrames(body io.Reader, frames chan<- liveFrame, cancel context.CancelFunc) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), liveMaxLine)
	var eventName string
	var data []byte
	var retry string
	hasRetry := false
	heartbeat := false
	fields := 0
	reset := func() {
		eventName, retry = "", ""
		hasRetry = false
		data = nil
		heartbeat = false
		fields = 0
	}
	writeFrame := func() error {
		if fields == 0 {
			return nil
		}
		frame := liveFrame{Retry: retry, Heartbeat: heartbeat}
		if hasRetry {
			if fields != 1 {
				return errors.New("SSE retry frame contains other fields")
			}
			if _, err := strconv.Atoi(retry); err != nil {
				return errors.New("SSE retry value is not an integer")
			}
		} else if eventName != "" || data != nil {
			if eventName == "" || data == nil {
				return errors.New("SSE event requires exactly one event and data field")
			}
			decoded, err := decodeLiveEvent(eventName, data)
			if err != nil {
				return err
			}
			decoded.ReceivedAt = time.Now().In(time.FixedZone("MSK", 3*60*60))
			frame.Event = &decoded
		}
		select {
		case frames <- frame:
			return nil
		default:
			return errors.New("live stream reader was not drained")
		}
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := writeFrame(); err != nil {
				cancel()
				return err
			}
			reset()
			continue
		}
		if strings.HasPrefix(line, ":") {
			heartbeat = true
			fields++
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			field, value = line, ""
		}
		value = strings.TrimPrefix(value, " ")
		fields++
		switch field {
		case "event":
			if eventName != "" {
				return errors.New("SSE frame contains more than one event field")
			}
			eventName = value
		case "data":
			if data != nil {
				return errors.New("SSE frame contains more than one data line")
			}
			data = []byte(value)
		case "retry":
			if hasRetry {
				return errors.New("SSE frame contains more than one retry field")
			}
			hasRetry = true
			retry = value
		default:
			return fmt.Errorf("unknown SSE field %q", field)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if fields != 0 {
		return errors.New("SSE stream ended with an incomplete frame")
	}
	return nil
}

func decodeLiveEvent(name string, data []byte) (liveEvent, error) {
	event := liveEvent{Name: name}
	switch name {
	case "change":
		var payload liveChange
		if err := decodeExactLivePayload(data, []string{"epoch", "seq"}, &payload); err != nil {
			return liveEvent{}, fmt.Errorf("invalid live change payload: %w", err)
		}
		if payload.Epoch == "" || !decimalString(payload.Seq) {
			return liveEvent{}, errors.New("live change payload requires epoch and DecimalString seq")
		}
		if _, err := strconv.ParseInt(payload.Seq, 10, 64); err != nil {
			return liveEvent{}, errors.New("live change seq exceeds NonNegativeInt64")
		}
		event.Change = &payload
	case "tick":
		var payload liveTick
		if err := decodeExactLivePayload(data, []string{"serverTime"}, &payload); err != nil {
			return liveEvent{}, fmt.Errorf("invalid live tick payload: %w", err)
		}
		if err := validateMoscowTimestamp(payload.ServerTime); err != nil {
			return liveEvent{}, fmt.Errorf("invalid live tick timestamp: %w", err)
		}
		event.Tick = &payload
	case "session":
		var payload liveSession
		if err := decodeExactLivePayload(data, []string{"idleExpiresAt", "absoluteExpiresAt"}, &payload); err != nil {
			return liveEvent{}, fmt.Errorf("invalid live session payload: %w", err)
		}
		if err := validateMoscowTimestamp(payload.IdleExpiresAt); err != nil {
			return liveEvent{}, fmt.Errorf("invalid idle expiry: %w", err)
		}
		if err := validateMoscowTimestamp(payload.AbsoluteExpiresAt); err != nil {
			return liveEvent{}, fmt.Errorf("invalid absolute expiry: %w", err)
		}
		event.Session = &payload
	case "session-ended":
		var payload liveSessionEnded
		if err := decodeExactLivePayload(data, []string{"reason"}, &payload); err != nil {
			return liveEvent{}, fmt.Errorf("invalid live session-ended payload: %w", err)
		}
		if payload.Reason != "signed_out" && payload.Reason != "revoked" && payload.Reason != "expired" {
			return liveEvent{}, errors.New("live session-ended payload has an unknown reason")
		}
		event.SessionEnded = &payload
	case "upstream":
		var payload liveUpstream
		if err := decodeExactLivePayload(data, []string{"available"}, &payload); err != nil {
			return liveEvent{}, fmt.Errorf("invalid live upstream payload: %w", err)
		}
		event.Upstream = &payload
	default:
		return liveEvent{}, fmt.Errorf("unknown live event %q", name)
	}
	return event, nil
}

func decodeExactLivePayload(data []byte, want []string, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return errors.New("event data must be one JSON object")
	}
	allowed := make(map[string]bool, len(want))
	for _, key := range want {
		allowed[key] = true
	}
	seen := make(map[string]bool, len(want))
	for decoder.More() {
		token, tokenErr := decoder.Token()
		key, ok := token.(string)
		if tokenErr != nil || !ok {
			return errors.New("event data contains an invalid object key")
		}
		if !allowed[key] {
			return fmt.Errorf("event data has an unknown or case-mismatched field %q", key)
		}
		if seen[key] {
			return fmt.Errorf("event data repeats field %q", key)
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("event data field %q is null", key)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("event data has more than one JSON value")
		}
		return err
	}
	if len(seen) != len(allowed) {
		return errors.New("event data is missing required fields")
	}
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(target); err != nil {
		return err
	}
	return nil
}

func decimalString(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for _, digit := range value[1:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func validateMoscowTimestamp(value string) error {
	if len(value) < len("2006-01-02T15:04:05+03:00") || value[len(value)-6:] != "+03:00" || value[10] != 'T' ||
		value[4] != '-' || value[7] != '-' || value[13] != ':' || value[16] != ':' {
		return errors.New("timestamp must use YYYY-MM-DDTHH:MM:SS and literal +03:00")
	}
	for _, index := range []int{0, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18} {
		if value[index] < '0' || value[index] > '9' {
			return errors.New("timestamp has invalid date or time digits")
		}
	}
	if len(value) > 25 {
		if value[19] != '.' || len(value) == 26 {
			return errors.New("timestamp fraction must use a dot and contain digits")
		}
		for _, digit := range value[20 : len(value)-6] {
			if digit < '0' || digit > '9' {
				return errors.New("timestamp fraction must contain digits")
			}
		}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return err
	}
	_, offset := parsed.Zone()
	if offset != 3*60*60 {
		return errors.New("timestamp does not use UTC+03:00")
	}
	return nil
}
