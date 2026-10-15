//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/JustIkra/umir-factory/reference/durable/postgres"
)

// Fixture: organization o1 has restaurants r1 and r2, organization o2 has r3.
func seedDishes(t *testing.T, db *postgres.DB) {
	t.Helper()
	err := db.InSystemTx(t.Context(), "test seed", pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		for _, stmt := range []string{
			"INSERT INTO dishes (org_id, restaurant_id, name) VALUES ('o1', 'r1', 'soup'), ('o1', 'r2', 'salad'), ('o2', 'r3', 'steak')",
			"INSERT INTO settings (org_id, theme) VALUES ('o1', 'light'), ('o2', 'dark')",
			"INSERT INTO profiles (user_id, nick) VALUES ('u1', 'ann'), ('u2', 'bob')",
		} {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func column(ctx context.Context, tx pgx.Tx, query string) ([]string, error) {
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type visible struct{ dishes, settings, profiles []string }

func readVisible(ctx context.Context, tx pgx.Tx) (visible, error) {
	var v visible
	var err error
	if v.dishes, err = column(ctx, tx, "SELECT name FROM dishes ORDER BY name"); err != nil {
		return v, err
	}
	if v.settings, err = column(ctx, tx, "SELECT theme FROM settings ORDER BY theme"); err != nil {
		return v, err
	}
	v.profiles, err = column(ctx, tx, "SELECT nick FROM profiles ORDER BY nick")
	return v, err
}

func TestScopeIsolationWithRealRole(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	seedDishes(t, rt)

	tenant := func(org, restaurant string) postgres.Scope {
		return postgres.Scope{Actor: postgres.ActorTenant, OrgID: org, RestaurantID: restaurant}
	}
	cases := []struct {
		name  string
		scope postgres.Scope
		want  visible
	}{
		{"restaurant r1 of o1", tenant("o1", "r1"), visible{[]string{"soup"}, []string{"light"}, nil}},
		{"restaurant r2 of o1", tenant("o1", "r2"), visible{[]string{"salad"}, []string{"light"}, nil}},
		{"restaurant r3 of o2", tenant("o2", "r3"), visible{[]string{"steak"}, []string{"dark"}, nil}},
		{"organization level o2", postgres.Scope{Actor: postgres.ActorTenant, OrgID: "o2"}, visible{nil, []string{"dark"}, nil}},
		{"unknown restaurant", tenant("o1", "nope"), visible{nil, []string{"light"}, nil}},
		{"user u1", postgres.Scope{Actor: postgres.ActorUser, UserID: "u1"}, visible{nil, nil, []string{"ann"}}},
		{"anonymous", postgres.Scope{Actor: postgres.ActorAnonymous}, visible{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got visible
			err := rt.InScopedTx(t.Context(), tc.scope, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				got, err = readVisible(ctx, tx)
				return err
			})
			if err != nil {
				t.Fatalf("InScopedTx: %v", err)
			}
			if !slices.Equal(got.dishes, tc.want.dishes) || !slices.Equal(got.settings, tc.want.settings) || !slices.Equal(got.profiles, tc.want.profiles) {
				t.Fatalf("visible = %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("system actor sees everything", func(t *testing.T) {
		var got visible
		err := rt.InSystemTx(t.Context(), "cross-tenant job", pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			got, err = readVisible(ctx, tx)
			return err
		})
		if err != nil {
			t.Fatalf("InSystemTx: %v", err)
		}
		if len(got.dishes) != 3 || len(got.settings) != 2 || len(got.profiles) != 2 {
			t.Fatalf("system actor sees %+v", got)
		}
	})

	t.Run("no context means no rows", func(t *testing.T) {
		var got visible
		err := rt.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			got, err = readVisible(ctx, tx)
			return err
		})
		if err != nil {
			t.Fatalf("InTx: %v", err)
		}
		if len(got.dishes)+len(got.settings)+len(got.profiles) != 0 {
			t.Fatalf("a transaction without scope sees %+v", got)
		}
		// Straight from the pool, outside any helper.
		var n int
		if err := rt.Pool().QueryRow(t.Context(), "SELECT (SELECT count(*) FROM dishes) + (SELECT count(*) FROM settings) + (SELECT count(*) FROM profiles)").Scan(&n); err != nil || n != 0 {
			t.Fatalf("pool query without scope: n=%d err=%v", n, err)
		}
	})

	t.Run("FORCE ROW LEVEL SECURITY binds the owner too", func(t *testing.T) {
		var forced int
		if err := f.owner.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT count(*) FROM pg_class WHERE relname IN ('dishes', 'settings', 'profiles') AND relrowsecurity AND relforcerowsecurity").Scan(&forced)
		}); err != nil || forced != 3 {
			t.Fatalf("tables with FORCE ROW LEVEL SECURITY = %d, err=%v", forced, err)
		}
		if got := count(t, t.Context(), f.owner, "SELECT count(*) FROM dishes"); got != 0 {
			t.Fatalf("the table owner without scope sees %d rows", got)
		}
	})

	t.Run("writes outside the scope are refused", func(t *testing.T) {
		r1 := tenant("o1", "r1")
		err := rt.InScopedTx(t.Context(), r1, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO dishes (org_id, restaurant_id, name) VALUES ('o1', 'r2', 'planted')")
			return err
		})
		if err == nil || sqlState(t, err) != "42501" {
			t.Fatalf("insert into another restaurant = %v, want 42501", err)
		}
		err = rt.InScopedTx(t.Context(), r1, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, "UPDATE dishes SET name = 'hijacked' WHERE restaurant_id = 'r2'")
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 0 {
				return errors.New("updated a row of another restaurant")
			}
			tag, err = tx.Exec(ctx, "DELETE FROM dishes WHERE restaurant_id = 'r3'")
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 0 {
				return errors.New("deleted a row of another restaurant")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		// Moving a visible row out of the scope is refused by WITH CHECK.
		err = rt.InScopedTx(t.Context(), r1, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "UPDATE dishes SET restaurant_id = 'r2' WHERE restaurant_id = 'r1'")
			return err
		})
		if err == nil || sqlState(t, err) != "42501" {
			t.Fatalf("moving a row to another restaurant = %v, want 42501", err)
		}
		if got := count(t, t.Context(), f.owner, "SELECT count(*) FROM dishes"); got != 0 {
			t.Fatalf("owner without scope sees %d rows", got)
		}
		err = rt.InSystemTx(t.Context(), "verify", pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			names, err := column(ctx, tx, "SELECT name FROM dishes ORDER BY name")
			if err != nil {
				return err
			}
			if !slices.Equal(names, []string{"salad", "soup", "steak"}) {
				return errors.New("rows changed: " + joinStrings(names))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func joinStrings(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}

func TestScopedTxRejectsInvalidScopeBeforeTouchingTheDatabase(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	called := false
	for _, s := range []postgres.Scope{
		{},
		{Actor: postgres.ActorSystem},
		{Actor: postgres.ActorTenant, RestaurantID: "r1"},
		{Actor: "root", OrgID: "o1"},
	} {
		err := rt.InScopedTx(t.Context(), s, pgx.TxOptions{}, func(context.Context, pgx.Tx) error {
			called = true
			return nil
		})
		if err == nil {
			t.Errorf("scope %+v must be rejected", s)
		}
	}
	if err := rt.InSystemTx(t.Context(), "", pgx.TxOptions{}, func(context.Context, pgx.Tx) error {
		called = true
		return nil
	}); err == nil {
		t.Error("InSystemTx without a reason must be rejected")
	}
	if called {
		t.Fatal("fn ran under an invalid scope")
	}
}

// guc reads the four context settings as the transaction sees them: unset and
// empty both map to "-".
func guc(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range []string{postgres.GUCActor, postgres.GUCOrgID, postgres.GUCRestaurantID, postgres.GUCUserID} {
		var v string
		if err := tx.QueryRow(ctx, "SELECT COALESCE(NULLIF(current_setting($1, true), ''), '-')", name).Scan(&v); err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

func pid(ctx context.Context, tx pgx.Tx) (int, error) {
	var p int
	err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&p)
	return p, err
}

func emptyContext(t *testing.T, rt *postgres.DB, wantPID int) {
	t.Helper()
	err := rt.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		p, err := pid(ctx, tx)
		if err != nil {
			return err
		}
		if p != wantPID {
			return errors.New("the next transaction ran on another connection; the test does not prove reuse")
		}
		g, err := guc(ctx, tx)
		if err != nil {
			return err
		}
		for name, v := range g {
			if v != "-" {
				return errors.New(name + " leaked: " + v)
			}
		}
		v, err := readVisible(ctx, tx)
		if err != nil {
			return err
		}
		if len(v.dishes)+len(v.settings)+len(v.profiles) != 0 {
			return errors.New("rows visible without scope: " + joinStrings(v.dishes))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestScopeDoesNotLeakThroughThePool(t *testing.T) {
	f := newFixture(t)
	seed := f.runtime(t, nil)
	seedDishes(t, seed)
	rt := f.runtime(t, withMaxConns(1)) // one connection: every transaction reuses it
	r1 := postgres.Scope{Actor: postgres.ActorTenant, OrgID: "o1", RestaurantID: "r1", UserID: "u1"}

	scoped := func(fn func(context.Context, pgx.Tx) error) (int, error) {
		var p int
		err := rt.InScopedTx(t.Context(), r1, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			if p, err = pid(ctx, tx); err != nil {
				return err
			}
			v, err := readVisible(ctx, tx)
			if err != nil {
				return err
			}
			if len(v.dishes) != 1 {
				return errors.New("the scoped transaction does not see its own rows")
			}
			return fn(ctx, tx)
		})
		return p, err
	}

	t.Run("after commit", func(t *testing.T) {
		p, err := scoped(func(context.Context, pgx.Tx) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		emptyContext(t, rt, p)
	})

	t.Run("after rollback", func(t *testing.T) {
		boom := errors.New("business failure")
		p, err := scoped(func(context.Context, pgx.Tx) error { return boom })
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		emptyContext(t, rt, p)
	})

	t.Run("after a SQL error", func(t *testing.T) {
		p, err := scoped(func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT * FROM no_such_table")
			return err
		})
		if err == nil {
			t.Fatal("expected the SQL error")
		}
		emptyContext(t, rt, p)
	})

	t.Run("after a panic in fn", func(t *testing.T) {
		var p int
		func() {
			defer func() {
				if recover() == nil {
					t.Error("the panic must propagate")
				}
			}()
			if err := rt.InScopedTx(t.Context(), r1, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				if p, err = pid(ctx, tx); err != nil {
					return err
				}
				panic("boom")
			}); err != nil {
				t.Error(err)
			}
		}()
		emptyContext(t, rt, p)
	})

	t.Run("system context does not leak", func(t *testing.T) {
		var p int
		err := rt.InSystemTx(t.Context(), "cross-tenant job", pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			if p, err = pid(ctx, tx); err != nil {
				return err
			}
			g, err := guc(ctx, tx)
			if err != nil {
				return err
			}
			if g[postgres.GUCActor] != "system" {
				return errors.New("the actor is not system inside InSystemTx")
			}
			v, err := readVisible(ctx, tx)
			if err != nil {
				return err
			}
			if len(v.dishes) != 3 {
				return errors.New("the system actor does not see all rows")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		emptyContext(t, rt, p)

		// A scoped transaction right after a system one is not system either.
		err = rt.InScopedTx(t.Context(), postgres.Scope{Actor: postgres.ActorAnonymous}, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			g, err := guc(ctx, tx)
			if err != nil {
				return err
			}
			if g[postgres.GUCActor] != "anonymous" || g[postgres.GUCOrgID] != "-" {
				return errors.New("actor or ids leaked from the system transaction")
			}
			v, err := readVisible(ctx, tx)
			if err != nil {
				return err
			}
			if len(v.dishes)+len(v.settings)+len(v.profiles) != 0 {
				return errors.New("anonymous sees rows")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("scope is set again on every retry", func(t *testing.T) {
		attempts := 0
		err := rt.InScopedTx(t.Context(), r1, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			attempts++
			g, err := guc(ctx, tx)
			if err != nil {
				return err
			}
			if g[postgres.GUCRestaurantID] != "r1" || g[postgres.GUCActor] != "tenant" || g[postgres.GUCUserID] != "u1" {
				return errors.New("scope missing on attempt")
			}
			if attempts < 3 {
				return errors.Join(errors.New("retry"), serializationFailure())
			}
			return nil
		})
		if err != nil || attempts != 3 {
			t.Fatalf("err=%v attempts=%d", err, attempts)
		}
	})
}
