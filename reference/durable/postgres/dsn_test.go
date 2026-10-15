package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	secretURL    = "postgres://svc:s3cr3t-pass@db.internal:5432/app?sslmode=verify-full"
	secretMarker = "s3cr3t-pass"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func writeSecret(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dsn")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod secret file: %v", err)
	}
	return path
}

func TestLoadDSNFromVariable(t *testing.T) {
	got, err := LoadDSN(envFrom(map[string]string{"WAREHOUSE_DATABASE_URL": secretURL}), "WAREHOUSE")
	if err != nil || got != secretURL {
		t.Fatalf("LoadDSN = %q, %v", got, err)
	}
}

func TestLoadDSNFromFileTrimsOneNewline(t *testing.T) {
	path := writeSecret(t, secretURL+"\n", 0o600)
	got, err := LoadDSN(envFrom(map[string]string{"SERVING_DATABASE_URL_FILE": path}), "SERVING")
	if err != nil || got != secretURL {
		t.Fatalf("LoadDSN = %q, %v", got, err)
	}
}

func TestLoadDSNRejectsBothSources(t *testing.T) {
	path := writeSecret(t, secretURL, 0o600)
	_, err := LoadDSN(envFrom(map[string]string{
		"FEEDBACK_DATABASE_URL":      secretURL,
		"FEEDBACK_DATABASE_URL_FILE": path,
	}), "FEEDBACK")
	if err == nil {
		t.Fatal("both the variable and the file must be an error")
	}
	assertNoSecret(t, err, path)
	if !strings.Contains(err.Error(), "FEEDBACK_DATABASE_URL") {
		t.Fatalf("error should name the variables: %v", err)
	}
}

func TestLoadDSNRejectsMissingAndBadSources(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]map[string]string{
		"nothing configured": {},
		"file missing":       {"AUTH_DATABASE_URL_FILE": filepath.Join(dir, "absent")},
		"file empty":         {"AUTH_DATABASE_URL_FILE": writeSecret(t, "\n", 0o600)},
		"file readable by group": {
			"AUTH_DATABASE_URL_FILE": writeSecret(t, secretURL, 0o640),
		},
		"directory": {"AUTH_DATABASE_URL_FILE": dir},
		"not a postgres url": {
			"AUTH_DATABASE_URL": "mysql://svc:" + secretMarker + "@db/app",
		},
		"not a url at all": {"AUTH_DATABASE_URL": "host=db password=" + secretMarker},
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := LoadDSN(envFrom(values), "AUTH")
			if err == nil {
				t.Fatalf("expected an error, got %q", got)
			}
			assertNoSecret(t, err, values["AUTH_DATABASE_URL_FILE"])
		})
	}
}

func TestLoadDSNRejectsSymlink(t *testing.T) {
	target := writeSecret(t, secretURL, 0o600)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	if _, err := LoadDSN(envFrom(map[string]string{"AUTH_DATABASE_URL_FILE": link}), "AUTH"); err == nil {
		t.Fatal("a symlink must be rejected")
	}
}

func TestLoadDSNRejectsBadPrefix(t *testing.T) {
	for _, prefix := range []string{"", "auth", "A-B", "A B"} {
		if _, err := LoadDSN(envFrom(nil), prefix); err == nil {
			t.Fatalf("prefix %q must be rejected", prefix)
		}
	}
}

func TestLoadMigrateDSNIsFileOnly(t *testing.T) {
	path := writeSecret(t, secretURL+"\n", 0o600)
	got, err := LoadMigrateDSN(envFrom(map[string]string{"AUTH_MIGRATE_DATABASE_URL_FILE": path}), "AUTH")
	if err != nil || got != secretURL {
		t.Fatalf("LoadMigrateDSN = %q, %v", got, err)
	}

	// The runtime DSN variables are never used for migrations.
	if _, err := LoadMigrateDSN(envFrom(map[string]string{"AUTH_DATABASE_URL_FILE": path}), "AUTH"); err == nil {
		t.Fatal("the runtime DSN must not satisfy LoadMigrateDSN")
	}

	// The owner DSN does not travel in the environment; the variable form is an error.
	_, err = LoadMigrateDSN(envFrom(map[string]string{"AUTH_MIGRATE_DATABASE_URL": secretURL}), "AUTH")
	if err == nil {
		t.Fatal("AUTH_MIGRATE_DATABASE_URL must be rejected")
	}
	assertNoSecret(t, err, "")

	_, err = LoadMigrateDSN(envFrom(map[string]string{
		"AUTH_MIGRATE_DATABASE_URL":      secretURL,
		"AUTH_MIGRATE_DATABASE_URL_FILE": path,
	}), "AUTH")
	if err == nil {
		t.Fatal("both the variable and the file must be an error")
	}
	assertNoSecret(t, err, path)

	if _, err := LoadMigrateDSN(envFrom(nil), "AUTH"); err == nil {
		t.Fatal("a missing migrate DSN must be an error")
	}
}

func assertNoSecret(t *testing.T, err error, extra string) {
	t.Helper()
	text := err.Error()
	if strings.Contains(text, secretMarker) || strings.Contains(text, "svc:") {
		t.Fatalf("error leaks the DSN: %v", err)
	}
	if extra != "" && strings.Contains(text, extra) {
		t.Fatalf("error leaks the secret file path: %v", err)
	}
}
