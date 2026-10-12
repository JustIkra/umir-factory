package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type feedbackTestGateway struct {
	server      *httptest.Server
	contexts    map[string]string
	counts      map[string]int
	submissions map[string]map[string]any
	next        int
}

func newFeedbackTestGateway(t *testing.T) (*feedbackTestGateway, *suite) {
	t.Helper()
	gateway := &feedbackTestGateway{
		contexts:    make(map[string]string),
		counts:      make(map[string]int),
		submissions: make(map[string]map[string]any),
	}
	gateway.server = httptest.NewServer(http.HandlerFunc(gateway.serveHTTP))
	t.Cleanup(gateway.server.Close)
	s := &suite{
		cfg:    config{gatewayURL: gateway.server.URL, operatorLogin: "operator"},
		client: gateway.server.Client(),
		session: &operatorSession{
			cookie: &http.Cookie{Name: sessionCookie, Value: "session-1"},
			csrf:   "session-token",
			origin: gateway.server.URL,
		},
	}
	return gateway, s
}

func (g *feedbackTestGateway) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Path {
	case "/api/operator/csrf":
		if _, err := r.Cookie(sessionCookie); err == nil {
			writeFeedbackTestResponse(w, http.StatusOK, `{"csrfToken":"session-token"}`)
			return
		}
		g.next++
		token := fmt.Sprintf("anonymous-token-%d", g.next)
		cookie := fmt.Sprintf("anonymous-context-%d", g.next)
		g.contexts[token] = cookie
		http.SetCookie(w, &http.Cookie{Name: preauthCookie, Value: cookie, Path: "/", HttpOnly: true})
		writeFeedbackTestResponse(w, http.StatusOK, `{"csrfToken":"`+token+`"}`)
	case "/api/feedback":
		g.feedback(w, r)
	default:
		writeFeedbackTestResponse(w, http.StatusNotFound, `{"code":"NotFound"}`)
	}
}

func (g *feedbackTestGateway) feedback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeFeedbackProblem(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
		return
	}
	if r.Header.Get("Origin") != g.server.URL || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeFeedbackProblem(w, http.StatusForbidden, "InvalidOrigin")
		return
	}
	token := r.Header.Get(csrfHeader)
	if token == "session-token" {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value != "session-1" {
			writeFeedbackProblem(w, http.StatusForbidden, "InvalidCsrfToken")
			return
		}
	} else {
		cookie, err := r.Cookie(preauthCookie)
		if err != nil || g.contexts[token] != cookie.Value {
			writeFeedbackProblem(w, http.StatusForbidden, "InvalidCsrfToken")
			return
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		writeFeedbackProblem(w, http.StatusRequestEntityTooLarge, "PayloadTooLarge")
		return
	}
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		writeFeedbackProblem(w, http.StatusBadRequest, "InvalidRequest")
		return
	}
	g.counts[token]++
	if g.counts[token] > 5 {
		w.Header().Set("Retry-After", "60")
		writeFeedbackProblem(w, http.StatusTooManyRequests, "FeedbackRateLimited")
		return
	}
	if strings.TrimSpace(fmt.Sprint(request["name"])) == "" || strings.TrimSpace(fmt.Sprint(request["message"])) == "" {
		writeFeedbackTestResponse(w, http.StatusUnprocessableEntity, `{"code":"InvalidFeedback","message":"invalid feedback","fieldErrors":{"name":"Required"},"requestID":"0123456789abcdef0123456789abcdef"}`)
		return
	}
	consent := object(request["consent"])
	if consent["accepted"] != true || text(consent["documentVersion"]) != "v1" {
		writeFeedbackProblem(w, http.StatusUnprocessableEntity, "ConsentRequired")
		return
	}
	submissionID := text(request["submissionID"])
	if text(request["website"]) != "" {
		writeFeedbackReceipt(w, http.StatusCreated, submissionID)
		return
	}
	if previous, exists := g.submissions[submissionID]; exists {
		if !sameFeedbackRequest(previous, request) {
			writeFeedbackProblem(w, http.StatusConflict, "SubmissionIdentityConflict")
			return
		}
		writeFeedbackReceipt(w, http.StatusOK, submissionID)
		return
	}
	g.submissions[submissionID] = request
	writeFeedbackReceipt(w, http.StatusCreated, submissionID)
}

func sameFeedbackRequest(left, right map[string]any) bool {
	return reflect.DeepEqual(left, right)
}

func writeFeedbackTestResponse(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	if _, err := io.WriteString(w, body); err != nil {
		panic(fmt.Errorf("write feedback test response: %w", err))
	}
}

func writeFeedbackProblem(w http.ResponseWriter, status int, code string) {
	writeFeedbackTestResponse(w, status, `{"code":"`+code+`","message":"test problem","requestID":"0123456789abcdef0123456789abcdef"}`)
}

func writeFeedbackReceipt(w http.ResponseWriter, status int, submissionID string) {
	writeFeedbackTestResponse(w, status, `{"submissionID":"`+submissionID+`","receivedAt":"2026-10-12T12:30:00+03:00"}`)
}

func TestFeedbackAcceptanceCoversPublicAndSessionBoundScenarios(t *testing.T) {
	gateway, s := newFeedbackTestGateway(t)
	if err := s.runFeedback(); err != nil {
		t.Fatal(err)
	}
	want := []string{"F01:passed", "F02:passed", "F03:passed", "F04:passed", "F05:passed"}
	if fmt.Sprint(s.result.Outcomes) != fmt.Sprint(want) {
		t.Fatalf("outcomes = %v, want %v", s.result.Outcomes, want)
	}
	if len(gateway.submissions) != 4 {
		t.Fatalf("durable submissions in fake = %d, want 4 ordinary accepted IDs", len(gateway.submissions))
	}
	if gateway.counts["session-token"] != 6 {
		t.Fatalf("session context attempts = %d, want 6 including the limited request", gateway.counts["session-token"])
	}
	if len(gateway.counts) < 3 {
		t.Fatalf("feedback contexts = %d, want isolated anonymous and session contexts", len(gateway.counts))
	}
	for _, record := range s.result.Observations {
		if record.ID == "F05" && record.Observations["retryAfterSeconds"] != strconv.Itoa(60) {
			t.Fatalf("F05 Retry-After observation = %q", record.Observations["retryAfterSeconds"])
		}
	}
}

func TestFeedbackReceiptRequiresMoscowOffset(t *testing.T) {
	if err := validateFeedbackReceipt(response{status: http.StatusCreated, body: map[string]any{"submissionID": "id", "receivedAt": "2026-10-12T12:30:00Z"}, header: http.Header{"Cache-Control": []string{"no-store"}}}, "id", http.StatusCreated); err == nil {
		t.Fatal("UTC receipt accepted; expected explicit Moscow offset")
	}
	if err := validateFeedbackReceipt(response{status: http.StatusCreated, body: map[string]any{"submissionID": "id", "receivedAt": "2026-10-12T15:30:00+03:00"}, header: http.Header{"Cache-Control": []string{"no-store"}}}, "id", http.StatusCreated); err != nil {
		t.Fatalf("valid Moscow receipt rejected: %v", err)
	}
}

func TestFeedbackRawBodyCanFillExactLimit(t *testing.T) {
	payload := map[string]any{"submissionID": "id", "name": "Ada", "email": "ada@example.org", "message": "x", "consent": map[string]any{"accepted": true, "documentVersion": "v1"}}
	body, err := paddedFeedbackJSON(payload, 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 16<<10 {
		t.Fatalf("body size = %d, want %d", len(body), 16<<10)
	}
	if !json.Valid(body) {
		t.Fatal("exact-limit body is not valid JSON")
	}
}

func TestFeedbackSubmissionIDsAreUUIDv4(t *testing.T) {
	first, err := newFeedbackSubmissionID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newFeedbackSubmissionID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 36 || first[14] != '4' || !strings.Contains("89ab", string(first[19])) {
		t.Fatalf("generated IDs are not distinct UUID v4 values: %q and %q", first, second)
	}
}

func TestFeedbackCLIGateRequiresAuth(t *testing.T) {
	factory := t.TempDir()
	for name, content := range map[string]string{"compose.yaml": "services: {}\n", ".env.example": "UMIR_API_PORT=0\n"} {
		if err := os.WriteFile(filepath.Join(factory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--phase", "normal", "--gateway-url", "http://127.0.0.1:1", "--factory-dir", factory, "--compose-project", "umir-http-0123456789abcdef01234567", "--feedback"}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "--feedback requires --auth") {
		t.Fatalf("feedback auth gate error = %q", stderr.String())
	}
}
