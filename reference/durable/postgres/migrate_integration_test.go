//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/JustIkra/umir-factory/reference/durable/postgres"
)

func appliedVersions(t *testing.T, db *postgres.DB) []string {
	t.Helper()
	var out []string
	err := db.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT version::text || ':' || name FROM schema_migrations ORDER BY version")
		if err != nil {
			return err
		}
		defer rows.Close()
		out = nil
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	return out
}

func TestMigrateTwentyParallelRunsApplyOnce(t *testing.T) {
	e := newEnv(t)
	spec := testSpec(t)
	owner := openDB(t, e.OwnerURL, withMaxConns(20))

	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range errs {
		wg.Go(func() { errs[i] = postgres.Migrate(t.Context(), owner, spec) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Migrate #%d: %v", i, err)
		}
	}
	got := appliedVersions(t, owner)
	if len(got) != 2 || got[0] != "1:base" || got[1] != "2:scoped" {
		t.Fatalf("schema_migrations = %v, want [1:base 2:scoped]", got)
	}
	if n := count(t, t.Context(), owner, "SELECT count(*) FROM storage_meta"); n != 1 {
		t.Fatalf("storage_meta rows = %d, want exactly one generation", n)
	}
	var applied bool
	if err := owner.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT min(applied_at) IS NOT NULL FROM schema_migrations").Scan(&applied)
	}); err != nil || !applied {
		t.Fatalf("applied_at is not recorded: %v %v", applied, err)
	}
	if err := postgres.Migrate(t.Context(), owner, spec); err != nil {
		t.Fatalf("a repeated Migrate must succeed: %v", err)
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	f := newFixture(t)
	older := postgres.Spec{Service: "demo", Dir: "migrations", FS: fstest.MapFS{
		"migrations/0001_base.sql": mapFS(t, f.spec)["migrations/0001_base.sql"],
	}}
	err := postgres.Migrate(t.Context(), f.owner, older)
	if !errors.Is(err, postgres.ErrSchemaNewer) {
		t.Fatalf("Migrate with an older build = %v, want ErrSchemaNewer", err)
	}
	if err := postgres.VerifySchema(t.Context(), f.owner, older); !errors.Is(err, postgres.ErrSchemaNewer) {
		t.Fatalf("VerifySchema with an older build = %v, want ErrSchemaNewer", err)
	}
}

func TestMigrateRefusesChangedMigrationName(t *testing.T) {
	f := newFixture(t)
	renamed := postgres.Spec{Service: "demo", Dir: "migrations", FS: fstest.MapFS{
		"migrations/0001_base.sql":    mapFS(t, f.spec)["migrations/0001_base.sql"],
		"migrations/0002_renamed.sql": mapFS(t, f.spec)["migrations/0002_scoped.sql"],
	}}
	if err := postgres.Migrate(t.Context(), f.owner, renamed); !errors.Is(err, postgres.ErrMigrationMismatch) {
		t.Fatalf("Migrate = %v, want ErrMigrationMismatch", err)
	}
	if err := postgres.VerifySchema(t.Context(), f.owner, renamed); !errors.Is(err, postgres.ErrMigrationMismatch) {
		t.Fatalf("VerifySchema = %v, want ErrMigrationMismatch", err)
	}
}

func TestMigrateRollsBackAFailingMigration(t *testing.T) {
	e := newEnv(t)
	owner := openDB(t, e.OwnerURL, nil)
	bad := postgres.Spec{Service: "demo", Dir: "m", FS: fstest.MapFS{
		"m/0001_ok.sql":  {Data: []byte("CREATE TABLE first_ok (id int);")},
		"m/0002_bad.sql": {Data: []byte("CREATE TABLE second_half (id int); SELECT * FROM does_not_exist;")},
	}}
	err := postgres.Migrate(t.Context(), owner, bad)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("Migrate = %v, want the SQL error", err)
	}
	if got := count(t, t.Context(), owner, "SELECT count(*) FROM pg_tables WHERE tablename IN ('first_ok', 'second_half', 'schema_migrations')"); got != 0 {
		t.Fatalf("%d tables survived a failed migration run; the run is one transaction", got)
	}
}

func TestVerifySchemaRequiresExactVersionForRuntimeRole(t *testing.T) {
	e := newEnv(t)
	spec := testSpec(t)
	runtime := openDB(t, e.RuntimeURL, nil)
	if err := postgres.VerifySchema(t.Context(), runtime, spec); !errors.Is(err, postgres.ErrSchemaOutdated) {
		t.Fatalf("VerifySchema on an empty database = %v, want ErrSchemaOutdated", err)
	}

	owner := openDB(t, e.OwnerURL, nil)
	if err := postgres.Migrate(t.Context(), owner, spec); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := postgres.VerifySchema(t.Context(), runtime, spec); err != nil {
		t.Fatalf("VerifySchema at the expected version: %v", err)
	}

	newer := postgres.Spec{Service: "demo", Dir: "migrations", FS: fstest.MapFS{
		"migrations/0001_base.sql":   mapFS(t, spec)["migrations/0001_base.sql"],
		"migrations/0002_scoped.sql": mapFS(t, spec)["migrations/0002_scoped.sql"],
		"migrations/0003_more.sql":   {Data: []byte("CREATE TABLE more (id int);")},
	}}
	if err := postgres.VerifySchema(t.Context(), runtime, newer); !errors.Is(err, postgres.ErrSchemaOutdated) {
		t.Fatalf("VerifySchema with a newer build = %v, want ErrSchemaOutdated (the runtime role must not migrate)", err)
	}
	// The runtime role cannot migrate: it lacks CREATE on the schema.
	if err := postgres.Migrate(t.Context(), runtime, newer); err == nil {
		t.Fatal("the runtime role must not be able to run migrations")
	}
}

func TestGenerationIsCreatedByTheFirstMigrationAndRotatedByTheOwner(t *testing.T) {
	f := newFixture(t)
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	first, err := postgres.Generation(t.Context(), f.owner)
	if err != nil || !uuidPattern.MatchString(first) {
		t.Fatalf("Generation = %q, %v", first, err)
	}
	again, err := postgres.Generation(t.Context(), f.owner)
	if err != nil || again != first {
		t.Fatalf("Generation is not stable: %q then %q (%v)", first, again, err)
	}

	runtime := f.runtime(t, nil)
	if got, err := postgres.Generation(t.Context(), runtime); err != nil || got != first {
		t.Fatalf("the runtime role must read the generation: %q, %v", got, err)
	}
	// Default privileges give the runtime role UPDATE on new tables; the
	// production grants keep storage_meta read-only for it.
	exec(t, f.owner, "REVOKE INSERT, UPDATE, DELETE ON storage_meta FROM "+quote(f.env.RuntimeRole))

	if _, err := postgres.RotateGeneration(t.Context(), runtime, "restore from backup"); err == nil {
		t.Fatal("the runtime role must not rotate the generation")
	}
	for _, reason := range []string{"", "  ", "line\nbreak"} {
		if _, err := postgres.RotateGeneration(t.Context(), f.owner, reason); err == nil {
			t.Fatalf("reason %q must be rejected", reason)
		}
	}
	before, err := postgres.Generation(t.Context(), f.owner)
	if err != nil {
		t.Fatalf("Generation: %v", err)
	}
	rotated, err := postgres.RotateGeneration(t.Context(), f.owner, "restore from backup")
	if err != nil || !uuidPattern.MatchString(rotated) || rotated == before {
		t.Fatalf("RotateGeneration = %q, %v (before %q)", rotated, err, before)
	}
	if got, err := postgres.Generation(t.Context(), runtime); err != nil || got != rotated {
		t.Fatalf("Generation after rotation = %q, %v; want %q", got, err, rotated)
	}
	var reason, previous string
	if err := f.owner.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT rotation_reason, previous_generation::text FROM storage_meta").Scan(&reason, &previous)
	}); err != nil || reason != "restore from backup" || previous != before {
		t.Fatalf("rotation record: reason=%q previous=%q err=%v", reason, previous, err)
	}
}

func quote(role string) string { return pgx.Identifier{role}.Sanitize() }
