package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE34MechanicsMissingProfileFailsClosed(t *testing.T) {
	err := runBackendE34(context.Background(), backendScenarioInput{}, "BE-E4-16")
	if !errors.Is(err, errBackendScenarioUnimplemented) || !strings.Contains(err.Error(), "independently READY missing-tzdata profile") {
		t.Fatalf("missing profile must produce its precise request: %v", err)
	}
}

func TestE34MechanicsExactFactsFailClosed(t *testing.T) {
	for _, bad := range []any{nil, float64(9007199254740993), json.Number("9007199254740993"), "9007199254740992"} {
		if err := e34Expect("/quantity/largeExactString", bad, "9007199254740993"); err == nil {
			t.Fatalf("accepted absent, rounded or differently typed fact: %T", bad)
		}
	}
	if err := e34Expect("/quantity/largeExactString", "9007199254740993", "9007199254740993"); err != nil {
		t.Fatal(err)
	}
	if err := e34Expect("/coverage/complete", false, false); err != nil {
		t.Fatal(err)
	}
	for _, parent := range []any{nil, map[string]any{}, map[string]any{"rows": []any{}, "nextCursor": nil}} {
		if err := e34Absent("/page/lastCursor", parent, "nextCursor"); err == nil {
			t.Fatal("missing/empty parent or explicit null proved absence")
		}
	}
	if err := e34Absent("/page/lastCursor", map[string]any{"items": []any{}}, "nextCursor"); err != nil {
		t.Fatal(err)
	}
}

func TestE34MechanicsJournalRequiresScopeEpochAndIdentity(t *testing.T) {
	scope := backendScope{"org", "tenant"}
	makeRecord := func(tenant, epoch, id string) map[string]any {
		return map[string]any{"tenantID": tenant, "runtimeEpoch": epoch, "event": map[string]any{"eventID": id, "type": "BatchExpiryPending"}}
	}
	for _, rows := range [][]any{
		{makeRecord("foreign", "epoch", "one")},
		{makeRecord("tenant", "old", "one")},
		{makeRecord("tenant", "epoch", "")},
		{makeRecord("tenant", "epoch", "one"), makeRecord("tenant", "epoch", "one")},
		{nil},
	} {
		if _, err := e34Events(map[string]any{"result": map[string]any{"events": rows}}, scope, "epoch"); err == nil {
			t.Fatal("unsafe journal evidence accepted")
		}
	}
	if _, err := e34Events(map[string]any{}, scope, "epoch"); err == nil {
		t.Fatal("absent journal proved zero facts")
	}
	if _, err := e34Events(map[string]any{"result": map[string]any{"events": make([]any, 500)}}, scope, "epoch"); err == nil {
		t.Fatal("possibly truncated journal accepted")
	}
	good := map[string]any{"result": map[string]any{"events": []any{makeRecord("tenant", "epoch", "one")}}}
	events, err := e34Events(good, scope, "epoch")
	if err != nil || e34Count(events, "BatchExpiryPending") != 1 {
		t.Fatalf("valid correlated owner facts rejected: %v", err)
	}
}

func TestE34MechanicsRowsCannotHideMissingOrDuplicates(t *testing.T) {
	for _, data := range []any{nil, map[string]any{}, map[string]any{"items": []any{nil}}, map[string]any{"items": []any{map[string]any{"productID": "p"}, map[string]any{"productID": "p"}}}} {
		if _, err := e34Row(data, "productID", "p", "items"); err == nil {
			t.Fatal("unreliable owner row accepted")
		}
	}
}

func TestE34MechanicsIntervalsUseResolvedInstants(t *testing.T) {
	if !e34SameInstant("2026-03-28T23:00:00Z", "2026-03-29T00:00:00+01:00") || e34SameInstant(nil, "2026-03-29T00:00:00+01:00") || e34SameInstant("invalid", "invalid") {
		t.Fatal("equivalent offsets or absent instants mishandled")
	}
	for _, test := range []struct {
		from, to string
		hours    time.Duration
	}{
		{"2026-03-29T00:00:00+01:00", "2026-03-30T00:00:00+02:00", 23 * time.Hour},
		{"2026-10-25T00:00:00+02:00", "2026-10-26T00:00:00+01:00", 25 * time.Hour},
	} {
		got, err := e34Duration(map[string]any{"from": test.from, "to": test.to}, "from", "to")
		if err != nil || got != test.hours {
			t.Fatalf("physical interval differs: %v %v", got, err)
		}
	}
	for _, data := range []map[string]any{{}, {"from": "bad", "to": "bad"}, {"from": "2026-10-13T00:00:00+03:00", "to": "2026-10-12T00:00:00+03:00"}} {
		if _, err := e34Duration(data, "from", "to"); err == nil {
			t.Fatal("missing/reversed instants accepted")
		}
	}
}

func TestE34MechanicsAssignmentAndIncompleteRequests(t *testing.T) {
	for stage, count := range map[string]int{"E3": 12, "E4": 18} {
		for index := 1; index <= count; index++ {
			id := backendCaseID(stage, index)
			_, body := e34Bodies[id]
			request, incomplete := e34OwnerRequests[id]
			if body == incomplete {
				t.Fatalf("case must have exactly one executable body or request: %s", id)
			}
			err := runBackendE34(context.Background(), backendScenarioInput{}, id)
			if err == nil || !errors.Is(err, errBackendScenarioUnimplemented) {
				t.Fatalf("missing real input cannot pass: %s %v", id, err)
			}
			if incomplete && !strings.Contains(err.Error(), request) {
				t.Fatalf("owner request lost: %s", id)
			}
		}
	}
	if len(e34Bodies)+len(e34OwnerRequests) != 30 {
		t.Fatal("assignment drift")
	}
	for _, id := range []string{"BE-E1-01", "BE-E3-13", "BE-E4-19", "BE-E4-016"} {
		if err := runBackendE34(context.Background(), backendScenarioInput{}, id); err == nil {
			t.Fatal("unassigned case passed")
		}
	}
}

type e34MechanicsTransport func(*http.Request) (*http.Response, error)

func (f e34MechanicsTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestE34MechanicsTypedResponseAndQualification(t *testing.T) {
	const source = `{"context":{"orgID":"22222222-2222-4222-8222-222222222222","tenantID":"33333333-3333-4333-8333-333333333333","runtimeEpoch":"44444444-4444-4444-8444-444444444444","settings":{"timeZone":"Europe/Moscow","settingsVersion":"1"},"evaluatedAt":"2026-10-13T01:00:00+03:00","stateVersion":"1"},"result":{"evaluatedAt":"2026-10-13T01:00:00+03:00","stateVersion":"1","items":[]}}`
	for _, test := range []struct {
		name, raw          string
		qualified, success bool
	}{
		{"schema-valid mechanics reply", source, true, true},
		{"missing captured context", `{"result":{"items":[]}}`, true, false},
		{"denied qualification", source, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := newBackendSession("http://127.0.0.1:18080", "http://127.0.0.1:18080")
			if b == nil {
				t.Fatal("session unavailable")
			}
			calls := 0
			b.probe = func(context.Context) (func(), error) {
				if !test.qualified {
					return nil, errors.New("mechanics ownership refusal")
				}
				return func() {}, nil
			}
			b.client.Transport = e34MechanicsTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(test.raw))}, nil
			})
			_, err := e34HTTP(context.Background(), b, "products", map[string]string{"restaurantID": "33333333-3333-4333-8333-333333333333"}, url.Values{}, nil, nil, 200)
			if (err == nil) != test.success || !test.qualified && calls != 0 {
				t.Fatalf("mechanics boundary result: calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestE34MechanicsPrivateFactsRejectSecretsAndRetainExactBytes(t *testing.T) {
	facts := &e34Facts{}
	ctx := context.WithValue(context.Background(), e34FactsKey{}, facts)
	raw := []byte(`{"quantity":"9007199254740993"}`)
	e34Retain(ctx, "inventory", nil, raw, 200)
	raw[2] = 'X'
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := &backendClient{run: backendRunDescriptor{Fixtures: fixtureDescriptor{HarnessPrivateRoot: root}}}
	if err := facts.save(client, "BE-E3-02", false); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(client.run.Fixtures.HarnessPrivateRoot, "backend-e34-*-facts.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("private facts missing: %v", err)
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private source facts exposed: %v", err)
	}
	saved, err := os.ReadFile(files[0])
	if err != nil || !strings.Contains(string(saved), `"quantity":"9007199254740993"`) || !strings.Contains(string(saved), `"assertionsSucceeded":false`) {
		t.Fatalf("exact source bytes/outcome changed: %v", err)
	}
	private := &e34Facts{}
	privateCtx := context.WithValue(context.Background(), e34FactsKey{}, private)
	e34Retain(privateCtx, "login", []byte(`{"password":"mechanics-sentinel"}`), []byte(`{"session":"mechanics-sentinel"}`), 200)
	if len(private.rows) != 0 || private.save(client, "BE-E3-02", true) == nil {
		t.Fatal("secret-bearing operation entered source evidence")
	}
}
