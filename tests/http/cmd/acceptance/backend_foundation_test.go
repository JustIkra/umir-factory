package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

func TestScenarioRoutingAndPlaceholdersFailClosed(t *testing.T) {
	counts := map[string]int{}
	for stage, limit := range map[string]int{"E1": 14, "E2": 8, "E3": 12, "E4": 18, "E5": 15} {
		for i := 1; i <= limit; i++ {
			id := backendCaseID(stage, i)
			group, err := backendScenarioGroup(id)
			if err != nil {
				t.Fatal(err)
			}
			counts[group]++
			if id != "BE-E1-02" && id != "BE-E1-12" {
				if err := dispatchBackendScenario(context.Background(), backendScenarioInput{}, id); !errors.Is(err, errBackendScenarioUnimplemented) {
					t.Fatalf("%s granted readiness: %v", id, err)
				}
			}
		}
	}
	if counts["e12"] != 22 || counts["e34"] != 30 || counts["e5"] != 15 {
		t.Fatal(counts)
	}
	for _, id := range []string{"BE-E1-00", "BE-E1-15", "BE-E4-19", "BE-E5-16", "BE-E6-01", "BE-E1-2", "BE-E1-+1"} {
		if _, err := backendScenarioGroup(id); err == nil {
			t.Fatal("foreign case routed")
		}
	}
}
func TestScenarioBudgetCannotExtendParent(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	child, stop := backendScenarioContext(parent)
	defer stop()
	pd, _ := parent.Deadline()
	cd, _ := child.Deadline()
	if cd.After(pd) {
		t.Fatal("extended budget")
	}
	cancel()
	if _, err := backendWaitMilliseconds(child, time.Second); err == nil {
		t.Fatal("expired budget accepted")
	}
	if _, err := backendWaitMilliseconds(context.Background(), time.Second); err == nil {
		t.Fatal("unbounded context accepted")
	}
}
func TestControlCacheComparisonIncludesIncompleteReply(t *testing.T) {
	request := []byte(`{"requestID":"synthetic","data":{"waitMs":0}}`)
	reply := []byte(`{"data":{"complete":false,"records":[]}}`)
	if err := equalControlReplay(request, reply, request, reply); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ request, reply []byte }{{[]byte(`{"requestID":"synthetic","data":{"waitMs":1}}`), reply}, {request, []byte(`{"data":{"complete":true,"records":[]}}`)}} {
		if err := equalControlReplay(request, reply, mutation.request, mutation.reply); err == nil {
			t.Fatal("cache mutation accepted")
		}
	}
}
func TestRestartConfigOnlyReplacesObservedClock(t *testing.T) {
	raw, err := os.ReadFile("../../internal/faultproxy/protocol-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var examples struct {
		Positive []struct {
			Schema string
			Value  json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &examples); err != nil {
		t.Fatal(err)
	}
	var config []byte
	for _, ex := range examples.Positive {
		if ex.Schema == "#/$defs/OwnerConfig" {
			config = ex.Value
			break
		}
	}
	var data map[string]any
	if err := json.Unmarshal(config, &data); err != nil {
		t.Fatal(err)
	}
	clock := fixtureClock{Owner: "serving", InstanceID: backendTestID, FixtureID: text(data["fixtureID"]), Generation: "7", Instant: "2026-10-13T10:00:00+03:00", Source: "owner-injected", Healthy: true, InjectedConsumers: []string{"JWT", "Serving"}}
	run := backendRunDescriptor{Project: text(data["project"]), Fixtures: fixtureDescriptor{FixtureID: clock.FixtureID, Owners: map[string]fixtureOwner{"serving": {ConfigSHA256: faultproxy.SHA256(config)}}}}
	updated, err := backendRestartConfig(config, run, "serving", clock)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(updated, &actual); err != nil {
		t.Fatal(err)
	}
	if actual["initialInstant"] != clock.Instant || actual["clockGeneration"] != "7" {
		t.Fatal("checkpoint lost")
	}
	actual["initialInstant"] = data["initialInstant"]
	actual["clockGeneration"] = data["clockGeneration"]
	a, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("nonclock config changed")
	}
	clock.Owner = "auth"
	if _, err := backendRestartConfig(config, run, "serving", clock); err == nil {
		t.Fatal("foreign checkpoint accepted")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceBackendConfig(path, updated); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := replaceBackendConfig(link, updated); err == nil {
		t.Fatal("symlink replaced")
	}
}
func TestRestartReadbackRejectsSameInstanceOrChangedTime(t *testing.T) {
	before := fixtureClock{Owner: "feedback", InstanceID: backendTestID, FixtureID: backendOtherID, Source: "owner-injected", Generation: "7", Instant: "2026-10-13T10:00:00+03:00", Healthy: true, InjectedConsumers: []string{"Journal", "Worker"}}
	after := before
	after.InstanceID = "33333333-3333-4333-8333-333333333333"
	if err := validateBackendRestartClock(before, after); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*fixtureClock){func(c *fixtureClock) { c.InstanceID = before.InstanceID }, func(c *fixtureClock) { c.Generation = "8" }, func(c *fixtureClock) { c.Instant = "2026-10-13T10:01:00+03:00" }, func(c *fixtureClock) { c.FixtureID = backendTestID }, func(c *fixtureClock) { c.Healthy = false }} {
		bad := after
		change(&bad)
		if err := validateBackendRestartClock(before, bad); err == nil {
			t.Fatal("unproved restart accepted")
		}
	}
}
func TestRestartOwnershipFailureCannotStopOrReplace(t *testing.T) {
	calls := []string{}
	c := newBackendClient(backendRunDescriptor{Project: "umir-backend-0123456789abcdef01234567"})
	c.execute = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return []byte("foreign"), nil
	}
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	if _, err := c.restart(ctx, "auth"); err == nil {
		t.Fatal("foreign restart accepted")
	}
	for _, call := range calls {
		if strings.Contains(call, " stop ") || strings.Contains(call, " start ") {
			t.Fatal("foreign mutation")
		}
	}
}

func TestPublicRawHelperPreservesBusinessBytesAndQuery(t *testing.T) {
	received := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received = append(received, r.URL.RawQuery+"|"+string(raw))
		w.WriteHeader(400)
		if _, err := w.Write([]byte(`{"code":"InvalidArgument","outcome":"not_committed","message":"synthetic","retryable":false}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	session := newBackendSession(server.URL, server.URL)
	payload := []byte(`{"commandID":"original-business","commandID":"duplicate"}`)
	query := url.Values{"from": []string{"first", "second"}}
	for range 2 {
		if _, err := session.callBytes(context.Background(), "createOrganization", nil, query, payload, nil, false, 400); err != nil {
			t.Fatal(err)
		}
	}
	if len(received) != 2 || received[0] != received[1] || !strings.Contains(received[0], string(payload)) {
		t.Fatal("business bytes changed")
	}
	if session.publicReply().Status != 400 {
		t.Fatal("reply status missing")
	}
	if session.observations[0].RequestValid {
		t.Fatal("malformed request reported valid")
	}
}

func TestPublicConcurrentCallsKeepIndependentEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"csrfToken":"synthetic-private"}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	session := newBackendSession(server.URL, server.URL)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := session.call(context.Background(), "csrf", nil, nil, nil, 200); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(session.observations) != 8 {
		t.Fatal("lost HTTP observation")
	}
	for _, observation := range session.observations {
		if !observation.ResponseValid {
			t.Fatal("misassociated HTTP evidence")
		}
	}
}
