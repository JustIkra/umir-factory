//go:build integration

package itest

import (
	"context"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"
)

var dbNamePattern = regexp.MustCompile(`^t_factory_[0-9a-f]{8}$`)

func databaseExists(t *testing.T, name string) bool {
	t.Helper()
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, DatabaseURL(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer closePG(conn)
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		t.Fatalf("pg_database: %v", err)
	}
	return exists
}

func TestNewDatabaseCreatesAndDrops(t *testing.T) {
	var name string
	t.Run("use", func(t *testing.T) {
		ctx := t.Context()
		conn, err := pgx.Connect(ctx, NewDatabase(t))
		if err != nil {
			t.Fatalf("connect to the new database: %v", err)
		}
		// The connection stays open on purpose: DROP ... WITH (FORCE) must
		// disconnect it.
		t.Cleanup(func() { closePG(conn) })
		if err := conn.QueryRow(ctx, "SELECT current_database()").Scan(&name); err != nil {
			t.Fatalf("current_database: %v", err)
		}
		if !dbNamePattern.MatchString(name) {
			t.Fatalf("database name %q does not match %s", name, dbNamePattern)
		}
		if !databaseExists(t, name) {
			t.Fatalf("database %s is not listed in pg_database", name)
		}
		if _, err := conn.Exec(ctx, "CREATE TABLE probe(id int)"); err != nil {
			t.Fatalf("the new database is not writable: %v", err)
		}
	})
	if name == "" {
		t.Fatal("subtest did not report the database name")
	}
	if databaseExists(t, name) {
		t.Fatalf("database %s still exists after cleanup", name)
	}
}

func TestNewDatabaseNamesAreUnique(t *testing.T) {
	a, b := NewDatabase(t), NewDatabase(t)
	if a == b {
		t.Fatal("two calls returned the same database URL")
	}
}

func TestDropDatabaseRefusesForeignNames(t *testing.T) {
	if err := dropDatabase(context.Background(), DatabaseURL(t), "postgres"); err == nil {
		t.Fatal("dropDatabase accepted a name outside t_factory_")
	}
	if err := dropDatabase(context.Background(), DatabaseURL(t), "t_other_12345678"); err == nil {
		t.Fatal("dropDatabase accepted a name of another repository")
	}
}
