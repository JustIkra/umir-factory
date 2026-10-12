package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLiveReaderValidatesInitialFramesAndMoscowPayloads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		liveFixtureWrite(t, w, "retry: 3000\n\n")
		for _, event := range []string{
			`event: change\ndata: {"epoch":"epoch-a","seq":"9007199254740993"}\n\n`,
			`event: tick\ndata: {"serverTime":"2026-10-12T17:00:00+03:00"}\n\n`,
			`event: session\ndata: {"idleExpiresAt":"2026-10-12T17:30:00+03:00","absoluteExpiresAt":"2026-10-12T20:00:00+03:00"}\n\n`,
			`event: upstream\ndata: {"available":true}\n\n`,
		} {
			liveFixtureWrite(t, w, strings.ReplaceAll(event, `\n`, "\n"))
			liveFixtureFlush(t, w)
		}
		<-time.After(100 * time.Millisecond)
	}))
	defer server.Close()

	stream, err := openLiveStream(context.Background(), &http.Client{}, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer liveFixtureClose(t, stream)
	initial, err := stream.initialEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(initial) != 4 || initial[0].Name != "change" || initial[1].Name != "tick" || initial[2].Name != "session" || initial[3].Name != "upstream" {
		t.Fatalf("initial events = %#v", initial)
	}
	if initial[0].Change.Seq != "9007199254740993" || initial[2].Session.IdleExpiresAt != "2026-10-12T17:30:00+03:00" {
		t.Fatalf("initial payloads = %#v", initial)
	}
	if initial[3].ReceivedAt.IsZero() {
		t.Fatal("live event has no read-time observation timestamp")
	}
	_, offset := initial[3].ReceivedAt.Zone()
	if offset != 3*60*60 {
		t.Fatalf("live event observation timezone offset = %d, want Moscow UTC+3", offset)
	}
}

func TestLiveReaderRejectsUnknownEventsAndMalformedPayloads(t *testing.T) {
	for name, event := range map[string]string{
		"unknown event":           `event: other\ndata: {}\n\n`,
		"unknown field":           `event: upstream\ndata: {"available":true,"secret":"x"}\n\n`,
		"case-mismatched field":   `event: upstream\ndata: {"Available":true}\n\n`,
		"missing required bool":   `event: upstream\ndata: {}\n\n`,
		"null required bool":      `event: upstream\ndata: {"available":null}\n\n`,
		"duplicate JSON key":      `event: upstream\ndata: {"available":true,"available":false}\n\n`,
		"null object":             `event: upstream\ndata: null\n\n`,
		"missing field":           `event: change\ndata: {"epoch":"e"}\n\n`,
		"invalid decimal":         `event: change\ndata: {"epoch":"e","seq":"1.0"}\n\n`,
		"int64 sequence overflow": `event: change\ndata: {"epoch":"e","seq":"9223372036854775808"}\n\n`,
		"invalid timestamp":       `event: tick\ndata: {"serverTime":"2026-10-12T14:00:00Z"}\n\n`,
		"missing seconds":         `event: tick\ndata: {"serverTime":"2026-10-12T17:00+03:00"}\n\n`,
		"comma fraction":          `event: tick\ndata: {"serverTime":"2026-10-12T17:00:00,1+03:00"}\n\n`,
		"multiple data lines":     `event: upstream\ndata: {"available":true}\ndata: {}\n\n`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("X-Accel-Buffering", "no")
				liveFixtureWrite(t, w, "retry: 3000\n\n", strings.ReplaceAll(event, `\n`, "\n"))
				liveFixtureFlush(t, w)
				<-time.After(100 * time.Millisecond)
			}))
			defer server.Close()
			stream, err := openLiveStream(context.Background(), &http.Client{}, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = stream.initialEvents(context.Background())
			if err == nil {
				liveFixtureClose(t, stream)
				t.Fatal("invalid initial stream was accepted")
			}
			if closeErr := stream.Close(); closeErr != nil && !errors.Is(closeErr, err) {
				t.Errorf("close error %v does not preserve protocol error %v", closeErr, err)
			}
		})
	}
}

func TestLiveChangeAcceptsMaximumNonNegativeInt64Sequence(t *testing.T) {
	event, err := decodeLiveEvent("change", []byte(`{"epoch":"epoch-a","seq":"9223372036854775807"}`))
	if err != nil || event.Change == nil || event.Change.Seq != "9223372036854775807" {
		t.Fatalf("maximum change sequence = %#v, %v", event.Change, err)
	}
}

func TestLiveReaderCancellationClosesAndJoinsReader(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		liveFixtureWrite(t, w, "retry: 3000\n\n")
		liveFixtureFlush(t, w)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	stream, err := openLiveStream(context.Background(), &http.Client{}, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	closed := make(chan struct{})
	go func() {
		liveFixtureClose(t, stream)
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("closing stream did not join the reader promptly")
	}
}

func TestLiveReaderDrainsFiniteFramesBeforeEOF(t *testing.T) {
	for range 25 {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			frames := []string{
				"retry: 3000\n\n",
				`event: change\ndata: {"epoch":"epoch-a","seq":"1"}\n\n`,
				`event: tick\ndata: {"serverTime":"2026-10-12T17:00:00+03:00"}\n\n`,
				`event: session\ndata: {"idleExpiresAt":"2026-10-12T17:30:00+03:00","absoluteExpiresAt":"2026-10-12T20:00:00+03:00"}\n\n`,
				`event: upstream\ndata: {"available":true}\n\n`,
				`event: session-ended\ndata: {"reason":"signed_out"}\n\n`,
			}
			for _, frame := range frames {
				liveFixtureWrite(t, w, strings.ReplaceAll(frame, `\n`, "\n"))
			}
		}))
		stream, err := openLiveStream(context.Background(), &http.Client{}, server.URL, nil)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		initial, err := stream.initialEvents(context.Background())
		if err != nil || len(initial) != 4 {
			liveFixtureClose(t, stream)
			server.Close()
			t.Fatalf("finite initial events = %d, %v", len(initial), err)
		}
		ended, err := stream.Next(context.Background())
		if err != nil || ended.Event == nil || ended.Event.Name != "session-ended" {
			liveFixtureClose(t, stream)
			server.Close()
			t.Fatalf("session-ended frame = %#v, %v", ended, err)
		}
		if _, err := stream.Next(context.Background()); !errors.Is(err, io.EOF) {
			liveFixtureClose(t, stream)
			server.Close()
			t.Fatalf("finite stream EOF = %v", err)
		}
		if err := stream.Close(); err != nil {
			server.Close()
			t.Fatal(err)
		}
		server.Close()
	}
}

func TestLiveReaderDoesNotReadUnboundedNonStreamErrorBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		liveFixtureFlush(t, w)
		<-r.Context().Done()
	}))
	defer server.Close()
	started := time.Now()
	if _, err := openLiveStream(context.Background(), &http.Client{}, server.URL, nil); err == nil {
		t.Fatal("unauthenticated response opened as a live stream")
	}
	if time.Since(started) > time.Second {
		t.Fatal("non-stream error response body delayed stream rejection")
	}
}

func liveFixtureWrite(t *testing.T, writer io.Writer, values ...string) {
	t.Helper()
	for _, value := range values {
		if _, err := io.WriteString(writer, value); err != nil {
			t.Errorf("write live fixture: %v", err)
			return
		}
	}
}

func liveFixtureFlush(t *testing.T, writer http.ResponseWriter) {
	t.Helper()
	flusher, ok := writer.(http.Flusher)
	if !ok {
		t.Error("live fixture response does not support flushing")
		return
	}
	flusher.Flush()
}

func liveFixtureClose(t *testing.T, stream *liveStream) {
	t.Helper()
	if err := stream.Close(); err != nil {
		t.Errorf("close live fixture stream: %v", err)
	}
}
