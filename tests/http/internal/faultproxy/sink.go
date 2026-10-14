package faultproxy

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const FixtureProtocol = "umir-operational-fixtures/1.1.0"
const FixtureBindings = "af215b378140b8e690523fc537007679b8c2599ed3039f7f2a0c651a64c59410"

type FixtureError struct {
	Code      string
	Retryable bool
	Effect    string
}

func (e *FixtureError) Error() string { return e.Code }
func fixtureError(code string) error  { return &FixtureError{Code: code, Effect: "none"} }

type sinkCapture struct {
	meta map[string]any
	raw  []byte
	code string
}
type sinkBinding struct {
	correlation          map[string]any
	expiry               any
	instance, generation string
}
type sinkFault struct {
	id, mode, state string
	selector        map[string]any
	deadline        time.Time
	hits            int
	clear           chan struct{}
}
type sinkReplay struct {
	digest string
	body   []byte
	done   chan struct{}
}
type sinkOutput struct {
	path    string
	expires time.Time
}
type FixtureSink struct {
	mu                                                        sync.Mutex
	target, project, token, instance, private, binary, config string
	captures                                                  []sinkCapture
	bindings                                                  map[string]sinkBinding
	faults                                                    map[string]*sinkFault
	replay                                                    map[string]*sinkReplay
	outputs                                                   []sinkOutput
	sequence                                                  uint64
	total, dropped                                            int
	changed                                                   chan struct{}
	slots                                                     chan struct{}
	cancel                                                    context.CancelFunc
	done                                                      chan struct{}
}

func NewFixtureSink(target, project, token, private, binary, config string) (*FixtureSink, error) {
	if (target != "smtp" && target != "notification") || !regexp.MustCompile(`^umir-backend-[0-9a-f]{24}$`).MatchString(project) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(token) || !filepath.IsAbs(private) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(binary) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(config) {
		return nil, ErrProtocol
	}
	info, e := os.Lstat(private)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, ErrProtocol
	}
	instance, e := NewUUID()
	if e != nil {
		return nil, ErrProtocol
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &FixtureSink{target: target, project: project, token: token, instance: instance, private: private, binary: binary, config: config, bindings: map[string]sinkBinding{}, faults: map[string]*sinkFault{}, replay: map[string]*sinkReplay{}, sequence: 1, changed: make(chan struct{}), slots: make(chan struct{}, 8), cancel: cancel, done: make(chan struct{})}
	go s.cleanup(ctx)
	return s, nil
}
func (s *FixtureSink) cleanup(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			for _, o := range s.outputs {
				if e := os.Remove(o.path); e != nil && !errors.Is(e, os.ErrNotExist) {
					s.dropped++
				}
			}
			s.outputs = nil
			s.mu.Unlock()
			return
		case now := <-ticker.C:
			s.mu.Lock()
			kept := s.outputs[:0]
			for _, o := range s.outputs {
				if now.Before(o.expires) {
					kept = append(kept, o)
				} else if e := os.Remove(o.path); e != nil && !errors.Is(e, os.ErrNotExist) {
					s.dropped++
				}
			}
			s.outputs = kept
			s.mu.Unlock()
		}
	}
}
func (s *FixtureSink) Close()  { s.cancel(); <-s.done }
func (s *FixtureSink) notify() { close(s.changed); s.changed = make(chan struct{}) }
func canonical(data any) []byte {
	b, e := json.Marshal(data)
	if e != nil {
		return nil
	}
	return b
}
func correlationKey(c map[string]any) string { return string(canonical(c)) }

func (s *FixtureSink) Accept(ctx context.Context, c map[string]any, raw []byte, synthetic, safe bool) (string, error) {
	if e := ValidateOperational("#/$defs/Correlation", canonical(c)); e != nil {
		return "", fixtureError("CorrelationConflict")
	}
	c = copyFixtureMap(c)
	if c == nil {
		return "", fixtureError("InvalidRequest")
	}
	if !synthetic || !safe {
		return "", fixtureError("PrivacyViolation")
	}
	if len(raw) > 65536 {
		return "", fixtureError("CapacityExceeded")
	}
	if c["kind"] == "auth-otp" && s.target != "smtp" || c["kind"] == "feedback" && ((s.target == "smtp" && c["channel"] != "smtp") || (s.target == "notification" && c["channel"] != "telegram")) {
		return "", fixtureError("CorrelationConflict")
	}
	code := ""
	if c["kind"] == "auth-otp" {
		var e error
		code, e = extractOTP(raw)
		if e != nil {
			return "", fixtureError("PrivacyViolation")
		}
	}
	s.mu.Lock()
	reply := "ack"
	var hold <-chan struct{}
	var delay time.Duration
	for _, f := range s.faults {
		if f.state == "armed" && !time.Now().Before(f.deadline) {
			f.state = "expired"
		}
		if f.state == "armed" && reflect.DeepEqual(f.selector, c) {
			f.state = "hit"
			f.hits = 1
			switch f.mode {
			case "temporary-reject":
				reply = "temporary"
			case "permanent-reject":
				reply = "permanent"
			case "accept-drop-reply":
				reply = "lost"
			case "hold-before-accept":
				reply = "temporary"
				hold = f.clear
				delay = time.Until(f.deadline)
			}
			break
		}
	}
	if hold != nil {
		s.mu.Unlock()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", fixtureError("OutcomeUnknown")
		case <-hold:
		case <-timer.C:
		}
		return reply, nil
	}
	if len(s.captures) >= 256 || s.total+len(raw) > 16777216 {
		s.dropped++
		s.mu.Unlock()
		return "", fixtureError("CapacityExceeded")
	}
	id, e := NewUUID()
	if e != nil {
		s.mu.Unlock()
		return "", fixtureError("NotReady")
	}
	accepted := reply == "ack" || reply == "lost"
	meta := map[string]any{"captureID": id, "sequence": strconv.FormatUint(s.sequence, 10), "correlation": c, "accepted": accepted, "reply": reply, "bytes": len(raw), "otpPresent": code != "", "recipientSynthetic": synthetic, "notificationContentSafe": safe}
	s.captures = append(s.captures, sinkCapture{meta: meta, raw: bytes.Clone(raw), code: code})
	s.sequence++
	s.total += len(raw)
	s.notify()
	s.mu.Unlock()
	return reply, nil
}

func (s *FixtureSink) Operation(ctx context.Context, op string, data map[string]any) (map[string]any, error) {
	req := map[string]any{"version": 1, "project": s.project, "requestID": "11111111-1111-4111-8111-111111111111", "operation": op, "data": data}
	if e := ValidateOperational("#/$defs/"+strings.ReplaceAll(op, ".", "_")+"_request", canonical(req)); e != nil {
		return nil, fixtureError("InvalidRequest")
	}
	data = copyFixtureMap(data)
	if data == nil {
		return nil, fixtureError("InvalidRequest")
	}
	switch op {
	case "fixture.ready":
		caps := []string{"sink.capture", "sink.correlation.bind", "sink.fault.arm", "sink.fault.read", "sink.fault.clear", "sink.reset"}
		if s.target == "smtp" {
			caps = append(caps, "sink.otp.write")
		}
		return map[string]any{"target": s.target, "instanceID": s.instance, "protocol": FixtureProtocol, "binarySHA256": s.binary, "configSHA256": s.config, "ready": true, "capabilities": caps}, nil
	case "sink.capture":
		return s.capture(ctx, data)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch op {
	case "sink.correlation.bind":
		c, ok := data["correlation"].(map[string]any)
		if !ok {
			return nil, fixtureError("InvalidRequest")
		}
		expiry := data["authoritativeExpiresAt"]
		if c["kind"] == "auth-otp" && expiry == nil || c["kind"] == "feedback" && expiry != nil {
			return nil, fixtureError("CorrelationConflict")
		}
		if c["kind"] == "auth-otp" && s.target != "smtp" || c["kind"] == "feedback" && ((s.target == "smtp" && c["channel"] != "smtp") || (s.target == "notification" && c["channel"] != "telegram")) {
			return nil, fixtureError("CorrelationConflict")
		}
		key := correlationKey(c)
		old, found := s.bindings[key]
		if found && !reflect.DeepEqual(old.expiry, expiry) {
			return nil, fixtureError("CorrelationConflict")
		}
		if !found && len(s.bindings) >= 256 {
			return nil, fixtureError("CapacityExceeded")
		}
		inst, instanceOK := data["ownerInstanceID"].(string)
		if !instanceOK {
			return nil, fixtureError("InvalidRequest")
		}
		gen := fixtureString(data, "ownerClockGeneration")
		s.bindings[key] = sinkBinding{c, expiry, inst, gen}
		return map[string]any{"correlation": c, "ownerInstanceID": inst, "ownerClockGeneration": gen, "authoritativeExpiresAt": expiry, "bound": true}, nil
	case "sink.otp.write":
		if s.target != "smtp" {
			return nil, fixtureError("UnsupportedOperation")
		}
		c := fixtureObject(data, "correlation")
		b, ok := s.bindings[correlationKey(c)]
		if !ok || b.expiry == nil {
			return nil, fixtureError("NotReady")
		}
		output := fixtureObject(data, "output")
		p := fixtureString(output, "path")
		if output["target"] != s.target || !regexp.MustCompile(`^/run/umir-fixtures/private/[A-Za-z0-9._-]+$`).MatchString(p) || filepath.Base(p) == "." || filepath.Base(p) == ".." {
			return nil, fixtureError("Forbidden")
		}
		for _, cap := range s.captures {
			if cap.meta["captureID"] != data["captureID"] {
				continue
			}
			if cap.meta["accepted"] != true || !reflect.DeepEqual(cap.meta["correlation"], c) || cap.code == "" {
				return nil, fixtureError("CorrelationConflict")
			}
			file := filepath.Join(s.private, filepath.Base(p))
			f, e := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if e != nil {
				return nil, fixtureError("FaultConflict")
			}
			_, e = f.WriteString(cap.code)
			e = errors.Join(e, f.Close())
			if e != nil {
				if removeErr := os.Remove(file); removeErr != nil {
					return nil, fixtureError("OutcomeUnknown")
				}
				return nil, fixtureError("StoreUnavailable")
			}
			s.outputs = append(s.outputs, sinkOutput{file, time.Now().Add(30 * time.Second)})
			return map[string]any{"captureID": data["captureID"], "correlation": c, "secretFile": output, "expiresAt": b.expiry}, nil
		}
		return nil, fixtureError("NotFound")
	case "sink.fault.arm":
		id := fixtureString(data, "faultID")
		c := fixtureObject(data, "selector")
		mode := fixtureString(data, "mode")
		duration, e := numberInt(data["expireAfterMs"])
		if e != nil {
			return nil, fixtureError("InvalidRequest")
		}
		if _, ok := s.faults[id]; ok {
			return nil, fixtureError("FaultConflict")
		}
		for _, f := range s.faults {
			if f.state == "armed" && time.Now().Before(f.deadline) && reflect.DeepEqual(f.selector, c) {
				return nil, fixtureError("FaultConflict")
			}
		}
		if len(s.faults) >= 256 {
			return nil, fixtureError("CapacityExceeded")
		}
		s.faults[id] = &sinkFault{id: id, mode: mode, state: "armed", selector: c, deadline: time.Now().Add(time.Duration(duration) * time.Millisecond), clear: make(chan struct{})}
		return map[string]any{"faultID": id, "state": "armed"}, nil
	case "sink.fault.read", "sink.fault.clear":
		id := fixtureString(data, "faultID")
		f, ok := s.faults[id]
		if !ok {
			return nil, fixtureError("NotFound")
		}
		if f.state == "armed" && !time.Now().Before(f.deadline) {
			f.state = "expired"
		}
		if op == "sink.fault.clear" {
			if f.state != "cleared" {
				close(f.clear)
				f.state = "cleared"
			}
			return map[string]any{"faultID": id, "state": "cleared"}, nil
		}
		return map[string]any{"faultID": id, "state": f.state, "hits": f.hits}, nil
	case "sink.reset":
		for _, f := range s.faults {
			if f.state != "cleared" {
				close(f.clear)
			}
		}
		for _, o := range s.outputs {
			if e := os.Remove(o.path); e != nil && !errors.Is(e, os.ErrNotExist) {
				return nil, fixtureError("StoreUnavailable")
			}
		}
		s.outputs = nil
		s.captures = nil
		s.total = 0
		s.dropped = 0
		s.bindings = map[string]sinkBinding{}
		s.faults = map[string]*sinkFault{}
		s.notify()
		return map[string]any{"instanceID": s.instance, "captures": 0, "faults": 0}, nil
	}
	return nil, fixtureError("UnsupportedOperation")
}
func numberInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case json.Number:
		x, e := strconv.Atoi(string(n))
		return x, e
	default:
		return 0, ErrProtocol
	}
}
func (s *FixtureSink) capture(ctx context.Context, data map[string]any) (map[string]any, error) {
	after := fixtureString(data, "afterSequence")
	seq, e := Decimal(after)
	if e != nil {
		return nil, fixtureError("InvalidRequest")
	}
	limit, e := numberInt(data["limit"])
	if e != nil {
		return nil, fixtureError("InvalidRequest")
	}
	wait, e := numberInt(data["waitMs"])
	if e != nil {
		return nil, fixtureError("InvalidRequest")
	}
	c := fixtureObject(data, "selector")
	timer := time.NewTimer(time.Duration(wait) * time.Millisecond)
	defer timer.Stop()
	for {
		s.mu.Lock()
		caps := []any{}
		next := s.sequence
		complete := true
		for _, cap := range s.captures {
			n, sequenceErr := Decimal(fixtureString(cap.meta, "sequence"))
			if sequenceErr != nil {
				s.mu.Unlock()
				return nil, fixtureError("OutcomeUnknown")
			}
			if n < seq || !reflect.DeepEqual(cap.meta["correlation"], c) {
				continue
			}
			if len(caps) == limit {
				complete = false
				next = n
				break
			}
			caps = append(caps, copyFixtureMap(cap.meta))
		}
		result := map[string]any{"instanceID": s.instance, "captures": caps, "nextSequence": strconv.FormatUint(next, 10), "complete": complete, "dropped": s.dropped}
		changed := s.changed
		s.mu.Unlock()
		if len(caps) > 0 || wait == 0 {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return nil, fixtureError("WaitExpired")
		case <-timer.C:
			return result, nil
		case <-changed:
		}
	}
}
func (s *FixtureSink) ControlHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/fixture/v1/control" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 16385))
		if e != nil || len(raw) > 16384 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		value, e := StrictJSON(raw, 16)
		req, ok := value.(map[string]any)
		id := fixtureString(req, "requestID")
		op := fixtureString(req, "operation")
		if e != nil || !ok || !UUID(id) || !OperationalOperationKnown(op) {
			id, e = NewUUID()
			if e != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			s.writeControl(w, id, "invalid", nil, fixtureError("InvalidRequest"))
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) != 1 {
			s.writeControl(w, id, op, nil, fixtureError("Unauthenticated"))
			return
		}
		if req["project"] != s.project {
			s.writeControl(w, id, op, nil, fixtureError("WrongProject"))
			return
		}
		digest := SHA256(canonical(req))
		s.mu.Lock()
		entry, exists := s.replay[id]
		if exists {
			s.mu.Unlock()
			if entry.digest != digest {
				s.writeControl(w, id, op, nil, fixtureError("FaultConflict"))
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-entry.done:
			}
			writeControlBytes(w, entry.body)
			return
		}
		if len(s.replay) >= 256 {
			s.mu.Unlock()
			s.writeControl(w, id, op, nil, fixtureError("CapacityExceeded"))
			return
		}
		entry = &sinkReplay{digest: digest, done: make(chan struct{})}
		s.replay[id] = entry
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		data := fixtureObject(req, "data")
		var result map[string]any
		var callErr error
		if ValidateOperational("#/$defs/"+strings.ReplaceAll(op, ".", "_")+"_request", raw) != nil {
			callErr = fixtureError("InvalidRequest")
		} else {
			result, callErr = s.Operation(ctx, op, data)
		}
		body := s.controlBytes(id, op, result, callErr)
		s.mu.Lock()
		entry.body = body
		close(entry.done)
		s.mu.Unlock()
		writeControlBytes(w, body)
	})
}
func (s *FixtureSink) controlBytes(id, op string, data map[string]any, e error) []byte {
	response := map[string]any{"version": 1, "project": s.project, "requestID": id, "operation": op}
	if e != nil {
		var f *FixtureError
		if !errors.As(e, &f) {
			f = &FixtureError{Code: "NotReady", Effect: "none"}
		}
		response["error"] = map[string]any{"code": f.Code, "effect": f.Effect, "retryable": f.Retryable}
	} else {
		response["data"] = data
	}
	return canonical(response)
}
func (s *FixtureSink) writeControl(w http.ResponseWriter, id, op string, data map[string]any, e error) {
	writeControlBytes(w, s.controlBytes(id, op, data, e))
}
func writeControlBytes(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	if _, e := w.Write(data); e != nil {
		return
	}
}

func extractOTP(raw []byte) (string, error) {
	msg, e := mail.ReadMessage(bytes.NewReader(raw))
	if e != nil {
		return "", ErrProtocol
	}
	parts := []string{}
	if e := mimeText(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body, &parts, 0); e != nil {
		return "", ErrProtocol
	}
	found := ""
	for _, part := range parts {
		if !strings.Contains(part, "Verification code:") && !strings.Contains(part, "Введите этот код на странице регистрации оператора:") {
			return "", ErrProtocol
		}
		for _, line := range strings.Split(strings.ReplaceAll(part, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if regexp.MustCompile(`^[0-9]{6}$`).MatchString(line) {
				if found != "" {
					return "", ErrProtocol
				}
				found = line
			}
		}
	}
	if found == "" {
		return "", ErrProtocol
	}
	return found, nil
}
func mimeText(contentType, transfer string, r io.Reader, out *[]string, depth int) error {
	if depth > 4 {
		return ErrProtocol
	}
	t, params, e := mime.ParseMediaType(contentType)
	if e != nil {
		return ErrProtocol
	}
	switch strings.ToLower(transfer) {
	case "", "7bit", "8bit":
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	default:
		return ErrProtocol
	}
	if strings.HasPrefix(t, "multipart/") {
		reader := multipart.NewReader(r, params["boundary"])
		for count := 0; count < 8; count++ {
			p, e := reader.NextPart()
			if errors.Is(e, io.EOF) {
				return nil
			}
			if e != nil {
				return ErrProtocol
			}
			e = mimeText(p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), p, out, depth+1)
			e = errors.Join(e, p.Close())
			if e != nil {
				return ErrProtocol
			}
		}
		return ErrProtocol
	}
	if t != "text/plain" {
		if t == "text/html" {
			return nil
		}
		return ErrProtocol
	}
	if cs := strings.ToLower(params["charset"]); cs != "" && cs != "utf-8" && cs != "us-ascii" {
		return ErrProtocol
	}
	b, e := io.ReadAll(io.LimitReader(r, 65537))
	if e != nil || len(b) > 65536 {
		return ErrProtocol
	}
	*out = append(*out, string(b))
	return nil
}

func copyFixtureMap(data map[string]any) map[string]any {
	v, e := StrictJSON(canonical(data), 16)
	if e != nil {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

func fixtureString(m map[string]any, key string) string {
	v, ok := m[key].(string)
	if !ok {
		return ""
	}
	return v
}
func fixtureObject(m map[string]any, key string) map[string]any {
	v, ok := m[key].(map[string]any)
	if !ok {
		return nil
	}
	return v
}
