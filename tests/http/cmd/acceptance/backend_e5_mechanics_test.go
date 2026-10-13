package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

func TestE5MechanicsRejectsForeignCase(t *testing.T) {
	for _, id := range []string{"BE-E1-02", "BE-E5-00", "BE-E5-16", "BE-E5-1", ""} {
		if err := runBackendE5(context.Background(), backendScenarioInput{}, id); !errors.Is(err, faultproxy.ErrProtocol) {
			t.Fatalf("foreign ID %q did not fail protocol validation: %v", id, err)
		}
	}
}

func e5MechanicsEmptyCapture(t *testing.T) e5BusinessCapture {
	t.Helper()
	scope := backendScope{backendTestID, backendOtherID}
	capture := e5BusinessCapture{Scope: scope, Replies: map[string]json.RawMessage{}}
	for _, op := range []string{"products", "dishCatalog", "inventory", "history", "pendingWriteoffs", "menu"} {
		contextValue := map[string]any{"orgID": scope.OrgID, "tenantID": scope.RestaurantID, "runtimeEpoch": backendTestID, "stateVersion": "0"}
		if op == "menu" {
			contextValue["runtimeEpoch"] = backendOtherID
			contextValue["warehouseEpoch"] = backendTestID
		}
		key := "items"
		if op == "history" {
			key = "events"
		}
		if op == "menu" {
			key = "dishes"
		}
		value := map[string]any{"context": contextValue, "result": map[string]any{key: []any{}}}
		if op == "pendingWriteoffs" {
			delete(value, "result")
			value["items"] = []any{}
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		capture.Replies[op] = raw
	}
	return capture
}

func TestE5MechanicsEmptyObservationRejectsMissingOrRetainedFacts(t *testing.T) {
	if err := e5BusinessEmpty(e5MechanicsEmptyCapture(t), backendTestID, backendOtherID); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"products", "dishCatalog", "inventory", "history", "pendingWriteoffs", "menu"} {
		for _, mutation := range []string{"missing", "null", "nonempty", "wrong-scope", "nonzero-version", "epoch"} {
			t.Run(op+"/"+mutation, func(t *testing.T) {
				capture := e5MechanicsEmptyCapture(t)
				if mutation == "missing" {
					delete(capture.Replies, op)
				} else {
					var value map[string]any
					if err := json.Unmarshal(capture.Replies[op], &value); err != nil {
						t.Fatal(err)
					}
					contextValue := privateValue[map[string]any](t, value["context"])
					switch mutation {
					case "wrong-scope":
						contextValue["tenantID"] = backendTestID
					case "nonzero-version":
						contextValue["stateVersion"] = "1"
					case "epoch":
						if op == "menu" {
							contextValue["warehouseEpoch"] = backendOtherID
						} else {
							contextValue["runtimeEpoch"] = backendOtherID
						}
					default:
						var rows any = []any{map[string]any{"retained": true}}
						if mutation == "null" {
							rows = nil
						}
						key := "items"
						if op == "history" {
							key = "events"
						}
						if op == "menu" {
							key = "dishes"
						}
						if op == "pendingWriteoffs" {
							value["items"] = rows
						} else {
							privateValue[map[string]any](t, value["result"])[key] = rows
						}
					}
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					capture.Replies[op] = raw
				}
				if err := e5BusinessEmpty(capture, backendTestID, backendOtherID); err == nil {
					t.Fatal("missing, retained or foreign fact accepted as empty")
				}
			})
		}
	}
}

func TestE5MechanicsFeedbackOrchestration(t *testing.T) {
	for _, mutation := range []string{"none", "no-drop", "not-committed", "wrong-scope", "changed-receipt", "missing-row", "changed-fingerprint", "changed-job", "not-recovered", "conflict-mutates", "conflict-success"} {
		t.Run(mutation, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "feedback")
			rig.faultOutcome = "committed"
			if mutation == "not-committed" {
				rig.faultOutcome = "unknown"
			}
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			if _, err := rig.client.control(ctx, "feedback", "fixture.ready", map[string]any{}); err != nil {
				t.Fatal(err)
			}
			scopes := []backendScope{{backendTestID, backendOtherID}, {"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"}}
			var mu sync.Mutex
			rows := map[string]feedbackRecord{}
			first, conflicts := true, 0
			var serverErr error
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				respond := func(status int, value any) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					if err := json.NewEncoder(w).Encode(value); err != nil {
						serverErr = errors.Join(serverErr, err)
					}
				}
				if r.URL.Path == "/api/v2/me" {
					respond(200, map[string]any{"user": map[string]any{"userID": backendTestID, "login": "mechanics", "displayName": "Mechanics User", "credentialVersion": "1"}, "session": map[string]any{"expiresAt": "2026-10-13T10:30:00+03:00", "idleExpiresAt": "2026-10-13T10:30:00+03:00", "absoluteExpiresAt": "2026-10-13T18:00:00+03:00", "rememberMe": false, "csrfToken": "mechanics"}})
					return
				}
				var payload struct{ SubmissionID, Message string }
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					serverErr = errors.Join(serverErr, err)
					respond(400, map[string]any{})
					return
				}
				namespace := feedbackNamespace{Kind: "anonymous"}
				for _, scope := range scopes {
					if strings.Contains(r.URL.Path, scope.RestaurantID) {
						namespace = feedbackNamespace{"app", backendTestID, scope.OrgID, scope.RestaurantID}
					}
				}
				key, err := feedbackPairKey(feedbackSubmission{namespace, payload.SubmissionID})
				if err != nil {
					serverErr = errors.Join(serverErr, err)
					return
				}
				if strings.HasPrefix(payload.Message, "Changed") {
					conflicts++
					if mutation == "conflict-mutates" {
						row := rows[key]
						row.Fingerprint = strings.Repeat("f", 64)
						rows[key] = row
					}
					if mutation == "conflict-success" {
						respond(201, map[string]any{"receipt": map[string]any{"submissionID": payload.SubmissionID, "receivedAt": rig.clock.Instant}, "scope": namespace, "state": "duplicate"})
						return
					}
					respond(409, map[string]any{"code": "CommandConflict", "outcome": "not_committed", "message": "mechanics conflict", "retryable": false})
					return
				}
				state := "duplicate"
				if _, exists := rows[key]; !exists {
					state = "accepted"
					i := len(rows)*2 + 1
					rows[key] = feedbackRecord{SubmissionID: payload.SubmissionID, Namespace: namespace, CommandID: payload.SubmissionID, Fingerprint: strings.Repeat("a", 64), ReceivedAt: rig.clock.Instant, Jobs: []feedbackJob{{fmt.Sprintf("%08x-1111-4111-8111-111111111111", i), "smtp", "pending", 0, rig.clock.Instant}, {fmt.Sprintf("%08x-1111-4111-8111-111111111111", i+1), "telegram", "pending", 0, rig.clock.Instant}}}
				}
				if first && mutation != "no-drop" {
					first = false
					respond(502, map[string]any{"code": "UpstreamUnavailable", "outcome": "unknown", "message": "mechanics lost reply", "retryable": false})
					return
				}
				first = false
				returnedScope, receivedAt := namespace, rows[key].ReceivedAt
				if mutation == "wrong-scope" {
					returnedScope.TenantID = backendTestID
				}
				if mutation == "changed-receipt" && state == "duplicate" {
					receivedAt = "2026-10-13T10:01:00+03:00"
				}
				respond(201, map[string]any{"receipt": map[string]any{"submissionID": payload.SubmissionID, "receivedAt": receivedAt}, "scope": returnedScope, "state": state})
			}))
			defer server.Close()
			previous := rig.client.execute
			rig.client.execute = func(ctx context.Context, raw []byte, args ...string) ([]byte, error) {
				var request fixtureRequest
				if len(raw) > 0 && json.Unmarshal(raw, &request) == nil && request.Operation == "feedback.observe" {
					mu.Lock()
					keys := []string{}
					for key := range rows {
						keys = append(keys, key)
					}
					slices.Sort(keys)
					records := []feedbackRecord{}
					for _, key := range keys {
						row := rows[key]
						row.Jobs = slices.Clone(row.Jobs)
						records = append(records, row)
					}
					if len(rig.client.restarts) > 0 && len(records) > 0 {
						switch mutation {
						case "missing-row":
							records = records[:len(records)-1]
						case "changed-fingerprint":
							records[0].Fingerprint = strings.Repeat("b", 64)
						case "changed-job":
							records[0].Jobs[0].JobID = "55555555-5555-4555-8555-555555555555"
						}
					}
					canonical, err := canonicalFeedbackJSON(records)
					if err != nil {
						mu.Unlock()
						return nil, err
					}
					rig.projection = map[string]any{"snapshotID": backendOtherID, "snapshotSHA256": faultproxy.SHA256(canonical), "complete": len(records) == 3, "recovered": len(rig.client.restarts) > 0 && mutation != "not-recovered", "records": records}
					mu.Unlock()
				}
				return previous(ctx, raw, args...)
			}
			b := newBackendSession(server.URL, server.URL)
			if b == nil {
				t.Fatal("mechanics session")
			}
			b.probe = func(context.Context) (func(), error) { return func() {}, nil }
			err := runBackendE5(ctx, backendScenarioInput{b, rig.client, scopes}, "BE-E5-13")
			mu.Lock()
			defer mu.Unlock()
			if serverErr != nil {
				t.Fatal(serverErr)
			}
			if mutation == "none" {
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != 3 || conflicts != 3 || len(rig.client.restarts) != 1 {
					t.Fatal("mechanics orchestration did not reach every branch")
				}
				info, err := os.Stat(filepath.Join(rig.client.run.EvidenceDirectory, "BE-E5-13-facts.json"))
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatal("mechanics facts not retained privately")
				}
			} else if err == nil {
				t.Fatal("mutated mechanics facts permitted case success")
			}
		})
	}
}

func TestE5MechanicsBudgetAndInvalidScopeFailBeforeCalls(t *testing.T) {
	for _, id := range []string{"BE-E5-08", "BE-E5-13"} {
		b := newBackendSession("http://127.0.0.1:49152", "http://127.0.0.1:49152")
		input := backendScenarioInput{b, &backendClient{}, []backendScope{{backendTestID, backendOtherID}, {backendOtherID, backendTestID}}}
		if err := runBackendE5(context.Background(), input, id); err == nil {
			t.Fatal("unbounded scenario accepted")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		input.Scopes[0].RestaurantID = "invalid"
		err := runBackendE5(ctx, input, id)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "setup") {
			t.Fatalf("invalid scope reached API: %v", err)
		}
	}
}

func TestE5MechanicsCurrentEpochCannotHideReconstructedAttempt(t *testing.T) {
	for _, mutation := range []string{"none", "reconstructed-order", "reconstructed-writeoff", "pending-writeoff", "wrong-epoch"} {
		t.Run(mutation, func(t *testing.T) {
			var requestErr error
			var mu sync.Mutex
			calls := 0
			seed := e5ResetSeed{Scope: backendScope{backendTestID, backendOtherID}, ProposalID: "original-proposal", OrderCommand: "original-order", WriteoffCommand: "original-writeoff", BatchID: "original-batch"}
			warehouse, serving := "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				status := 404
				value := map[string]any{"code": "ProposalNotFound", "outcome": "unknown", "message": "mechanics missing proposal", "retryable": false}
				if strings.Contains(r.URL.Path, "/serving/") {
					if r.URL.Query().Get("runtimeEpoch") != serving || r.URL.Query().Get("commandID") != seed.OrderCommand || r.URL.Query().Get("proposalID") != seed.ProposalID {
						requestErr = errors.New("original identities not probed in current epoch")
					}
					if mutation == "reconstructed-order" {
						status = 409
						value["code"] = "CommandConflict"
						value["outcome"] = "not_committed"
					}
				} else {
					if r.Header.Get("X-Umir-Runtime-Epoch") != warehouse || r.URL.Query().Get("commandID") != seed.WriteoffCommand || r.URL.Query().Get("batchID") != seed.BatchID {
						requestErr = errors.New("writeoff identities not probed in current epoch")
					}
					status = 200
					outcome := map[string]any{"state": "unseen", "outcome": "unknown"}
					if mutation == "pending-writeoff" {
						outcome["state"] = "pending"
					}
					if mutation == "reconstructed-writeoff" {
						status = 409
						value["code"] = "CommandConflict"
						value["outcome"] = "not_committed"
					} else {
						epoch := warehouse
						if mutation == "wrong-epoch" {
							epoch = backendTestID
						}
						value = map[string]any{"context": map[string]any{"orgID": seed.Scope.OrgID, "tenantID": seed.Scope.RestaurantID, "runtimeEpoch": epoch, "settings": map[string]any{"timeZone": "Europe/Moscow", "settingsVersion": "1"}, "evaluatedAt": "2026-10-13T10:00:00+03:00", "stateVersion": "0"}, "commandID": seed.WriteoffCommand, "batchID": seed.BatchID, "commandFingerprint": strings.Repeat("a", 64), "observation": outcome}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if err := json.NewEncoder(w).Encode(value); err != nil {
					requestErr = errors.Join(requestErr, err)
				}
			}))
			defer server.Close()
			b := newBackendSession(server.URL, server.URL)
			if b == nil {
				t.Fatal("mechanics session unavailable")
			}
			b.probe = func(context.Context) (func(), error) { return func() {}, nil }
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			_, err := e5ProbeReconstruction(ctx, backendScenarioInput{Session: b}, []e5ResetSeed{seed}, warehouse, serving)
			mu.Lock()
			defer mu.Unlock()
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			if mutation == "none" {
				if err != nil || calls != 2 {
					t.Fatalf("current-epoch observations failed: %v calls=%d", err, calls)
				}
			} else if err == nil {
				t.Fatal("reconstructed or pending original attempt hidden by old-epoch refusal")
			}
		})
	}
}

func TestE5MechanicsSessionCookieComparisonKeepsTokensPrivate(t *testing.T) {
	b := newBackendSession("http://127.0.0.1:49152", "http://127.0.0.1:49152")
	if b == nil {
		t.Fatal("mechanics session unavailable")
	}
	if _, err := e5SessionCookie(b); err == nil {
		t.Fatal("missing cookie accepted")
	}
	u, err := url.Parse(b.base)
	if err != nil {
		t.Fatal(err)
	}
	b.client.Jar.SetCookies(u, []*http.Cookie{{Name: "umir_operator_session", Value: "mechanics-private-token", Path: "/"}})
	before, err := e5SessionCookie(b)
	if err != nil {
		t.Fatal("issued cookie unavailable")
	}
	b.client.Jar.SetCookies(u, []*http.Cookie{{Name: "umir_operator_session", Value: "mechanics-replaced-token", Path: "/"}})
	after, err := e5SessionCookie(b)
	if err != nil || before == after {
		t.Fatal("replacement cookie not distinguished")
	}
	for _, session := range []*backendSession{nil, {}, {client: &backendPublicClient{}}} {
		if _, err := e5SessionCookie(session); err == nil {
			t.Fatal("incomplete cookie binding accepted")
		}
	}
}

func TestE5MechanicsMissingSetupCannotPass(t *testing.T) {
	for _, id := range []string{"BE-E5-08", "BE-E5-13"} {
		ctx, cancel := backendScenarioContext(context.Background())
		err := runBackendE5(ctx, backendScenarioInput{}, id)
		cancel()
		if !errors.Is(err, errBackendScenarioUnimplemented) || !strings.Contains(err.Error(), "setup") {
			t.Fatalf("%s missing setup must fail before application calls: %v", id, err)
		}
	}
}

func TestE5MechanicsIncompleteNamesOwnerRequest(t *testing.T) {
	for i := 1; i <= 15; i++ {
		if i == 8 || i == 13 {
			continue
		}
		id := backendCaseID("E5", i)
		err := runBackendE5(context.Background(), backendScenarioInput{}, id)
		if !errors.Is(err, errBackendScenarioUnimplemented) || !strings.Contains(err.Error(), id) || !strings.Contains(err.Error(), "owner") {
			t.Fatalf("%s must retain an explicit owner request: %v", id, err)
		}
	}
}
