package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestInitializeCreatesPrivateRandomCredentialsAndOnlyMetadata(t *testing.T) {
	directory := t.TempDir()
	var output bytes.Buffer
	if err := runAt(directory, strings.NewReader("{\"version\":1}\n"), &output, false); err != nil {
		t.Fatal(err)
	}
	var files fixtureFiles
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &files); err != nil {
		t.Fatalf("fixture metadata is not JSON: %v", err)
	}
	usernamePath := filepath.Join(directory, "proxy-username")
	passwordPath := filepath.Join(directory, "proxy-password")
	username, err := os.ReadFile(usernamePath)
	if err != nil {
		t.Fatal(err)
	}
	password, err := os.ReadFile(passwordPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(username) != 64 || len(password) != 64 || bytes.Equal(username, password) {
		t.Fatal("generated credentials are not distinct 256-bit hex strings")
	}
	if mode := mustMode(t, usernamePath); mode != 0o400 {
		t.Errorf("username mode = %04o, want 0400", mode)
	}
	if mode := mustMode(t, passwordPath); mode != 0o600 {
		t.Errorf("password mode = %04o, want 0600", mode)
	}
	subscriptionPath := filepath.Join(directory, "subscription-url")
	subscription, err := os.ReadFile(subscriptionPath)
	if err != nil {
		t.Fatalf("read synthetic subscription file: %v", err)
	}
	if string(subscription) != "https://subscription.invalid/factory-acceptance" {
		t.Fatalf("subscription file contains unexpected data")
	}
	if files.SubscriptionPath != "/run/feedback-fixture/subscription-url" || files.SubscriptionMode != 0o400 {
		t.Fatalf("subscription metadata = %+v", files)
	}
	if mode := mustMode(t, subscriptionPath); mode != 0o400 {
		t.Errorf("subscription mode = %04o, want 0400", mode)
	}
	usernameInfo, err := os.Lstat(usernamePath)
	if err != nil {
		t.Fatal(err)
	}
	usernameStat, ok := usernameInfo.Sys().(*syscall.Stat_t)
	if !ok || int(usernameStat.Uid) != os.Getuid() || files.UID != os.Getuid() {
		t.Fatalf("credential owner = %d, metadata uid = %d, current uid = %d", usernameStat.Uid, files.UID, os.Getuid())
	}
	if _, err := os.Stat(filepath.Join(directory, "unexpected")); !os.IsNotExist(err) {
		t.Fatalf("unexpected extra file exists: %v", err)
	}
	if strings.Contains(output.String(), string(username)) || strings.Contains(output.String(), string(password)) {
		t.Fatal("fixture output contains credential content")
	}
}

func TestFixtureRequestRequiresOneKnownVersionedObject(t *testing.T) {
	for _, input := range []string{"", "{}", "{\"version\":2}", "{\"version\":1,\"secret\":\"x\"}", "{\"version\":1}{\"version\":1}", "{\"version\":1}" + strings.Repeat(" ", 4096)} {
		if err := validateRequest(strings.NewReader(input)); err == nil {
			t.Errorf("accepted fixture request %q", input)
		}
	}
	if err := validateRequest(strings.NewReader("{\"version\":1}\n")); err != nil {
		t.Fatalf("valid fixture request rejected: %v", err)
	}
}

func mustMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
