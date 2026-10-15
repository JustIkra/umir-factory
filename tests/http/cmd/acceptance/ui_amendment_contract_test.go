package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestUIAmendmentCompositionIsReproducibleAndPreservesInputs(t *testing.T) {
	directory := t.TempDir()
	inputs := []string{"backend-v2-public.json", "backend-v2-private.json", "ui-amendment-v1-public.json", "ui-amendment-v1-private.json"}
	before := map[string][32]byte{}
	for _, name := range inputs {
		body, err := os.ReadFile(filepath.Join(completionAPI(t), name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = sha256.Sum256(body)
		if err := os.WriteFile(filepath.Join(directory, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	script := filepath.Join(completionAPI(t), "../../tests/http/tools/compose-ui-contract.mjs")
	command := exec.CommandContext(ctx, "node", script, directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compose UA1: %v: %s", err, output)
	}
	for _, name := range inputs {
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || sha256.Sum256(body) != before[name] {
			t.Fatal("composition modified a predecessor/input", name, err)
		}
	}
	for _, name := range []string{"ui-amendment-v1-composed-public.json", "ui-amendment-v1-composed-private.json"} {
		actual, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		committed, err := os.ReadFile(filepath.Join(completionAPI(t), name))
		if err != nil || !bytes.Equal(actual, committed) {
			t.Fatal("composed UA1 profile differs from its inputs", name, err)
		}
		var document struct {
			Sources map[string]string `json:"x-ua1-source"`
		}
		if err := json.Unmarshal(actual, &document); err != nil {
			t.Fatal(err)
		}
		for input, digest := range before {
			if document.Sources[input] != hex.EncodeToString(digest[:]) {
				t.Fatal("profile lost its exact source identity", input)
			}
		}
	}
}
