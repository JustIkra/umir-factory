package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// operatorGateway stands in for the gateway's operator routes and records business requests.
type operatorGateway struct {
	t             *testing.T
	mu            sync.Mutex
	business      []*http.Request
	openBusiness  bool
	sessionCookie *http.Cookie
}

func (g *operatorGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/operator/csrf":
		http.SetCookie(w, &http.Cookie{Name: preauthCookie, Value: "pre-1", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		g.write(w, http.StatusOK, `{"csrfToken":"anonymous-token"}`)
	case "/api/operator/session":
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["password"] != "secret password" ||
			r.Header.Get(csrfHeader) != "anonymous-token" || r.Header.Get("Origin") == "" {
			g.write(w, http.StatusUnauthorized, `{"code":"InvalidCredentials"}`)
			return
		}
		http.SetCookie(w, g.sessionCookie)
		g.write(w, http.StatusOK, `{"operator":{"login":"operator"},"session":{"csrfToken":"session-token","expiresAt":"2026-10-12T12:30:00+03:00","rememberMe":false}}`)
	default:
		g.mu.Lock()
		g.business = append(g.business, r.Clone(r.Context()))
		g.mu.Unlock()
		if _, err := r.Cookie(sessionCookie); err != nil && !g.openBusiness {
			w.Header().Set("WWW-Authenticate", `Session realm="umir-operator"`)
			g.write(w, http.StatusUnauthorized, `{"code":"Unauthenticated"}`)
			return
		}
		g.write(w, http.StatusOK, `{"items":[]}`)
	}
}

func (g *operatorGateway) write(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	if _, err := w.Write([]byte(body)); err != nil {
		g.t.Errorf("write response: %v", err)
	}
}

func (g *operatorGateway) requests() []*http.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*http.Request(nil), g.business...)
}

func newOperatorGateway(t *testing.T) (*operatorGateway, *httptest.Server, *suite) {
	t.Helper()
	gateway := &operatorGateway{t: t, sessionCookie: &http.Cookie{Name: sessionCookie, Value: "session-1", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode}}
	server := httptest.NewServer(gateway)
	t.Cleanup(server.Close)
	return gateway, server, &suite{cfg: config{gatewayURL: server.URL, operatorLogin: "operator"}, client: server.Client()}
}

func TestSignedInRequestsCarryTheFrontendCredentials(t *testing.T) {
	gateway, server, s := newOperatorGateway(t)
	session, _, err := s.signIn("secret password")
	if err != nil {
		t.Fatal(err)
	}
	s.session = session
	if _, err := s.expect(http.MethodGet, "/api/serving/products", nil, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	if _, err := s.expect(http.MethodPost, "/api/serving/products", map[string]string{"productID": "p"}, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	requests := gateway.requests()
	if len(requests) != 2 {
		t.Fatalf("business requests = %d, want 2", len(requests))
	}
	read, write := requests[0], requests[1]
	if cookie, err := read.Cookie(sessionCookie); err != nil || cookie.Value != "session-1" || read.Header.Get(csrfHeader) != "" {
		t.Errorf("GET carried cookie %v, CSRF %q; want the session cookie only", cookie, read.Header.Get(csrfHeader))
	}
	if cookie, err := write.Cookie(sessionCookie); err != nil || cookie.Value != "session-1" ||
		write.Header.Get(csrfHeader) != "session-token" || write.Header.Get("Origin") != server.URL {
		t.Errorf("POST carried cookie %v, CSRF %q, Origin %q", cookie, write.Header.Get(csrfHeader), write.Header.Get("Origin"))
	}
}

func TestUnauthenticatedBusinessAccessFailsTheAuthScenario(t *testing.T) {
	gateway, _, s := newOperatorGateway(t)
	session, _, err := s.signIn("secret password")
	if err != nil {
		t.Fatal(err)
	}
	s.session = session
	if err := s.a01(""); err != nil {
		t.Fatalf("A01 with a guarded gateway = %v", err)
	}
	gateway.openBusiness = true
	if err := s.a01(""); err == nil || !strings.Contains(err.Error(), "succeeded") {
		t.Fatalf("A01 accepted unauthenticated business access: %v", err)
	}
}

func TestRawRequestsCarryTheSession(t *testing.T) {
	var none *operatorSession
	if none.rawHeaders(http.MethodPost) != "" {
		t.Error("no session sent credentials")
	}
	session := &operatorSession{cookie: &http.Cookie{Name: sessionCookie, Value: "s"}, csrf: "c", origin: "http://127.0.0.1:1"}
	if got := session.rawHeaders(http.MethodGet); got != "Cookie: umir_operator_session=s\r\n" {
		t.Errorf("GET headers = %q", got)
	}
	if got := session.rawHeaders(http.MethodPost); got != "Cookie: umir_operator_session=s\r\nOrigin: http://127.0.0.1:1\r\nX-CSRF-Token: c\r\n" {
		t.Errorf("POST headers = %q", got)
	}
}

func TestOperatorPasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("secret password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readPassword(path); err != nil || got != "secret password" {
		t.Errorf("readPassword = %q, %v", got, err)
	}
	if _, err := readPassword("password"); err == nil {
		t.Error("relative password file accepted")
	}
}

func TestAuthCLIRequiresCredentialsTogether(t *testing.T) {
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("secret password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Everything else is valid, so only the auth flags can make the arguments invalid.
	factory := t.TempDir()
	for name, content := range map[string]string{"compose.yaml": "services: {}\n", ".env.example": "UMIR_API_PORT=0\n"} {
		if err := os.WriteFile(filepath.Join(factory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := []string{"--phase", "normal", "--gateway-url", "http://127.0.0.1:1", "--factory-dir", factory, "--compose-project", "umir-http-0123456789abcdef01234567"}
	for name, extra := range map[string][]string{
		"auth without credentials": {"--auth"},
		"auth without password":    {"--auth", "--operator-login", "operator"},
		"realtime without auth":    {"--realtime"},
		"credentials without auth": {"--operator-login", "operator", "--operator-password-file", password},
		"missing password file":    {"--auth", "--operator-login", "operator", "--operator-password-file", password + "-missing"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(append(append([]string(nil), base...), extra...), &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr=%s", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("invalid auth CLI emitted evidence: %s", stdout.String())
			}
		})
	}
}
