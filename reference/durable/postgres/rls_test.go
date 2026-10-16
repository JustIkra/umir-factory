package postgres

import (
	"strings"
	"testing"
)

func TestRLSPolicySQL(t *testing.T) {
	got, err := RLSPolicySQL("dishes", "restaurant_id")
	if err != nil {
		t.Fatalf("RLSPolicySQL: %v", err)
	}
	for _, want := range []string{
		`ALTER TABLE "dishes" ENABLE ROW LEVEL SECURITY;`,
		`ALTER TABLE "dishes" FORCE ROW LEVEL SECURITY;`,
		`CREATE POLICY "dishes_scope_restaurant_id" ON "dishes"`,
		`USING (`,
		`WITH CHECK (`,
		`current_setting('umir.restaurant_id', true)`,
		`current_setting('umir.actor', true)`,
		`'system'`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("policy SQL lacks %q:\n%s", want, got)
		}
	}
}

func TestRLSPolicySQLMapsColumnsToGUCs(t *testing.T) {
	cases := map[string]string{
		"restaurant_id":   "umir.restaurant_id",
		"org_id":          "umir.org_id",
		"organization_id": "umir.org_id",
		"user_id":         "umir.user_id",
	}
	for column, guc := range cases {
		got, err := RLSPolicySQL("t", column)
		if err != nil {
			t.Fatalf("column %s: %v", column, err)
		}
		if !strings.Contains(got, "current_setting('"+guc+"', true)") {
			t.Errorf("column %s does not read %s:\n%s", column, guc, got)
		}
	}
}

func TestRLSPolicySQLAcceptsSchemaQualifiedTable(t *testing.T) {
	got, err := RLSPolicySQL("app.dishes", "org_id")
	if err != nil {
		t.Fatalf("RLSPolicySQL: %v", err)
	}
	if !strings.Contains(got, `"app"."dishes"`) {
		t.Fatalf("schema-qualified table is not quoted as an identifier:\n%s", got)
	}
}

func TestRLSPolicySQLRejectsUnsafeInput(t *testing.T) {
	bad := [][2]string{
		{"", "org_id"},
		{"dishes; DROP TABLE x", "org_id"},
		{`di"shes`, "org_id"},
		{"Dishes", "org_id"},
		{"a.b.c", "org_id"},
		{"dishes", "name"},
		{"dishes", ""},
		{"dishes", "org_id; --"},
		{strings.Repeat("a", 64), "org_id"},
	}
	for _, c := range bad {
		if sql, err := RLSPolicySQL(c[0], c[1]); err == nil {
			t.Errorf("RLSPolicySQL(%q, %q) must fail, got %q", c[0], c[1], sql)
		}
	}
}

func TestScopeExpressions(t *testing.T) {
	for _, expr := range []string{ScopeActorSQL, ScopeOrgIDSQL, ScopeRestaurantIDSQL, ScopeUserIDSQL} {
		if !strings.HasPrefix(expr, "NULLIF(current_setting('umir.") || !strings.HasSuffix(expr, ", true), '')") {
			t.Errorf("expression must map an unset or empty setting to NULL: %s", expr)
		}
	}
}

func TestRLSPolicySQLCastsTheSettingForUUIDColumns(t *testing.T) {
	text, err := RLSPolicySQL("t", "restaurant_id")
	if err != nil || strings.Contains(text, "::uuid") {
		t.Fatalf("the default policy must not cast: %v\n%s", err, text)
	}
	got, err := RLSPolicySQL("t", "restaurant_id", RLSUUIDColumn())
	if err != nil {
		t.Fatalf("RLSPolicySQL: %v", err)
	}
	want := `"restaurant_id" = NULLIF(current_setting('umir.restaurant_id', true), '')::uuid`
	if !strings.Contains(got, want) {
		t.Fatalf("policy lacks %q:\n%s", want, got)
	}
	if strings.Count(got, "::uuid") != 2 { // USING and WITH CHECK
		t.Fatalf("expected the cast in USING and WITH CHECK:\n%s", got)
	}
}
