package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestE5MechanicsAuthUserPreservation(t *testing.T) {
	for _, field := range []string{"unchanged", "absent-email", "userID", "credentialVersion", "login", "displayName", "email", "email-added", "email-removed"} {
		t.Run(field, func(t *testing.T) {
			var changed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var value any
				if r.URL.Path == "/api/v2/me" {
					user := map[string]any{"userID": backendTestID, "login": "original", "displayName": "Original User", "email": "original@umir.invalid", "credentialVersion": "1"}
					if field == "absent-email" || field == "email-added" {
						delete(user, "email")
					}
					csrf := "mechanics-before"
					if changed.Load() {
						csrf = "mechanics-after"
						switch field {
						case "userID":
							user[field] = backendOtherID
						case "credentialVersion":
							user[field] = "2"
						case "email", "email-added":
							user["email"] = "changed@umir.invalid"
						case "email-removed":
							delete(user, "email")
						case "login", "displayName":
							user[field] = "Changed Value"
						}
					}
					value = map[string]any{"user": user, "session": map[string]any{"expiresAt": "2026-10-13T10:30:00+03:00", "idleExpiresAt": "2026-10-13T10:30:00+03:00", "absoluteExpiresAt": "2026-10-13T18:00:00+03:00", "rememberMe": false, "csrfToken": csrf}}
				} else {
					value = map[string]any{"orgID": backendTestID, "name": "Unchanged Organization", "settings": map[string]any{"timeZone": "Europe/Moscow", "settingsVersion": "1"}, "lifecycle": "active", "lifecycleVersion": "1"}
				}
				if err := json.NewEncoder(w).Encode(value); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			b := newBackendSession(server.URL, server.URL)
			b.probe = func(context.Context) (func(), error) { return func() {}, nil }
			input := backendScenarioInput{Session: b, Scopes: []backendScope{{backendTestID, backendOtherID}}}
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			before, err := e5AuthRead(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			changed.Store(true)
			after, err := e5AuthRead(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			wantEqual := field == "unchanged" || field == "absent-email"
			if reflect.DeepEqual(before, after) != wantEqual {
				t.Fatalf("User.%s preservation comparison differs", field)
			}
		})
	}
}

func TestE5MechanicsEmptyReadsBindObservedRestartStages(t *testing.T) {
	warehouseAfter := "33333333-3333-4333-8333-333333333333"
	servingAfter := "44444444-4444-4444-8444-444444444444"
	for _, stage := range []string{"warehouse-only", "both-restarted"} {
		t.Run(stage, func(t *testing.T) {
			serving := backendOtherID
			if stage == "both-restarted" {
				serving = servingAfter
			}
			capture := e5MechanicsEmptyCapture(t)
			for op, raw := range capture.Replies {
				var reply map[string]any
				if err := json.Unmarshal(raw, &reply); err != nil {
					t.Fatal(err)
				}
				ctx := privateValue[map[string]any](t, reply["context"])
				ctx["runtimeEpoch"] = warehouseAfter
				if op == "menu" {
					ctx["runtimeEpoch"], ctx["warehouseEpoch"] = serving, warehouseAfter
				}
				updated, err := json.Marshal(reply)
				if err != nil {
					t.Fatal(err)
				}
				capture.Replies[op] = updated
			}
			if err := e5BusinessEmpty(capture, warehouseAfter, serving); err != nil {
				t.Fatal(err)
			}
			for _, epochs := range [][2]string{{backendTestID, serving}, {warehouseAfter, backendTestID}, {backendTestID, backendOtherID}, {"", serving}, {warehouseAfter, "invalid"}} {
				if err := e5BusinessEmpty(capture, epochs[0], epochs[1]); err == nil {
					t.Fatal("empty reads accepted for different or unavailable observed owner epochs")
				}
			}
		})
	}
	if err := e5BusinessEmpty(e5MechanicsEmptyCapture(t), warehouseAfter, servingAfter); err == nil {
		t.Fatal("coherently stale replies accepted for restarted owners")
	}
}
