package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSmokeRoutes(t *testing.T) {
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path]++
		switch r.URL.Path {
		case "/api/serving/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-serving"}`); err != nil {
				t.Errorf("write serving health response: %v", err)
			}
		case "/api/warehouse/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-warehouse"}`); err != nil {
				t.Errorf("write warehouse health response: %v", err)
			}
		case "/":
			if err := writeTestResponse(w, `<html><div id="root"></div></html>`); err != nil {
				t.Errorf("write frontend response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := server.Client()
	if err := runSmoke(context.Background(), client, smokeTargets{Web: server.URL, API: server.URL}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/api/serving/healthz", "/api/warehouse/healthz", "/"} {
		want := 2
		if route == "/" {
			want = 1
		}
		if seen[route] != want {
			t.Errorf("route %s hit %d times, want %d", route, seen[route], want)
		}
	}
}

func TestSmokeRejectsUnexpectedHealthPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if err := writeTestResponse(w, `{"status":"ok","service":"umir-serving","extra":"unexpected"}`); err != nil {
			t.Errorf("write health response: %v", err)
		}
	}))
	defer server.Close()
	if err := runSmoke(context.Background(), server.Client(), smokeTargets{Web: server.URL, API: server.URL}); err == nil {
		t.Fatal("smoke accepted an extra health response field")
	}
}

func TestVerifyHealthReportsResponseCloseFailure(t *testing.T) {
	closeErr := errors.New("close failed")
	for name, verify := range map[string]func(*http.Client) error{
		"health": func(client *http.Client) error {
			return verifyHealth(context.Background(), client, "http://example.test/healthz", "umir-serving")
		},
		"frontend": func(client *http.Client) error {
			return verifyFrontend(context.Background(), client, "http://example.test/")
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"status":"ok","service":"umir-serving"}`
			if name == "frontend" {
				body = `<html><div id="root"></div></html>`
			}
			client := &http.Client{Transport: responseTransport{body: closeErrorBody{Reader: strings.NewReader(body), err: closeErr}}}
			if err := verify(client); !errors.Is(err, closeErr) {
				t.Fatalf("verify error = %v, want close failure", err)
			}
		})
	}
}

func TestWriteTestResponseReportsShortWrite(t *testing.T) {
	err := writeTestResponse(shortResponseWriter{}, "payload")
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeTestResponse error = %v, want short write", err)
	}
}

type responseTransport struct {
	body io.ReadCloser
}

func (t responseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: t.body, Header: make(http.Header)}, nil
}

type closeErrorBody struct {
	*strings.Reader
	err error
}

func (b closeErrorBody) Close() error { return b.err }

type shortResponseWriter struct{}

func (shortResponseWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return len(data) - 1, nil
}

func writeTestResponse(w io.Writer, value string) error {
	written, err := io.WriteString(w, value)
	if err != nil {
		return err
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	return nil
}

func TestDefaultHTTPTimeoutIsFiveSeconds(t *testing.T) {
	if got := defaultHTTPClient().Timeout; got != 5*time.Second {
		t.Fatalf("HTTP timeout = %s, want 5s", got)
	}
}

func TestSmokeUsesPublishedPortsAndEnvFile(t *testing.T) {
	factory := smokeTestFactory(t)
	server, seen := smokeTestServer(t)
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	executor := &smokeExecutor{ports: map[string][]byte{
		"frontend": []byte("0.0.0.0:" + port + "\n"),
		"gateway":  []byte("127.0.0.1:" + port + "\n"),
	}}
	var stdout, stderr bytes.Buffer
	code := runWithExecutor(context.Background(), []string{"smoke", "--factory-dir", factory, "--env-file", "config/ci.env"}, &stdout, &stderr, executor)
	if code != 0 {
		t.Fatalf("smoke returned %d: %s", code, stderr.String())
	}
	if len(executor.calls) != 2 {
		t.Fatalf("Compose calls = %d, want frontend and gateway port discovery", len(executor.calls))
	}
	resolvedFactory, err := filepath.EvalSymlinks(factory)
	if err != nil {
		t.Fatal(err)
	}
	for i, service := range []string{"frontend", "gateway"} {
		call := executor.calls[i]
		want := []string{"compose", "--env-file", "config/ci.env", "port", service, "8080"}
		if call.dir != resolvedFactory || call.name != "docker" || fmt.Sprint(call.args) != fmt.Sprint(want) {
			t.Errorf("Compose call = %#v, want docker %v from %s", call, want, resolvedFactory)
		}
	}
	for _, route := range []string{"/api/serving/healthz", "/api/warehouse/healthz"} {
		if seen[route] != 2 {
			t.Errorf("route %s hit %d times, want once on web and once on API", route, seen[route])
		}
	}
	if seen["/"] != 1 {
		t.Errorf("frontend root hit %d times, want once", seen["/"])
	}
}

func TestSmokeDefaultDiscoveryOmitsEnvFileAndAllowsMissingDotEnv(t *testing.T) {
	factory := smokeTestFactory(t)
	server, _ := smokeTestServer(t)
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	executor := &smokeExecutor{ports: map[string][]byte{"gateway": []byte("127.0.0.1:" + port + "\n")}}
	args := []string{"smoke", "--factory-dir", factory, "--web-url", server.URL}
	var stderr bytes.Buffer
	if code := runWithExecutor(context.Background(), args, &bytes.Buffer{}, &stderr, executor); code != 0 {
		t.Fatalf("smoke returned %d: %s", code, stderr.String())
	}
	if len(executor.calls) != 1 || fmt.Sprint(executor.calls[0].args) != fmt.Sprint([]string{"compose", "port", "gateway", "8080"}) {
		t.Fatalf("Compose call = %#v, want gateway lookup with Compose's default env behavior", executor.calls)
	}
}

func TestSmokeExplicitURLsSkipTheirComposeCalls(t *testing.T) {
	factory := smokeTestFactory(t)
	server, _ := smokeTestServer(t)
	defer server.Close()
	executor := &smokeExecutor{}
	var stderr bytes.Buffer
	code := runWithExecutor(context.Background(), []string{"smoke", "--factory-dir", factory, "--web-url", server.URL, "--api-url", server.URL}, &bytes.Buffer{}, &stderr, executor)
	if code != 0 {
		t.Fatalf("smoke returned %d: %s", code, stderr.String())
	}
	if len(executor.calls) != 0 {
		t.Fatalf("explicit web and API URLs invoked Docker: %#v", executor.calls)
	}
}

func TestSmokeURLOverrideSkipsOnlyMatchingComposeCall(t *testing.T) {
	for _, test := range []struct {
		name    string
		flag    string
		service string
	}{
		{name: "web", flag: "--web-url", service: "gateway"},
		{name: "api", flag: "--api-url", service: "frontend"},
	} {
		t.Run(test.name, func(t *testing.T) {
			factory := smokeTestFactory(t)
			server, _ := smokeTestServer(t)
			defer server.Close()
			parsed, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, port, err := net.SplitHostPort(parsed.Host)
			if err != nil {
				t.Fatal(err)
			}
			executor := &smokeExecutor{ports: map[string][]byte{test.service: []byte("127.0.0.1:" + port + "\n")}}
			args := []string{"smoke", "--factory-dir", factory, "--env-file", "config/ci.env", test.flag, server.URL}
			var stderr bytes.Buffer
			if code := runWithExecutor(context.Background(), args, &bytes.Buffer{}, &stderr, executor); code != 0 {
				t.Fatalf("smoke returned %d: %s", code, stderr.String())
			}
			if len(executor.calls) != 1 || executor.calls[0].args[4] != test.service {
				t.Fatalf("Compose calls = %#v, want discovery only for %s", executor.calls, test.service)
			}
		})
	}
}

func TestSmokeReportsMissingPublishedPort(t *testing.T) {
	factory := smokeTestFactory(t)
	executor := &smokeExecutor{ports: map[string][]byte{"frontend": []byte("\n")}}
	var stderr bytes.Buffer
	code := runWithExecutor(context.Background(), []string{"smoke", "--factory-dir", factory, "--api-url", "http://127.0.0.1:1"}, &bytes.Buffer{}, &stderr, executor)
	if code == 0 || !strings.Contains(stderr.String(), "no published port for frontend:8080") {
		t.Fatalf("smoke returned %d with diagnostic %q, want explicit missing-port error", code, stderr.String())
	}
}

func TestSmokeUsesFirstPublishedAddressFromMultipleMappings(t *testing.T) {
	factory := smokeTestFactory(t)
	server, seen := smokeTestServer(t)
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	executor := &smokeExecutor{ports: map[string][]byte{
		"frontend": []byte("0.0.0.0:" + port + "\n127.0.0.1:1\n"),
	}}
	var stderr bytes.Buffer
	code := runWithExecutor(context.Background(), []string{"smoke", "--factory-dir", factory, "--env-file", "config/ci.env", "--api-url", server.URL}, &bytes.Buffer{}, &stderr, executor)
	if code != 0 {
		t.Fatalf("smoke returned %d: %s", code, stderr.String())
	}
	if seen["/"] != 1 {
		t.Fatalf("first wildcard mapping was not used for frontend request: routes %#v", seen)
	}
}

func TestSmokeRejectsInvalidPublishedPort(t *testing.T) {
	factory := smokeTestFactory(t)
	executor := &smokeExecutor{ports: map[string][]byte{"frontend": []byte("127.0.0.1:not-a-port\n")}}
	var stderr bytes.Buffer
	code := runWithExecutor(context.Background(), []string{"smoke", "--factory-dir", factory, "--api-url", "http://127.0.0.1:1"}, &bytes.Buffer{}, &stderr, executor)
	if code == 0 || !strings.Contains(stderr.String(), "invalid published port for frontend:8080") {
		t.Fatalf("smoke returned %d with diagnostic %q, want invalid-port error", code, stderr.String())
	}
}

func TestSmokeRejectsInvalidEnvFileBeforeCompose(t *testing.T) {
	factory := smokeTestFactory(t)
	executor := &smokeExecutor{}
	var stderr bytes.Buffer
	code := runWithExecutor(context.Background(), []string{"smoke", "--factory-dir", factory, "--env-file", "missing.env"}, &bytes.Buffer{}, &stderr, executor)
	if code == 0 || !strings.Contains(stderr.String(), "smoke env file") {
		t.Fatalf("smoke returned %d with diagnostic %q, want invalid env-file error", code, stderr.String())
	}
	if len(executor.calls) != 0 {
		t.Fatalf("invalid env file reached Compose: %#v", executor.calls)
	}
}

func TestPublishedURLUsesLoopbackForWildcardBinds(t *testing.T) {
	for _, test := range []struct {
		name string
		bind string
		want string
	}{
		{name: "IPv4 wildcard", bind: "0.0.0.0:8080", want: "http://127.0.0.1:8080"},
		{name: "IPv6 wildcard", bind: "[::]:8080", want: "http://[::1]:8080"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := publishedURL(test.bind, "service", 8080)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("publishedURL = %q, want %q", got, test.want)
			}
			parsed, err := url.Parse(got)
			if err != nil || parsed.Host != strings.TrimPrefix(test.want, "http://") {
				t.Fatalf("published URL does not preserve a valid host: %q, %v", got, err)
			}
		})
	}
}

type smokeExecutor struct {
	calls []recordedCall
	ports map[string][]byte
}

func (e *smokeExecutor) Run(_ context.Context, dir string, _ []string, name string, args ...string) ([]byte, []byte, error) {
	e.calls = append(e.calls, recordedCall{name: name, dir: dir, args: append([]string(nil), args...)})
	if len(args) < 4 || args[0] != "compose" {
		return nil, nil, fmt.Errorf("unexpected command: %s %v", name, args)
	}
	serviceIndex := 2
	if len(args) == 6 && args[1] == "--env-file" {
		serviceIndex = 4
	} else if len(args) != 4 {
		return nil, nil, fmt.Errorf("unexpected command: %s %v", name, args)
	}
	if args[serviceIndex-1] != "port" || args[serviceIndex+1] != "8080" {
		return nil, nil, fmt.Errorf("unexpected command: %s %v", name, args)
	}
	return e.ports[args[serviceIndex]], nil, nil
}

func smokeTestFactory(t *testing.T) string {
	t.Helper()
	factory := t.TempDir()
	initRepo(t, factory)
	if err := os.MkdirAll(filepath.Join(factory, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factory, "config", "ci.env"), []byte("UMIR_BIND=127.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return factory
}

func smokeTestServer(t *testing.T) (*httptest.Server, map[string]int) {
	t.Helper()
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path]++
		switch r.URL.Path {
		case "/api/serving/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-serving"}`); err != nil {
				t.Errorf("write serving health response: %v", err)
			}
		case "/api/warehouse/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-warehouse"}`); err != nil {
				t.Errorf("write warehouse health response: %v", err)
			}
		case "/":
			if err := writeTestResponse(w, `<html><div id="root"></div></html>`); err != nil {
				t.Errorf("write frontend response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	return server, seen
}
