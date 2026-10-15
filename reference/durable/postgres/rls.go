package postgres

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// SQL expressions that read the context in policies. An unset setting and an
// empty one (what a pooled connection reports after a transaction that set it)
// both become NULL, so a missing context matches no row.
const (
	ScopeActorSQL        = `NULLIF(current_setting('umir.actor', true), '')`
	ScopeOrgIDSQL        = `NULLIF(current_setting('umir.org_id', true), '')`
	ScopeRestaurantIDSQL = `NULLIF(current_setting('umir.restaurant_id', true), '')`
	ScopeUserIDSQL       = `NULLIF(current_setting('umir.user_id', true), '')`
)

var identPart = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// scopeColumns maps a column name to the expression that scopes it.
var scopeColumns = map[string]string{
	"org_id":          ScopeOrgIDSQL,
	"organization_id": ScopeOrgIDSQL,
	"restaurant_id":   ScopeRestaurantIDSQL,
	"user_id":         ScopeUserIDSQL,
}

// RLSPolicySQL returns the statements that scope table by column for a
// migration: row level security is enabled and forced (the owner is bound too),
// and one policy lets a row through when the column equals the matching setting
// or the actor is system. The policy covers reads and writes (USING and WITH
// CHECK), so a transaction can neither see nor plant a row of another scope.
//
// column is restaurant_id, org_id (or organization_id) or user_id. Several
// policies on one table combine with OR; a table with two scope columns takes a
// hand-written policy if it needs AND.
func RLSPolicySQL(table, column string) (string, error) {
	expr, ok := scopeColumns[column]
	if !ok {
		return "", errors.New("unsupported scope column: use restaurant_id, org_id, organization_id or user_id")
	}
	parts := strings.Split(table, ".")
	if len(parts) > 2 {
		return "", errors.New("invalid table name")
	}
	for _, p := range parts {
		if !identPart.MatchString(p) {
			return "", errors.New("invalid table name")
		}
	}
	policy := parts[len(parts)-1] + "_scope_" + column
	if len(policy) > 63 {
		return "", errors.New("table name is too long for a policy name")
	}
	qualified := pgx.Identifier(parts).Sanitize()
	check := fmt.Sprintf(`%s = 'system' OR %s = %s`, ScopeActorSQL, pgx.Identifier{column}.Sanitize(), expr)
	return fmt.Sprintf(`ALTER TABLE %[1]s ENABLE ROW LEVEL SECURITY;
ALTER TABLE %[1]s FORCE ROW LEVEL SECURITY;
CREATE POLICY %[2]s ON %[1]s
    USING (%[3]s)
    WITH CHECK (%[3]s);
`, qualified, pgx.Identifier{policy}.Sanitize(), check), nil
}
