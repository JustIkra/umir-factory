package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIndependentPublicProbesCanOverlap(t *testing.T) {
	rig := newBackendInfrastructureRig(t, "gateway")
	c := rig.client
	entered := make(chan struct{}, 2)
	releaseHTTP := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-releaseHTTP:
		case <-r.Context().Done():
			return
		}
		if _, err := w.Write([]byte(`{"csrfToken":"fixture-csrf"}`)); err != nil {
			return
		}
	}))
	defer server.Close()
	c.run.GatewayURL = server.URL
	c.run.Origin = server.URL
	b := newBackendSession(server.URL, server.URL)
	qualified := make(chan struct{}, 2)
	b.probe = func(ctx context.Context) (func(), error) {
		release, err := c.publicProbe(ctx)
		if err == nil {
			qualified <- struct{}{}
		}
		return release, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 2)
	go func() { _, err := b.call(ctx, "csrf", nil, nil, nil, 200); done <- err }()
	select {
	case <-entered:
		<-qualified
	case <-ctx.Done():
		close(releaseHTTP)
		t.Fatal("first request did not reach handler")
	}
	go func() { _, err := b.call(ctx, "csrf", nil, nil, nil, 200); done <- err }()
	select {
	case <-qualified:
	case <-ctx.Done():
		close(releaseHTTP)
		for range 2 {
			<-done
		}
		t.Fatal("second request did not qualify while the first response was in flight")
	}
	overlap := false
	select {
	case <-entered:
		overlap = true
	case <-time.After(250 * time.Millisecond):
	}
	close(releaseHTTP)
	for range 2 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	if !overlap {
		t.Fatalf("qualified public requests serialized while first response was in flight; total calls=%d", calls.Load())
	}
}

func TestIndependentRestartRejectsReplacementAfterClockProof(t *testing.T) {
	rig := newBackendInfrastructureRig(t, "warehouse")
	c := rig.client
	execute := c.execute
	started, replaced := false, false
	oldID, newID := strings.Repeat("c", 64), strings.Repeat("d", 64)
	c.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " start ") {
			started = true
		}
		raw, err := execute(ctx, input, args...)
		if started && strings.Contains(joined, " exec ") {
			var req fixtureRequest
			if json.Unmarshal(input, &req) == nil && req.Operation == "clock.read" {
				replaced = true
				return raw, err
			}
		}
		if replaced && (strings.Contains(joined, " ps ") || (len(args) > 0 && args[0] == "container" && !strings.Contains(joined, ".Mounts"))) {
			return []byte(strings.ReplaceAll(string(raw), oldID, newID)), err
		}
		return raw, err
	}
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	_, err := c.restart(ctx, "warehouse")
	if !replaced {
		t.Fatal("probe did not reach post-restart saved-clock proof")
	}
	if err == nil {
		t.Fatalf("unrequested replacement accepted after clock proof: new binding=%t restart receipts=%d", c.containers["warehouse"] == newID, len(c.restarts))
	}
}

func TestIndependentRawPublicCallStillQualifiesOwnership(t *testing.T) {
	rig := newBackendInfrastructureRig(t, "gateway")
	c := rig.client
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		if _, err := w.Write([]byte(`{"code":"InvalidArgument","outcome":"not_committed","message":"Denied","retryable":false}`)); err != nil {
			return
		}
	}))
	defer server.Close()
	c.run.GatewayURL = server.URL
	c.run.Origin = server.URL
	b := newBackendSession(server.URL, server.URL)
	b.probe = c.publicProbe
	address := strings.TrimPrefix(server.URL, "http://")
	b.client.Transport = backendProbeTransport(address)
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	if err := c.qualifyProbe(ctx, "gateway"); err != nil {
		t.Fatal(err)
	}
	execute := c.execute
	c.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		raw, err := execute(ctx, input, args...)
		if strings.Contains(strings.Join(args, " "), " port ") {
			return []byte("127.0.0.1:1"), nil
		}
		return raw, err
	}
	if _, err := b.call(ctx, "csrf", nil, nil, nil, 200); err == nil {
		t.Fatal("probe did not arrange stale publisher")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v2/feedback", strings.NewReader(`{"tenantID":"spoof"}`))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := b.client.Do(req)
	if reply != nil {
		if closeErr := reply.Body.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}
	if err == nil || calls.Load() != 0 {
		t.Fatalf("raw public request reached stale listener without qualification; HTTP calls=%d", calls.Load())
	}
}

func TestBackendRawPublicBoundaryBudgetsAndOwnership(t *testing.T) {
	for _, failure := range []string{"none", "stale", "replaced", "foreign", "unhealthy", "redirect", "response-oversize", "request-oversize", "header-oversize", "outside", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "gateway")
			c := rig.client
			var calls atomic.Int32
			var outsideCalls atomic.Int32
			outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { outsideCalls.Add(1); w.WriteHeader(400) }))
			defer outside.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if failure == "redirect" {
					http.Redirect(w, r, outside.URL, http.StatusFound)
					return
				}
				w.WriteHeader(400)
				body := `{"code":"InvalidArgument","outcome":"not_committed","message":"Denied","retryable":false}`
				if failure == "response-oversize" {
					body = strings.Repeat("x", (1<<20)+1)
				}
				if _, err := w.Write([]byte(body)); err != nil {
					return
				}
			}))
			defer server.Close()
			c.run.GatewayURL, c.run.Origin = server.URL, server.URL
			b := newBackendSession(server.URL, server.URL)
			b.probe = c.publicProbe
			execute := c.execute
			c.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
				raw, err := execute(ctx, input, args...)
				joined := strings.Join(args, " ")
				switch failure {
				case "stale":
					if strings.Contains(joined, " port ") {
						return []byte("127.0.0.1:1"), nil
					}
				case "replaced":
					raw = []byte(strings.ReplaceAll(string(raw), strings.Repeat("c", 64), strings.Repeat("d", 64)))
				case "foreign":
					raw = []byte(strings.ReplaceAll(string(raw), c.run.Project, "umir-backend-ffffffffffffffffffffffff"))
				case "unhealthy":
					if strings.Contains(joined, ".NetworkSettings.Ports") {
						raw = []byte(strings.ReplaceAll(string(raw), `"healthy"`, `"unhealthy"`))
					}
				}
				return raw, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			body := `{"tenantID":"spoof"}`
			if failure == "request-oversize" {
				body = strings.Repeat("x", (1<<20)+1)
			}
			endpoint := server.URL
			if failure == "outside" {
				endpoint = outside.URL
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/v2/feedback", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if failure == "header-oversize" {
				req.Header.Set("X-Test", strings.Repeat("x", 16385))
			}
			if failure == "cancel" {
				cancel()
			}
			reply, err := b.client.Do(req)
			if reply != nil {
				_, readErr := io.ReadAll(reply.Body)
				err = errors.Join(err, readErr, reply.Body.Close())
			}
			if failure == "none" {
				if err != nil || reply.StatusCode != 400 || calls.Load() != 1 {
					t.Fatal("qualified raw refusal failed", err)
				}
			} else if err == nil {
				t.Fatal("raw public boundary failed open")
			}
			expected := int32(0)
			if failure == "none" || failure == "redirect" || failure == "response-oversize" {
				expected = 1
			}
			if calls.Load() != expected || outsideCalls.Load() != 0 {
				t.Fatal("unexpected HTTP request count")
			}
			release, err := c.lockPrivate(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
}

func TestBackendPublicResponseLeaseExcludesRestart(t *testing.T) {
	for _, completion := range []string{"close", "cancel", "eof"} {
		t.Run(completion, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "gateway")
			c := rig.client
			releaseHTTP := make(chan struct{})
			defer close(releaseHTTP)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				if err := http.NewResponseController(w).Flush(); err != nil {
					return
				}
				if completion == "eof" {
					return
				}
				select {
				case <-releaseHTTP:
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			c.run.GatewayURL, c.run.Origin = server.URL, server.URL
			b := newBackendSession(server.URL, server.URL)
			b.probe = c.publicProbe
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v2/auth/csrf", nil)
			if err != nil {
				t.Fatal(err)
			}
			reply, err := b.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			waiting, stop := context.WithTimeout(ctx, 30*time.Millisecond)
			release, err := c.lockPrivate(waiting)
			stop()
			if err == nil {
				release()
				t.Fatal("restart admitted with public response in flight")
			}
			switch completion {
			case "cancel":
				cancel()
			case "eof":
				if _, err := io.ReadAll(reply.Body); err != nil {
					t.Fatal(err)
				}
			case "close":
				if err := reply.Body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			admitted, finish := context.WithTimeout(context.Background(), time.Second)
			defer finish()
			release, err = c.lockPrivate(admitted)
			if err != nil {
				t.Fatal("completed public lease did not release", err)
			}
			blocked, stop := context.WithTimeout(admitted, 30*time.Millisecond)
			if publicRelease, err := c.publicProbe(blocked); err == nil {
				publicRelease()
				release()
				stop()
				t.Fatal("public request admitted during exclusive restart")
			}
			stop()
			release()
			if err := reply.Body.Close(); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestBackendRestartReplacementRetainsUnqualifiedState(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		name := "restart"
		if recreate {
			name = "recreate"
		}
		t.Run(name, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "warehouse")
			c := rig.client
			execute := c.execute
			started, replaced, starts := false, false, 0
			oldID := strings.Repeat("c", 64)
			expectedID := oldID
			if recreate {
				expectedID = strings.Repeat("d", 64)
			}
			replacedID := strings.Repeat("e", 64)
			c.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, " up ") {
					started = true
					starts++
					return execute(ctx, input, c.compose("start", "warehouse")...)
				}
				if strings.Contains(joined, " start ") {
					started = true
					starts++
				}
				raw, err := execute(ctx, input, args...)
				if started && strings.Contains(joined, " exec ") {
					var req fixtureRequest
					if json.Unmarshal(input, &req) == nil && req.Operation == "clock.read" {
						replaced = true
						return raw, err
					}
				}
				if started && (strings.Contains(joined, " ps ") || len(args) > 0 && args[0] == "container" && !strings.Contains(joined, ".Mounts")) {
					id := expectedID
					if replaced {
						id = replacedID
					}
					return []byte(strings.ReplaceAll(string(raw), oldID, id)), err
				}
				return raw, err
			}
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			priorEndpoint := c.run.PrivateServiceURLs["warehouse"]
			if _, err := c.restartMode(ctx, "warehouse", recreate); err == nil || !replaced {
				t.Fatal("late replacement did not reject")
			}
			if starts != 1 || len(c.restarts) != 0 || !c.unqualified["warehouse"] || c.containers["warehouse"] != oldID || c.run.PrivateServiceURLs["warehouse"] != priorEndpoint {
				t.Fatal("failed restart advanced qualified metadata")
			}
			if _, err := os.Stat(filepath.Join(c.run.Fixtures.HarnessPrivateRoot, "private-probe-state.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed restart published clock proof", err)
			}
			if err := c.qualifyProbe(ctx, "warehouse"); err == nil {
				t.Fatal("failed restart allowed subsequent probe")
			}
		})
	}
}
