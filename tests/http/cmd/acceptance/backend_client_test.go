package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

func TestBackendDiscoveryRejectsMismatchedDurableEdge(t *testing.T) {
	selector := map[string]any{"kind": "command", "challengeID": backendTestID, "commandID": "invite-begin", "operationID": "beginInvitationChallenge"}
	data := map[string]any{"instanceID": backendTestID, "clockGeneration": "1", "aggregateRevision": "3", "commandAggregateRevision": "2", "selector": selector, "jobs": []any{map[string]any{"jobID": backendOtherID, "challengeID": backendTestID, "channel": "smtp", "state": "pending", "attempts": 0, "acceptedAt": "2026-10-13T09:00:00+03:00", "expiresAt": "2026-10-13T09:05:00+03:00"}}, "complete": true}
	clock := fixtureClock{Owner: "auth", InstanceID: backendTestID, Generation: "1", Instant: "2026-10-13T09:00:00+03:00"}
	if _, e := validateDiscoveredJobs(data, selector, clock, "2026-10-13T09:05:00+03:00"); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(map[string]any){func(d map[string]any) { d["complete"] = false }, func(d map[string]any) { d["commandAggregateRevision"] = "4" }, func(d map[string]any) { d["instanceID"] = backendOtherID }, func(d map[string]any) { d["jobs"] = []any{} }, func(d map[string]any) {
		j, ok := d["jobs"].([]any)
		if !ok {
			t.Fatal("jobs shape")
		}
		d["jobs"] = append(j, j[0])
	}} {
		raw, e := json.Marshal(data)
		if e != nil {
			t.Fatal(e)
		}
		var mutated map[string]any
		if e := json.Unmarshal(raw, &mutated); e != nil {
			t.Fatal(e)
		}
		change(mutated)
		if _, e := validateDiscoveredJobs(mutated, selector, clock, "2026-10-13T09:05:00+03:00"); e == nil {
			t.Fatal("accepted mismatched edge")
		}
	}
}
func TestBackendControlErrorsNeverAuthorizeRetry(t *testing.T) {
	request := fixtureRequest{1, "umir-backend-0123456789abcdef01234567", backendTestID, "owner.jobs.discover", map[string]any{}}
	for _, response := range []map[string]any{
		{"version": 1, "project": request.Project, "requestID": request.RequestID, "operation": request.Operation, "error": map[string]any{"code": "OutcomeUnknown", "effect": "unknown", "retryable": true}},
		{"version": 1, "project": request.Project, "requestID": backendOtherID, "operation": request.Operation, "data": map[string]any{}},
	} {
		b, e := json.Marshal(response)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := validateControlResponse(request, b); e == nil {
			t.Fatal("unsafe control response accepted")
		}
	}
}
func TestBackendControlOwnershipPrecedesHelper(t *testing.T) {
	calls := 0
	c := backendClient{run: backendRunDescriptor{Project: "umir-backend-0123456789abcdef01234567"}, execute: func(context.Context, []byte, ...string) ([]byte, error) { calls++; return []byte("foreign"), nil }}
	if _, e := c.control(context.Background(), "auth", "clock.read", map[string]any{}); e == nil {
		t.Fatal("foreign container accepted")
	}
	if calls > 1 {
		t.Fatal("helper ran before ownership proof")
	}
	if !faultproxy.UUID(backendTestID) {
		t.Fatal("bad test UUID")
	}
}

const backendTestID = "11111111-1111-4111-8111-111111111111"
const backendOtherID = "22222222-2222-4222-8222-222222222222"

func TestRestartCheckpointUsesCanonicalOwnerClock(t *testing.T) {
	clock := map[string]any{"owner": "auth", "instanceID": backendTestID, "source": "owner-injected", "fixtureID": backendOtherID, "generation": "3", "instant": "2026-10-13T09:00:00+03:00", "injectedConsumers": []any{"Accounts", "JWT", "OTP", "Outbox"}, "healthy": true}
	raw, e := json.Marshal(clock)
	if e != nil {
		t.Fatal(e)
	}
	data := map[string]any{"clock": clock, "checkpointSHA256": faultproxy.SHA256(raw)}
	if _, e := validateCheckpoint(data, "auth", backendTestID, backendOtherID); e != nil {
		t.Fatal(e)
	}
	data["checkpointSHA256"] = strings.Repeat("a", 64)
	if _, e := validateCheckpoint(data, "auth", backendTestID, backendOtherID); e == nil {
		t.Fatal("accepted unproved checkpoint")
	}
}

func TestBackendClockBarrierStopsBeforeMutationWhenReadMissing(t *testing.T) {
	c := backendClient{run: backendRunDescriptor{Project: "umir-backend-0123456789abcdef01234567"}, execute: func(context.Context, []byte, ...string) ([]byte, error) { return nil, errors.New("unavailable") }}
	if e := c.advance(context.Background(), "2026-10-13T09:01:00+03:00"); e == nil {
		t.Fatal("clock absence became advancement")
	}
}
