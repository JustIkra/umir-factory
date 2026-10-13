package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendPublicFailureEvidenceRedactsSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		if _, e := w.Write([]byte(`{"password":"synthetic-secret","cookie":"private-cookie"}`)); e != nil {
			t.Error(e)
		}
	}))
	defer server.Close()
	b := newBackendSession(server.URL, server.URL)
	_, e := b.call(context.Background(), "me", nil, nil, nil, 200)
	if e == nil || strings.Contains(e.Error(), "synthetic-secret") {
		t.Fatal("unsafe failure evidence")
	}
}
func TestBackendFlagsRequirePrivateRunAndKeepFailureResult(t *testing.T) {
	var out, errout bytes.Buffer
	dir := t.TempDir()
	result := filepath.Join(dir, "result.json")
	code := run([]string{"--backend-run", filepath.Join(dir, "missing.json"), "--backend-result", result}, &out, &errout)
	if code == 0 {
		t.Fatal("missing descriptor passed")
	}
	raw, e := os.ReadFile(result)
	if e != nil {
		t.Fatal(e)
	}
	var parsed struct {
		Status string
		Passed []string
	}
	if json.Unmarshal(raw, &parsed) != nil || parsed.Status != "incomplete" || len(parsed.Passed) != 0 {
		t.Fatal("false acceptance")
	}
}

func TestBackendRefusalDoesNotInventUnknownOutcome(t *testing.T) {
	b := newBackendSession("http://127.0.0.1:1", "http://127.0.0.1:1")
	for _, outcome := range []string{"unknown", "unavailable", "indeterminate", "committed"} {
		raw, e := json.Marshal(map[string]any{"code": "InvalidArgument", "outcome": outcome, "message": "synthetic", "retryable": false})
		if e != nil {
			t.Fatal(e)
		}
		if e := b.validateRefusal(raw); e == nil {
			t.Fatal("non-refusal interpreted as no effect")
		}
	}
	raw, e := json.Marshal(map[string]any{"code": "InvalidArgument", "outcome": "not_committed", "message": "synthetic", "retryable": false})
	if e != nil {
		t.Fatal(e)
	}
	if e := b.validateRefusal(raw); e != nil {
		t.Fatal(e)
	}
}

func TestBackendPublicClientLoadsInheritedPathParametersAndCallsHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/csrf" {
			t.Error("unexpected route")
		}
		if _, e := w.Write([]byte(`{"csrfToken":"synthetic-private"}`)); e != nil {
			t.Error(e)
		}
	}))
	defer server.Close()
	b := newBackendSession(server.URL, server.URL)
	if b == nil {
		t.Fatal("client rejected valid frozen document with inherited path parameters")
	}
	if e := b.csrfToken(context.Background()); e != nil {
		t.Fatal(e)
	}
	if calls != 1 || len(b.observations) != 1 || !b.observations[0].ResponseValid {
		t.Fatal("HTTP call not observed")
	}
}
