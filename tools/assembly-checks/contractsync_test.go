package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	authCopyPath     = "umir-auth/internal/adapters/httpapi/contract.json"
	gatewayCopyPath  = "umir-gateway/internal/operatorhttp/auth-contract.json"
	frontendCopyPath = "umir-frontend/api/backend-v2-public.json"
)

func sumHex(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gatewayCopy(publicSum, privateSum string) string {
	return fmt.Sprintf(`{"source":{"publicSHA256":%q,"privateSHA256":%q},"operations":{}}`, publicSum, privateSum)
}

// syncFixture writes composed files for the given profile and in-sync consumer copies.
func syncFixture(t *testing.T, profile string) (factory, workspace string) {
	t.Helper()
	factory, workspace = t.TempDir(), t.TempDir()
	public, private := profile+" public", profile+" private"
	writeTestFile(t, filepath.Join(factory, "docs/api", profile+"-v1-composed-public.json"), public)
	writeTestFile(t, filepath.Join(factory, "docs/api", profile+"-v1-composed-private.json"), private)
	writeTestFile(t, filepath.Join(workspace, authCopyPath), private)
	writeTestFile(t, filepath.Join(workspace, gatewayCopyPath), gatewayCopy(sumHex(public), sumHex(private)))
	writeTestFile(t, filepath.Join(workspace, frontendCopyPath), public)
	return factory, workspace
}

func syncRun(t *testing.T, factory, workspace string, extra ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"contract-sync", "--factory-dir", factory, "--workspace", workspace}, extra...)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestContractSyncMatchesBothProfiles(t *testing.T) {
	for _, profile := range []string{"registration", "durable"} {
		t.Run(profile, func(t *testing.T) {
			factory, workspace := syncFixture(t, profile)
			code, stdout, stderr := syncRun(t, factory, workspace, "--profile", profile)
			if code != 0 || stderr != "" {
				t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
			}
		})
	}
}

func TestContractSyncDefaultsToDurableProfile(t *testing.T) {
	factory, workspace := syncFixture(t, "durable")
	if code, _, stderr := syncRun(t, factory, workspace); code != 0 {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestContractSyncReportsDrift(t *testing.T) {
	factory, workspace := syncFixture(t, "registration")
	writeTestFile(t, filepath.Join(workspace, authCopyPath), "stale private")
	writeTestFile(t, filepath.Join(workspace, gatewayCopyPath), gatewayCopy(sumHex("registration public"), sumHex("stale")))
	if err := os.Remove(filepath.Join(workspace, frontendCopyPath)); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := syncRun(t, factory, workspace, "--profile", "registration")
	if code != 1 {
		t.Fatalf("code %d, want 1", code)
	}
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) != 3 {
		t.Fatalf("want one line per drift, got %q", stderr)
	}
	for i, want := range []string{
		"umir-auth: " + authCopyPath + ": private SHA-256 differs",
		"umir-gateway: " + gatewayCopyPath + ": privateSHA256 differs",
		"umir-frontend: " + frontendCopyPath + ": missing",
	} {
		if !strings.HasPrefix(lines[i], want) {
			t.Fatalf("line %d is %q, want prefix %q", i, lines[i], want)
		}
	}
	if strings.Contains(stderr, "stale private") {
		t.Fatalf("file content leaked: %q", stderr)
	}
}

func TestContractSyncGatewayPublicDrift(t *testing.T) {
	factory, workspace := syncFixture(t, "durable")
	writeTestFile(t, filepath.Join(workspace, gatewayCopyPath), gatewayCopy(sumHex("stale public"), sumHex("durable private")))
	code, _, stderr := syncRun(t, factory, workspace)
	if code != 1 {
		t.Fatalf("code %d, want 1", code)
	}
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "umir-gateway: "+gatewayCopyPath+": publicSHA256 differs: want "+sumHex("durable public")) {
		t.Fatalf("stderr %q", stderr)
	}
}

func TestContractSyncTruncatesObservedValue(t *testing.T) {
	factory, workspace := syncFixture(t, "durable")
	long := strings.Repeat("x", 500)
	writeTestFile(t, filepath.Join(workspace, gatewayCopyPath), gatewayCopy(long, sumHex("durable private")))
	code, _, stderr := syncRun(t, factory, workspace)
	if code != 1 {
		t.Fatalf("code %d, want 1", code)
	}
	if strings.Contains(stderr, strings.Repeat("x", 65)) || !strings.Contains(stderr, strings.Repeat("x", 64)) {
		t.Fatalf("observed value must be cut to 64 characters: %q", stderr)
	}
	// A value of exactly 64 characters is printed whole.
	if got := shortValue(strings.Repeat("y", 64)); got != strings.Repeat("y", 64) {
		t.Fatalf("64-character value changed: %q", got)
	}
	if got := shortValue("é" + strings.Repeat("y", 70)); len([]rune(got)) != 64 || !strings.HasPrefix(got, "é") {
		t.Fatalf("multibyte value mishandled: %q", got)
	}
}

func TestContractSyncGatewayMalformedCopy(t *testing.T) {
	factory, workspace := syncFixture(t, "registration")
	writeTestFile(t, filepath.Join(workspace, gatewayCopyPath), `{"source":`)
	code, _, stderr := syncRun(t, factory, workspace, "--profile", "registration")
	if code != 1 || !strings.Contains(stderr, "umir-gateway: "+gatewayCopyPath+": unreadable") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestContractSyncProfilesAreDistinct(t *testing.T) {
	factory, workspace := syncFixture(t, "registration")
	if code, _, stderr := syncRun(t, factory, workspace, "--profile", "registration"); code != 0 {
		t.Fatalf("registration code %d stderr %q", code, stderr)
	}
	// The durable composed files are absent from this factory: an environment
	// error (2), not drift (1).
	if code, _, stderr := syncRun(t, factory, workspace, "--profile", "durable"); code != 2 || !strings.Contains(stderr, "durable-v1-composed-public.json") {
		t.Fatalf("durable code %d stderr %q", code, stderr)
	}
	// Durable composed files that differ from the registration copies show drift.
	writeTestFile(t, filepath.Join(factory, "docs/api/durable-v1-composed-public.json"), "durable public")
	writeTestFile(t, filepath.Join(factory, "docs/api/durable-v1-composed-private.json"), "durable private")
	code, _, stderr := syncRun(t, factory, workspace, "--profile", "durable")
	if code != 1 || len(strings.Split(strings.TrimSpace(stderr), "\n")) != 4 {
		t.Fatalf("durable code %d stderr %q", code, stderr)
	}
}

func TestContractSyncRejectsBadOptions(t *testing.T) {
	factory, workspace := syncFixture(t, "durable")
	for name, args := range map[string][]string{
		"no workspace":       {"contract-sync", "--factory-dir", factory},
		"relative workspace": {"contract-sync", "--factory-dir", factory, "--workspace", "relative"},
		"unknown profile":    {"contract-sync", "--factory-dir", factory, "--workspace", workspace, "--profile", "other"},
		"relative factory":   {"contract-sync", "--factory-dir", "relative", "--workspace", workspace},
		"extra argument":     {"contract-sync", "--factory-dir", factory, "--workspace", workspace, "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			if code := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
				t.Fatalf("code %d, want 2", code)
			}
		})
	}
}
