package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBackendE12MechanicsMembershipFacts(t *testing.T) {
	scope := backendScope{"22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}
	user := "11111111-1111-4111-8111-111111111111"
	fixture := func() map[string]any {
		return map[string]any{"organizations": []any{}, "restaurants": []any{}, "memberships": []any{map[string]any{
			"membershipID": "44444444-4444-4444-8444-444444444444", "membershipVersion": "1", "userID": user, "orgID": scope.OrgID, "orgRoles": []string{}, "state": "active",
			"assignments": []any{map[string]any{"assignmentID": "55555555-5555-4555-8555-555555555555", "assignmentVersion": "1", "restaurantID": scope.RestaurantID, "roles": []string{"Waiter", "Warehouse"}}},
		}}}
	}
	for _, name := range []string{"valid", "admin-escalation", "wrong-user", "wrong-org", "revoked", "wrong-restaurant", "missing-role", "duplicate-membership", "missing-membership", "null-parent"} {
		t.Run(name, func(t *testing.T) {
			data := fixture()
			members, ok := data["memberships"].([]any)
			if !ok {
				t.Fatal("mechanics fixture")
			}
			m, ok := members[0].(map[string]any)
			if !ok {
				t.Fatal("mechanics fixture")
			}
			assignments, ok := m["assignments"].([]any)
			if !ok {
				t.Fatal("mechanics fixture")
			}
			a, ok := assignments[0].(map[string]any)
			if !ok {
				t.Fatal("mechanics fixture")
			}
			switch name {
			case "admin-escalation":
				m["orgRoles"] = []string{"Admin"}
			case "wrong-user":
				m["userID"] = "66666666-6666-4666-8666-666666666666"
			case "wrong-org":
				m["orgID"] = "66666666-6666-4666-8666-666666666666"
			case "revoked":
				m["state"] = "revoked"
			case "wrong-restaurant":
				a["restaurantID"] = "66666666-6666-4666-8666-666666666666"
			case "missing-role":
				a["roles"] = []string{"Warehouse"}
			case "duplicate-membership":
				data["memberships"] = append(members, m)
			case "missing-membership":
				data["memberships"] = []any{}
			case "null-parent":
				data["memberships"] = nil
			}
			raw, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			_, err = backendE12MembershipFact(raw, scope, user, nil, []string{"Warehouse", "Waiter"})
			if (err == nil) != (name == "valid") {
				t.Fatalf("mechanics normalizer %s: %v", name, err)
			}
		})
	}
}

func TestBackendE12MechanicsMissingFactsNeverPass(t *testing.T) {
	for stage, limit := range map[string]int{"E1": 14, "E2": 8} {
		for i := 1; i <= limit; i++ {
			id := backendCaseID(stage, i)
			err := runBackendE12(context.Background(), backendScenarioInput{}, id)
			if err == nil {
				t.Fatalf("missing real facts granted %s", id)
			}
			if id != "BE-E1-02" && id != "BE-E1-12" && !errors.Is(err, errBackendScenarioUnimplemented) {
				t.Fatalf("lost incomplete identity %s: %v", id, err)
			}
			if id == "BE-E2-03" && !strings.Contains(err.Error(), "independently READY signer-rotation") {
				t.Fatalf("profile request absent: %v", err)
			}
		}
	}
}

func TestBackendE12MechanicsTypedRefusal(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		body      string
		wantError bool
	}{
		{"typed-forbidden", 403, `{"code":"Forbidden","outcome":"not_committed","message":"denied","retryable":false}`, false},
		{"successful-response", 200, `{}`, true},
		{"wrong-outcome", 403, `{"code":"Forbidden","outcome":"unknown","message":"denied","retryable":false}`, true},
		{"unrelated-error", 403, `{"code":"TechnicalError","outcome":"not_committed","message":"failed","retryable":false}`, true},
		{"missing-owner-facts", 403, `{}`, true},
		{"unknown-key", 403, `{"code":"Forbidden","outcome":"not_committed","message":"denied","retryable":false,"secret":"mechanics-sentinel"}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			qualified := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v2/orgs/22222222-2222-4222-8222-222222222222/restaurants" || r.Header.Get("Origin") == "" {
					t.Error("mechanics request binding")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				if _, err := io.WriteString(w, test.body); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			session := newBackendSession(server.URL, server.URL)
			if session == nil {
				t.Fatal("mechanics session")
			}
			session.probe = func(context.Context) (func(), error) { qualified++; return func() {}, nil }
			err := backendE12Refuse(context.Background(), session, "createRestaurant", map[string]string{"orgID": "22222222-2222-4222-8222-222222222222"}, map[string]any{"commandID": "mechanics-only", "name": "mechanics"}, 403)
			if (err != nil) != test.wantError || qualified != 1 {
				t.Fatalf("mechanics refusal: %v; qualified=%d", err, qualified)
			}
		})
	}
}

func TestBackendE12MechanicsAdminAssignmentAuthority(t *testing.T) {
	scope := backendScope{"22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}
	user := "11111111-1111-4111-8111-111111111111"
	for _, name := range []string{"no-assignment", "observed-own-assignment", "missing-restaurant-parent", "foreign-org-parent", "duplicate-assignment"} {
		t.Run(name, func(t *testing.T) {
			assignment := map[string]any{"restaurantID": scope.RestaurantID, "assignmentID": "44444444-4444-4444-8444-444444444444", "assignmentVersion": "1", "roles": []string{"Warehouse"}}
			assignments := []any{assignment}
			restaurant := map[string]any{"restaurantID": scope.RestaurantID, "orgID": scope.OrgID, "name": "Mechanics", "lifecycle": "ready", "lifecycleVersion": "1"}
			restaurants := []any{restaurant}
			switch name {
			case "no-assignment":
				assignments = []any{}
			case "missing-restaurant-parent":
				restaurants = []any{}
			case "foreign-org-parent":
				restaurant["orgID"] = "55555555-5555-4555-8555-555555555555"
			case "duplicate-assignment":
				assignments = append(assignments, assignment)
			}
			data := map[string]any{"organizations": []any{}, "restaurants": restaurants, "memberships": []any{map[string]any{"membershipID": "66666666-6666-4666-8666-666666666666", "membershipVersion": "2", "userID": user, "orgID": scope.OrgID, "orgRoles": []string{"Admin"}, "assignments": assignments, "state": "active"}}}
			raw, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			_, err = backendE12MembershipFact(raw, scope, user, []string{"Admin"}, nil)
			wantError := name != "no-assignment" && name != "observed-own-assignment"
			if (err != nil) != wantError {
				t.Fatalf("mechanics parent authority %s: %v", name, err)
			}
		})
	}
}
