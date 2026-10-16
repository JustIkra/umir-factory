//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/JustIkra/umir-factory/reference/durable/itest"
	"github.com/JustIkra/umir-factory/reference/durable/postgres"
)

func randHex(t testing.TB) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// fakeClock is the injected time source of the DB under test.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// env is a temporary database with a real owner role and a real runtime role.
// The owner creates the tables, the runtime role is not the owner and has
// NOBYPASSRLS, like umir_<svc>_owner and umir_<svc> in production (C8).
type env struct {
	AdminURL    string // superuser of the temporary database
	OwnerURL    string
	RuntimeURL  string
	OwnerRole   string
	RuntimeRole string
	password    string

	mu    sync.Mutex
	roles []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := t.Context()
	e := &env{password: randHex(t) + randHex(t)}
	base := "t_factory_" + randHex(t)
	e.OwnerRole = base + "_owner"
	e.RuntimeRole = base + "_rt"

	// Roles first, database second: cleanups run in reverse, so the database
	// goes before the roles and nothing keeps a role referenced.
	cluster := itest.DatabaseURL(t)
	conn, err := pgx.Connect(ctx, cluster)
	if err != nil {
		t.Fatalf("connect to the cluster: %v", err)
	}
	defer closeConn(conn)
	e.roles = []string{e.OwnerRole, e.RuntimeRole}
	t.Cleanup(func() { e.dropRoles(t, cluster) })
	for _, role := range e.roles {
		createRole(t, conn, role, e.password, "NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS")
	}

	e.AdminURL = itest.NewDatabase(t)
	admin, err := pgx.Connect(ctx, e.AdminURL)
	if err != nil {
		t.Fatalf("connect to the temporary database: %v", err)
	}
	defer closeConn(admin)
	owner, rt := pgx.Identifier{e.OwnerRole}.Sanitize(), pgx.Identifier{e.RuntimeRole}.Sanitize()
	for _, stmt := range []string{
		fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s, %s", pgx.Identifier{databaseName(t, e.AdminURL)}.Sanitize(), owner, rt),
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
		fmt.Sprintf("GRANT USAGE, CREATE ON SCHEMA public TO %s", owner),
		fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s", rt),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s", owner, rt),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO %s", owner, rt),
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	e.OwnerURL = withUser(t, e.AdminURL, e.OwnerRole, e.password)
	e.RuntimeURL = withUser(t, e.AdminURL, e.RuntimeRole, e.password)
	return e
}

// loginRole adds one more login role (for negative checks of AssertRuntimeRole)
// and returns its URL. The role is dropped with the environment.
func (e *env) loginRole(t *testing.T, suffix, attributes string, grants ...string) string {
	t.Helper()
	ctx := t.Context()
	role := "t_factory_" + randHex(t) + "_" + suffix
	cluster := itest.DatabaseURL(t)
	conn, err := pgx.Connect(ctx, cluster)
	if err != nil {
		t.Fatalf("connect to the cluster: %v", err)
	}
	defer closeConn(conn)
	e.mu.Lock()
	e.roles = append(e.roles, role)
	e.mu.Unlock()
	createRole(t, conn, role, e.password, attributes)
	admin, err := pgx.Connect(ctx, e.AdminURL)
	if err != nil {
		t.Fatalf("connect to the temporary database: %v", err)
	}
	defer closeConn(admin)
	quoted := pgx.Identifier{role}.Sanitize()
	stmts := append([]string{
		"GRANT CONNECT ON DATABASE " + pgx.Identifier{databaseName(t, e.AdminURL)}.Sanitize() + " TO %s",
		"GRANT USAGE ON SCHEMA public TO %s",
	}, grants...)
	for _, stmt := range stmts {
		if _, err := admin.Exec(ctx, fmt.Sprintf(stmt, quoted)); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return withUser(t, e.AdminURL, role, e.password)
}

func (e *env) dropRoles(t *testing.T, cluster string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cluster)
	if err != nil {
		t.Errorf("connect to drop roles: %v", err)
		return
	}
	defer closeConn(conn)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, role := range e.roles {
		quoted := pgx.Identifier{role}.Sanitize()
		if _, err := conn.Exec(ctx, "DROP OWNED BY "+quoted); err != nil {
			t.Errorf("drop owned by %s: %v", role, err)
		}
		if _, err := conn.Exec(ctx, "DROP ROLE IF EXISTS "+quoted); err != nil {
			t.Errorf("drop role %s: %v", role, err)
		}
	}
}

func createRole(t *testing.T, conn *pgx.Conn, role, password, attributes string) {
	t.Helper()
	// The password is random hex produced above, so inlining it is safe.
	stmt := fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' %s", pgx.Identifier{role}.Sanitize(), password, attributes)
	if _, err := conn.Exec(t.Context(), stmt); err != nil {
		t.Fatalf("create role %s: %v", role, err)
	}
}

func closeConn(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Close(ctx); err != nil {
		return
	}
}

func withUser(t testing.TB, raw, user, password string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

func databaseName(t testing.TB, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	return u.Path[1:]
}

func openDB(t testing.TB, rawURL string, mutate func(*postgres.Config)) *postgres.DB {
	t.Helper()
	cfg := postgres.Config{URL: rawURL, ApplicationName: "t-factory-postgres", MaxConns: 4, ConnectTimeout: 10 * time.Second}
	if mutate != nil {
		mutate(&cfg)
	}
	db, err := postgres.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func withClock(c *fakeClock) func(*postgres.Config) {
	return func(cfg *postgres.Config) { cfg.Clock = c.Now }
}

func withMaxConns(n int32) func(*postgres.Config) {
	return func(cfg *postgres.Config) { cfg.MaxConns = n }
}

// testSpec returns the migrations of the fictional service "demo". Version 1
// takes all four reference fragments, version 2 adds scoped tables with the
// reference RLS policies.
func testSpec(t testing.TB) postgres.Spec {
	t.Helper()
	policies := ""
	for _, p := range [][2]string{{"dishes", "restaurant_id"}, {"settings", "org_id"}, {"profiles", "user_id"}} {
		sql, err := postgres.RLSPolicySQL(p[0], p[1])
		if err != nil {
			t.Fatalf("RLSPolicySQL: %v", err)
		}
		policies += sql + "\n"
	}
	return postgres.Spec{
		Service: "demo",
		Dir:     "migrations",
		FS: fstest.MapFS{
			"migrations/0001_base.sql": {Data: []byte(`-- umir:include storage_meta
-- umir:include worker_leases
-- umir:include outbox
-- umir:include inbox
CREATE TABLE biz (id int PRIMARY KEY, note text NOT NULL DEFAULT '');
`)},
			"migrations/0002_scoped.sql": {Data: []byte(`CREATE TABLE dishes (id serial PRIMARY KEY, org_id text NOT NULL, restaurant_id text NOT NULL, name text NOT NULL);
CREATE TABLE settings (id serial PRIMARY KEY, org_id text NOT NULL, theme text NOT NULL);
CREATE TABLE profiles (id serial PRIMARY KEY, user_id text NOT NULL, nick text NOT NULL);
` + policies)},
		},
	}
}

// fixture is a migrated environment: the owner has applied testSpec, runtime
// connections use the real runtime role.
type fixture struct {
	env   *env
	spec  postgres.Spec
	owner *postgres.DB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	e := newEnv(t)
	spec := testSpec(t)
	owner := openDB(t, e.OwnerURL, nil)
	if err := postgres.Migrate(t.Context(), owner, spec); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &fixture{env: e, spec: spec, owner: owner}
}

func (f *fixture) runtime(t testing.TB, mutate func(*postgres.Config)) *postgres.DB {
	t.Helper()
	return openDB(t, f.env.RuntimeURL, mutate)
}

func count(t testing.TB, ctx context.Context, db *postgres.DB, query string, args ...any) int {
	t.Helper()
	var n int
	err := db.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, args...).Scan(&n)
	})
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func serializationFailure() error { return &pgconn.PgError{Code: "40001"} }

// mapFS returns the in-memory migration files of a spec built by testSpec.
func mapFS(t testing.TB, spec postgres.Spec) fstest.MapFS {
	t.Helper()
	files, ok := spec.FS.(fstest.MapFS)
	if !ok {
		t.Fatal("the spec does not use fstest.MapFS")
	}
	return files
}

// ignoreErr documents a deliberately ignored error from a cleanup path.
func ignoreErr(error) {}
