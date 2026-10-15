package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCompletionPrivatePreparation(t *testing.T) {
	path, err := filepath.Abs("../../tests/infra/completion/prepare.test.mjs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "--test", path, filepath.Join(filepath.Dir(path), "seed.test.mjs")).CombinedOutput()
	if err != nil {
		t.Fatalf("completion preparation: %v\n%s", err, output)
	}
}
