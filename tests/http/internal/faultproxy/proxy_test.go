package faultproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHoldBarrierCapturesBeforeForwardAndReleasesExactlyOnce(t *testing.T) {
	forwarded := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded <- struct{}{}
		writeTestBody(t, w, `{"ok":true}`)
	}))
	defer upstream.Close()
	proxy, err := New(upstream.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := controlTestServer(t, proxy)
	defer server.Close()
	control := NewControlClient(server.URL, "test-token")
	if err := control.Arm(context.Background(), Rule{ID: "hold", Method: http.MethodPost, Path: "/internal/v1/order-commits", Action: HoldBeforeForward, MaxUses: 1, Timeout: 2 * time.Second, DetachOnRelease: true}); err != nil {
		t.Fatal(err)
	}
	response := make(chan error, 1)
	go func() {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/internal/v1/order-commits", strings.NewReader(`{}`))
		if err != nil {
			response <- err
			return
		}
		result, err := server.Client().Do(request)
		if result != nil {
			if closeErr := result.Body.Close(); err == nil {
				err = closeErr
			}
		}
		response <- err
	}()
	if _, err := control.WaitEvent(context.Background(), "hold", EventCaptured, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-forwarded:
		t.Fatal("request reached upstream before release")
	default:
	}
	if err := control.Release(context.Background(), "hold"); err != nil {
		t.Fatal(err)
	}
	if _, err := control.WaitEvent(context.Background(), "hold", EventUpstreamResult, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-response:
		if err != nil {
			t.Fatalf("client request failed after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not finish after release")
	}
}

func TestDropResponseForwardsBusinessRequestThenReturnsEOF(t *testing.T) {
	forwarded := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded <- struct{}{}
		writeTestBody(t, w, `{"kind":"OrderCommitted"}`)
	}))
	defer upstream.Close()
	proxy, err := New(upstream.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := controlTestServer(t, proxy)
	defer server.Close()
	control := NewControlClient(server.URL, "test-token")
	if err := control.Arm(context.Background(), Rule{ID: "drop", Path: "/internal/v1/order-commits", Action: DropResponse, MaxUses: 1}); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/internal/v1/order-commits", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response, requestErr := server.Client().Do(request)
	if response != nil {
		if err := response.Body.Close(); requestErr == nil {
			requestErr = err
		}
	}
	if requestErr == nil {
		t.Fatal("lost response was reported as a successful HTTP response")
	}
	select {
	case <-forwarded:
	case <-time.After(time.Second):
		t.Fatal("fault proxy did not forward the business request")
	}
	if _, err := control.WaitEvent(context.Background(), "drop", EventUpstreamResult, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestTrafficStatsCountBusinessForwardingSeparatelyFromControlCalls(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestBody(t, w, "ok")
	}))
	defer upstream.Close()
	proxy, err := New(upstream.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := controlTestServer(t, proxy)
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/internal/v1/order-attempts")
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	control := NewControlClient(server.URL, "test-token")
	traffic, err := control.Stats(context.Background(), http.MethodGet, "/internal/v1/order-attempts")
	if err != nil {
		t.Fatal(err)
	}
	if traffic.Requests != 1 || traffic.Forwarded != 1 {
		t.Fatalf("traffic stats = %#v, want one business request and forward", traffic)
	}
	if err := control.ResetStats(context.Background()); err != nil {
		t.Fatal(err)
	}
	traffic, err = control.Stats(context.Background(), http.MethodGet, "/internal/v1/order-attempts")
	if err != nil || traffic.Requests != 0 || traffic.Forwarded != 0 {
		t.Fatalf("reset traffic stats = %#v, %v", traffic, err)
	}
}

func TestCorruptResponsePreservesHTTPStatusAndChangesPayload(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeTestBody(t, w, `{"kind":"OrderCommitted"}`)
	}))
	defer upstream.Close()
	proxy, err := New(upstream.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := controlTestServer(t, proxy)
	defer server.Close()
	control := NewControlClient(server.URL, "test-token")
	if err := control.Arm(context.Background(), Rule{ID: "corrupt", Path: "/internal/v1/order-commits", Action: CorruptJSON, MaxUses: 1}); err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Post(server.URL+"/internal/v1/order-commits", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close response body: %v", err)
		}
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || json.Valid(body) {
		t.Fatalf("response = %d %s, want status 200 and malformed JSON", response.StatusCode, body)
	}
}

func TestPublicHealthRequestIsForwardedToRealService(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestBody(t, w, `{"status":"ok","service":"umir-serving"}`)
	}))
	defer upstream.Close()
	proxy, err := New(upstream.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	public := httptest.NewServer(proxy)
	defer public.Close()
	response, err := public.Client().Get(public.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close response body: %v", err)
		}
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != `{"status":"ok","service":"umir-serving"}` {
		t.Fatalf("public health response = %d %s", response.StatusCode, body)
	}
}

func TestControlRejectsWrongTokenAndRuleCannotMatchOtherPaths(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeTestBody(t, w, "ok") }))
	defer upstream.Close()
	proxy, err := New(upstream.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := controlTestServer(t, proxy)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/control/rules", strings.NewReader(`{"id":"bad","path":"/x","action":"hold-before-forward","maxUses":1}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer wrong")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", response.StatusCode)
	}
}

func controlTestServer(t *testing.T, proxy *Proxy) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/control/", proxy.ControlHandler())
	mux.Handle("/healthz", proxy.ControlHandler())
	mux.Handle("/", proxy)
	return httptest.NewServer(mux)
}

func writeTestBody(t *testing.T, writer io.Writer, value string) {
	t.Helper()
	if _, err := io.WriteString(writer, value); err != nil {
		t.Errorf("write test response: %v", err)
	}
}
