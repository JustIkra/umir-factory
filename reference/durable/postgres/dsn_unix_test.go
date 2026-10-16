//go:build unix

package postgres

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLoadDSNRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("FIFOs are not available: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := LoadDSN(envFrom(map[string]string{"AUTH_DATABASE_URL_FILE": path}), "AUTH")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO must be rejected")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO blocked")
	}
}

func TestLoadDSNRejectsFileOverTheSizeLimit(t *testing.T) {
	path := writeSecret(t, "postgres://svc@db/app?x="+strings.Repeat("a", maxSecretFileSize), 0o600)
	if _, err := LoadDSN(envFrom(map[string]string{"AUTH_DATABASE_URL_FILE": path}), "AUTH"); err == nil {
		t.Fatal("a file over the limit must be rejected")
	}
}
