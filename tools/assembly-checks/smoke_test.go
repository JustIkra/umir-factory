package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSmokeRoutes(t *testing.T) {
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path]++
		switch r.URL.Path {
		case "/api/serving/healthz":
			fmt.Fprint(w, `{"status":"ok","service":"umir-serving"}`)
		case "/api/warehouse/healthz":
			fmt.Fprint(w, `{"status":"ok","service":"umir-warehouse"}`)
		case "/":
			fmt.Fprint(w, `<html><div id="root"></div></html>`)
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
		fmt.Fprint(w, `{"status":"ok","service":"umir-serving","extra":"unexpected"}`)
	}))
	defer server.Close()
	if err := runSmoke(context.Background(), server.Client(), smokeTargets{Gateways: []string{server.URL}, Frontend: server.URL}); err == nil {
		t.Fatal("smoke accepted an extra health response field")
	}
}

func TestDefaultHTTPTimeoutIsFiveSeconds(t *testing.T) {
	if got := defaultHTTPClient().Timeout; got != 5*time.Second {
		t.Fatalf("HTTP timeout = %s, want 5s", got)
	}
}
