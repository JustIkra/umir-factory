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
	if err := runSmoke(context.Background(), client, smokeTargets{Gateways: []string{server.URL, server.URL}, Frontend: server.URL}); err != nil {
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
	if err := runSmoke(context.Background(), server.Client(), smokeTargets{Gateways: []string{server.URL}, Frontend: server.URL}); err == nil {
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
