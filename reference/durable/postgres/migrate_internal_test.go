package postgres

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadMigrationsOrdersAndExpandsFragments(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/0002_second.sql": {Data: []byte("CREATE TABLE b (id int);")},
		"migrations/0001_first.sql":  {Data: []byte("-- umir:include inbox\nCREATE TABLE a (id int);")},
	}
	list, err := loadMigrations(Spec{Service: "demo", FS: fsys, Dir: "migrations"})
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(list) != 2 || list[0].version != 1 || list[0].name != "first" || list[1].version != 2 || list[1].name != "second" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if !strings.Contains(list[0].sql, "CREATE TABLE inbox") || !strings.Contains(list[0].sql, "CREATE TABLE a") {
		t.Fatalf("fragment was not expanded:\n%s", list[0].sql)
	}
	if strings.Contains(list[0].sql, "umir:include") {
		t.Fatal("the include directive must be replaced")
	}
}

func TestLoadMigrationsRejectsBadSets(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"gap":              {"m/0001_a.sql": {Data: []byte("SELECT 1")}, "m/0003_c.sql": {Data: []byte("SELECT 1")}},
		"starts at two":    {"m/0002_a.sql": {Data: []byte("SELECT 1")}},
		"duplicate":        {"m/0001_a.sql": {Data: []byte("SELECT 1")}, "m/0001_b.sql": {Data: []byte("SELECT 1")}},
		"bad name":         {"m/1_a.sql": {Data: []byte("SELECT 1")}},
		"upper case":       {"m/0001_A.sql": {Data: []byte("SELECT 1")}},
		"stray file":       {"m/0001_a.sql": {Data: []byte("SELECT 1")}, "m/README.md": {Data: []byte("x")}},
		"empty":            {},
		"empty body":       {"m/0001_a.sql": {Data: []byte("  \n")}},
		"unknown fragment": {"m/0001_a.sql": {Data: []byte("-- umir:include nope\n")}},
	}
	for name, fsys := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadMigrations(Spec{Service: "demo", FS: fsys, Dir: "m"}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestSpecValidate(t *testing.T) {
	good := fstest.MapFS{"m/0001_a.sql": {Data: []byte("SELECT 1")}}
	for _, svc := range []string{"", "Warehouse", "a b", "a.b", "-a", strings.Repeat("a", 41)} {
		if _, err := loadMigrations(Spec{Service: svc, FS: good, Dir: "m"}); err == nil {
			t.Errorf("service %q must be rejected", svc)
		}
	}
	if _, err := loadMigrations(Spec{Service: "demo", Dir: "m"}); err == nil {
		t.Error("nil FS must be rejected")
	}
	if _, err := loadMigrations(Spec{Service: "demo", FS: good, Dir: "m"}); err != nil {
		t.Errorf("valid spec rejected: %v", err)
	}
}

func TestCheckApplied(t *testing.T) {
	list := []migration{{version: 1, name: "a"}, {version: 2, name: "b"}}
	cases := []struct {
		name    string
		applied []appliedMigration
		want    error
	}{
		{"none", nil, nil},
		{"prefix", []appliedMigration{{1, "a"}}, nil},
		{"all", []appliedMigration{{1, "a"}, {2, "b"}}, nil},
		{"newer", []appliedMigration{{1, "a"}, {2, "b"}, {3, "c"}}, ErrSchemaNewer},
		{"renamed", []appliedMigration{{1, "a"}, {2, "x"}}, ErrMigrationMismatch},
		{"gap", []appliedMigration{{2, "b"}}, ErrMigrationMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkApplied(tc.applied, list)
			if !errors.Is(err, tc.want) {
				t.Fatalf("checkApplied = %v, want %v", err, tc.want)
			}
		})
	}
}
