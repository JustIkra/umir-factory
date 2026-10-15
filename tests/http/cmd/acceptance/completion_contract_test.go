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

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func completionAPI(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../../../docs/api")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func completionSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(completionAPI(t), "completion-v1-composed-public.json"))
	if err != nil {
		t.Fatal(err)
	}
	resource, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("fin.json", resource); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("fin.json#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func completionExamples(t *testing.T) []struct {
	Name, Schema string
	Value        map[string]any
} {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(completionAPI(t), "completion-v1-examples.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Examples []struct {
			Name, Schema string
			Value        map[string]any
		}
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Examples) != 3 {
		t.Fatal("missing supply, input or recommendation example")
	}
	return document.Examples
}

func TestCompletionExamplesMatchTheStrictProfile(t *testing.T) {
	for _, example := range completionExamples(t) {
		t.Run(example.Name, func(t *testing.T) {
			if err := completionSchema(t, example.Schema).Validate(example.Value); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompletionRejectsMalformedQuantitiesDecimalsAndExtraFields(t *testing.T) {
	cases := []struct {
		name, schema string
		value        map[string]any
	}{
		{"quantity-overflow", "FINSupplyRow", map[string]any{"suggestedQuantity": "9223372036854775808"}},
		{"quantity-number", "FINSupplyRow", map[string]any{"suggestedQuantity": 1}},
		{"coverage-NaN", "FINSupplyRow", map[string]any{"coverageDays": "NaN"}},
		{"mean-exponent", "FINSupplyRow", map[string]any{"averageDailyBaseUnits": "1e3"}},
		{"unexpected-supplier", "FINSupplyRow", map[string]any{"supplier": "unapproved"}},
		{"score-infinity", "FINRecommendation", map[string]any{"score": "Infinity"}},
		{"score-negative", "FINRecommendation", map[string]any{"score": "-0.100000"}},
	}
	examples := completionExamples(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			index := 0
			if tc.schema == "FINRecommendation" {
				index = 2
			}
			result, ok := examples[index].Value["result"].(map[string]any)
			if !ok {
				t.Fatal("missing example result")
			}
			items, ok := result["items"].([]any)
			if !ok || len(items) == 0 {
				t.Fatal("missing example items")
			}
			original, ok := items[0].(map[string]any)
			if !ok {
				t.Fatal("malformed example row")
			}
			candidate := make(map[string]any, len(original)+1)
			for key, value := range original {
				candidate[key] = value
			}
			for key, value := range tc.value {
				candidate[key] = value
			}
			if err := completionSchema(t, tc.schema).Validate(candidate); err == nil {
				t.Fatal("strict FIN1 schema accepted malformed data")
			}
		})
	}
}

func TestCompletionCompositionIsReproducibleAndPreservesInputs(t *testing.T) {
	directory := t.TempDir()
	inputs := []string{"ui-amendment-v1-composed-public.json", "ui-amendment-v1-composed-private.json", "completion-v1-public.json", "completion-v1-private.json"}
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
	script := filepath.Join(completionAPI(t), "../../tests/http/tools/compose-completion-contract.mjs")
	command := exec.CommandContext(ctx, "node", script, directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compose FIN1: %v: %s", err, output)
	}
	for _, name := range inputs {
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || sha256.Sum256(body) != before[name] {
			t.Fatal("composition modified a predecessor/input", name, err)
		}
	}
	for _, name := range []string{"completion-v1-composed-public.json", "completion-v1-composed-private.json"} {
		actual, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		committed, err := os.ReadFile(filepath.Join(completionAPI(t), name))
		if err != nil || !bytes.Equal(actual, committed) {
			t.Fatal("composed FIN1 profile differs from its inputs", name, err)
		}
		var document struct {
			Sources map[string]string `json:"x-fin-source"`
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
