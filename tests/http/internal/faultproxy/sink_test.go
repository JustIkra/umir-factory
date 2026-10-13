package faultproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const fixtureProject = "umir-backend-0123456789abcdef01234567"
const fixtureJob = "11111111-1111-4111-8111-111111111111"
const fixtureChallenge = "22222222-2222-4222-8222-222222222222"

func TestSinkExactCorrelationAndUnknownReply(t *testing.T) {
	s := newSinkForTest(t, "smtp")
	c := map[string]any{"kind": "auth-otp", "jobID": fixtureJob, "challengeID": fixtureChallenge}
	arm := map[string]any{"faultID": fixtureChallenge, "selector": c, "mode": "accept-drop-reply", "expireAfterMs": 30000, "count": 1}
	if _, e := s.Operation(context.Background(), "sink.fault.arm", arm); e != nil {
		t.Fatal(e)
	}
	raw := []byte("From: sender@umir.invalid\r\nTo: test@umir.invalid\r\nX-Umir-Fixture-Job-ID: " + fixtureJob + "\r\nX-Umir-Fixture-Challenge-ID: " + fixtureChallenge + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nVerification code:\r\n" + strings.Repeat("7", 6) + "\r\n")
	outcome, e := s.Accept(context.Background(), c, raw, true, true)
	if e != nil {
		t.Fatal(e)
	}
	if outcome != "lost" {
		t.Fatal("fault did not lose acknowledgment")
	}
	outcome, e = s.Accept(context.Background(), c, raw, true, true)
	if e != nil || outcome != "ack" {
		t.Fatal("fault repeated")
	}
	got, e := s.Operation(context.Background(), "sink.capture", map[string]any{"afterSequence": "1", "selector": c, "limit": 64, "waitMs": 0})
	if e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(got)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), strings.Repeat("7", 6)) || strings.Contains(string(b), "test@") {
		t.Fatal("capture leaked private message")
	}
	caps := sinkTestCaptures(t, got)
	if len(caps) != 2 {
		t.Fatal("did not preserve both retries")
	}
	other := map[string]any{"kind": "auth-otp", "jobID": fixtureChallenge, "challengeID": fixtureJob}
	got, e = s.Operation(context.Background(), "sink.capture", map[string]any{"afterSequence": "1", "selector": other, "limit": 64, "waitMs": 0})
	if e != nil || len(sinkTestCaptures(t, got)) != 0 {
		t.Fatal("unrelated capture selected")
	}
}
func TestSinkRequiresAuthoritativeExpiryAndPrivateOTPFile(t *testing.T) {
	s := newSinkForTest(t, "smtp")
	c := map[string]any{"kind": "auth-otp", "jobID": fixtureJob, "challengeID": fixtureChallenge}
	raw := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nVerification code:\r\n" + strings.Repeat("8", 6) + "\r\n")
	if _, e := s.Accept(context.Background(), c, raw, true, true); e != nil {
		t.Fatal(e)
	}
	got, e := s.Operation(context.Background(), "sink.capture", map[string]any{"afterSequence": "1", "selector": c, "limit": 64, "waitMs": 0})
	if e != nil {
		t.Fatal(e)
	}
	captures := sinkTestCaptures(t, got)
	if len(captures) != 1 {
		t.Fatal("missing capture")
	}
	captured, ok := captures[0].(map[string]any)
	if !ok {
		t.Fatal("invalid capture shape")
	}
	capture := captured["captureID"]
	payload := map[string]any{"captureID": capture, "correlation": c, "output": map[string]any{"target": "smtp", "path": "/run/umir-fixtures/private/test-otp"}}
	if _, e := s.Operation(context.Background(), "sink.otp.write", payload); e == nil {
		t.Fatal("OTP invented expiry")
	}
	if _, e := s.Operation(context.Background(), "sink.correlation.bind", map[string]any{"correlation": c, "ownerInstanceID": fixtureJob, "ownerClockGeneration": "1", "authoritativeExpiresAt": "2026-10-13T09:05:00+03:00"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Operation(context.Background(), "sink.otp.write", payload); e != nil {
		t.Fatal(e)
	}
	payload["output"] = map[string]any{"target": "notification", "path": "/run/umir-fixtures/private/test-other"}
	if _, e := s.Operation(context.Background(), "sink.otp.write", payload); e == nil {
		t.Fatal("cross-target file accepted")
	}
}
func TestSinkPrivacyAndCapacityFailClosed(t *testing.T) {
	s := newSinkForTest(t, "smtp")
	c := map[string]any{"kind": "feedback", "submissionID": fixtureJob, "jobID": fixtureChallenge, "channel": "smtp"}
	if _, e := s.Accept(context.Background(), c, []byte("sensitive"), false, true); e == nil {
		t.Fatal("real recipient accepted")
	}
	for i := 0; i < 256; i++ {
		if _, e := s.Accept(context.Background(), c, []byte("private"), true, true); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Accept(context.Background(), c, []byte("private"), true, true); e == nil {
		t.Fatal("silently dropped excess capture")
	}
}
func newSinkForTest(t *testing.T, target string) *FixtureSink {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0o700); e != nil {
		t.Fatal(e)
	}
	s, e := NewFixtureSink(target, fixtureProject, strings.Repeat("a", 64), dir, strings.Repeat("b", 64), strings.Repeat("c", 64))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}

func TestSinkCaptureCannotMutateRetainedCorrelation(t *testing.T) {
	s := newSinkForTest(t, "smtp")
	c := map[string]any{"kind": "feedback", "submissionID": fixtureJob, "jobID": fixtureChallenge, "channel": "smtp"}
	if _, e := s.Accept(context.Background(), c, []byte("private"), true, true); e != nil {
		t.Fatal(e)
	}
	c["jobID"] = fixtureJob
	original := map[string]any{"kind": "feedback", "submissionID": fixtureJob, "jobID": fixtureChallenge, "channel": "smtp"}
	got, e := s.Operation(context.Background(), "sink.capture", map[string]any{"selector": original, "afterSequence": "1", "limit": 1, "waitMs": 0})
	if e != nil {
		t.Fatal(e)
	}
	caps, ok := got["captures"].([]any)
	if !ok || len(caps) != 1 {
		t.Fatal("caller mutated retained correlation")
	}
}
func TestSinkRejectsUnapprovedAndAmbiguousOTP(t *testing.T) {
	for _, body := range []string{strings.Repeat("4", 6), "Verification code:\r\n" + strings.Repeat("4", 6) + "\r\n" + strings.Repeat("5", 6)} {
		if _, e := extractOTP([]byte("Content-Type: text/plain; charset=utf-8\r\n\r\n" + body)); e == nil {
			t.Fatal("unapproved or ambiguous code accepted")
		}
	}
}

func sinkTestCaptures(t *testing.T, data map[string]any) []any {
	t.Helper()
	caps, ok := data["captures"].([]any)
	if !ok {
		t.Fatal("invalid capture shape")
	}
	return caps
}

func TestSinkControlRedactsInvalidOperationAndRetainsReplay(t *testing.T) {
	s := newSinkForTest(t, "smtp")
	handler := s.ControlHandler()
	request := func(id, op string, data map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, e := json.Marshal(map[string]any{"version": 1, "project": fixtureProject, "requestID": id, "operation": op, "data": data})
		if e != nil {
			t.Fatal(e)
		}
		r := httptest.NewRequest(http.MethodPost, "/fixture/v1/control", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request(fixtureJob, "private-input-that-must-not-be-echoed", map[string]any{})
	if strings.Contains(w.Body.String(), "private-input") {
		t.Fatal("invalid operation echoed")
	}
	first := request(fixtureChallenge, "sink.reset", map[string]any{})
	again := request(fixtureChallenge, "sink.reset", map[string]any{})
	if first.Body.String() != again.Body.String() {
		t.Fatal("exact replay changed response")
	}
	conflict := request(fixtureChallenge, "sink.capture", map[string]any{"selector": map[string]any{"kind": "auth-otp", "jobID": fixtureJob, "challengeID": fixtureChallenge}, "afterSequence": "1", "limit": 1, "waitMs": 0})
	if !strings.Contains(conflict.Body.String(), "FaultConflict") {
		t.Fatal("changed replay did not conflict")
	}
}
func TestNotificationDoesNotAdvertiseSMTPOnlyOperation(t *testing.T) {
	s := newSinkForTest(t, "notification")
	got, e := s.Operation(context.Background(), "fixture.ready", map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	caps, ok := got["capabilities"].([]string)
	if !ok {
		t.Fatal("capabilities shape")
	}
	for _, cap := range caps {
		if cap == "sink.otp.write" {
			t.Fatal("unsupported capability advertised")
		}
	}
}
