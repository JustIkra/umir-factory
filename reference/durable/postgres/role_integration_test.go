//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/JustIkra/umir-factory/reference/durable/postgres"
)

func sqlState(t *testing.T, err error) string {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("not a PostgreSQL error: %v", err)
	}
	return pgErr.Code
}

func TestRuntimeRoleCannotRunDDLOrBypassRLS(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)

	if err := postgres.AssertRuntimeRole(t.Context(), rt); err != nil {
		t.Fatalf("AssertRuntimeRole(runtime role): %v", err)
	}

	ddl := []string{
		"CREATE TABLE intruder (id int)",
		"CREATE SCHEMA intruder",
		"DROP TABLE dishes",
		"ALTER TABLE dishes DISABLE ROW LEVEL SECURITY",
		"ALTER TABLE dishes NO FORCE ROW LEVEL SECURITY",
		"DROP POLICY dishes_scope_restaurant_id ON dishes",
		"ALTER TABLE dishes ADD COLUMN extra int",
		"TRUNCATE storage_meta, schema_migrations",
		"SET ROLE " + quote(f.env.OwnerRole),
	}
	for _, stmt := range ddl {
		err := rt.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		if err == nil {
			t.Errorf("runtime role ran %q", stmt)
			continue
		}
		if code := sqlState(t, err); code != "42501" {
			t.Errorf("%q failed with %s, want 42501 (insufficient_privilege): %v", stmt, code, err)
		}
	}

	// Seed one row per restaurant as the system actor, then try the usual
	// ways around a policy.
	seedDishes(t, rt)
	err := rt.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL row_security = off"); err != nil {
			return err
		}
		var n int
		return tx.QueryRow(ctx, "SELECT count(*) FROM dishes").Scan(&n)
	})
	if err == nil {
		t.Fatal("row_security = off must make the query fail for a role that does not bypass RLS")
	}
	if code := sqlState(t, err); code != "42501" {
		t.Fatalf("row_security = off failed with %s, want 42501: %v", code, err)
	}
	if got := count(t, t.Context(), rt, "SELECT count(*) FROM dishes"); got != 0 {
		t.Fatalf("a query without scope returned %d rows", got)
	}
}

func TestAssertRuntimeRoleRejectsPrivilegedRoles(t *testing.T) {
	f := newFixture(t)
	cases := map[string]struct {
		url    string
		reason string
	}{
		"owner of the tables":    {f.env.OwnerURL, "owner"},
		"superuser":              {f.env.AdminURL, "superuser"},
		"role that bypasses RLS": {f.env.loginRole(t, "bypass", "NOSUPERUSER BYPASSRLS"), "bypass"},
		"role with CREATE on the schema": {
			f.env.loginRole(t, "creator", "NOSUPERUSER NOBYPASSRLS", "GRANT CREATE ON SCHEMA public TO %s"), "create",
		},
		"member of the owner role": {
			f.env.loginRole(t, "member", "NOSUPERUSER NOBYPASSRLS", "GRANT "+quote(f.env.OwnerRole)+" TO %s"), "owner",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := openDB(t, tc.url, nil)
			err := postgres.AssertRuntimeRole(t.Context(), db)
			if err == nil {
				t.Fatal("AssertRuntimeRole must reject this role")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.reason) {
				t.Fatalf("error should name the reason %q: %v", tc.reason, err)
			}
		})
	}
}
