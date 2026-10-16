package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The Compose model of the completion profile is checked by the Node tests next
// to the preparation tests: hardening, limits, health checks, networks and image
// digests. This runs them from the Go gate, as for the preparation.
func TestCompletionComposeModel(t *testing.T) {
	path, err := filepath.Abs("../../tests/infra/completion/compose.test.mjs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "--test", path, filepath.Join(filepath.Dir(path), "transport-health.test.mjs")).CombinedOutput()
	if err != nil {
		t.Fatalf("completion compose model: %v\n%s", err, output)
	}
}
