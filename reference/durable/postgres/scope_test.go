package postgres

import (
	"strings"
	"testing"
)

func TestScopeValidate(t *testing.T) {
	long := strings.Repeat("x", maxScopeValueLen+1)
	cases := []struct {
		name  string
		scope Scope
		ok    bool
	}{
		{"tenant with restaurant", Scope{Actor: ActorTenant, OrgID: "o1", RestaurantID: "r1"}, true},
		{"tenant with user", Scope{Actor: ActorTenant, OrgID: "o1", RestaurantID: "r1", UserID: "u1"}, true},
		{"organization level tenant", Scope{Actor: ActorTenant, OrgID: "o1"}, true},
		{"tenant without organization", Scope{Actor: ActorTenant, RestaurantID: "r1"}, false},
		{"empty tenant", Scope{Actor: ActorTenant}, false},
		{"user", Scope{Actor: ActorUser, UserID: "u1"}, true},
		{"user without id", Scope{Actor: ActorUser}, false},
		{"anonymous", Scope{Actor: ActorAnonymous}, true},
		{"anonymous with ids", Scope{Actor: ActorAnonymous, OrgID: "o1"}, false},
		{"system is not scoped", Scope{Actor: ActorSystem}, false},
		{"empty actor", Scope{}, false},
		{"unknown actor", Scope{Actor: "admin", OrgID: "o1"}, false},
		{"actor casing", Scope{Actor: "Tenant", OrgID: "o1", RestaurantID: "r1"}, false},
		{"too long value", Scope{Actor: ActorTenant, OrgID: long, RestaurantID: "r1"}, false},
		{"NUL byte", Scope{Actor: ActorTenant, OrgID: "o\x001", RestaurantID: "r1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.scope.Validate()
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestScopeErrorsDoNotEchoValues(t *testing.T) {
	err := Scope{Actor: ActorAnonymous, OrgID: "secret-org-value"}.Validate()
	if err == nil || strings.Contains(err.Error(), "secret-org-value") {
		t.Fatalf("error must not echo values: %v", err)
	}
}

func TestSystemReasonValidation(t *testing.T) {
	for _, bad := range []string{"", "   ", "line\nbreak", strings.Repeat("x", maxReasonLen+1), "nul\x00"} {
		if err := validateReason(bad); err == nil {
			t.Fatalf("reason %q must be rejected", bad)
		}
	}
	if err := validateReason("outbox relay"); err != nil {
		t.Fatalf("valid reason rejected: %v", err)
	}
}

func TestGUCNames(t *testing.T) {
	if GUCActor != "umir.actor" || GUCOrgID != "umir.org_id" ||
		GUCRestaurantID != "umir.restaurant_id" || GUCUserID != "umir.user_id" {
		t.Fatal("GUC names are part of the contract (C5)")
	}
}
