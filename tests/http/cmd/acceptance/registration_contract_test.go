package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func registrationDocument(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(completionAPI(t), name))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func registrationSchema(t *testing.T, visibility, name string) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("registration.json", registrationDocument(t, "registration-v1-composed-"+visibility+".json")); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("registration.json#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}
func TestRegistrationExamplesAndStrictSchemas(t *testing.T) {
	doc := registrationDocument(t, "registration-v1-examples.json")
	examples, ok := doc["examples"].([]any)
	if !ok || len(examples) < 4 {
		t.Fatal("missing registration and legacy examples")
	}
	for _, visibility := range []string{"public", "private"} {
		for _, entry := range examples {
			example, ok := entry.(map[string]any)
			if !ok {
				t.Fatal("invalid example")
			}
			name, ok := example["schema"].(string)
			if !ok {
				t.Fatal("example schema missing")
			}
			if err := registrationSchema(t, visibility, name).Validate(example["value"]); err != nil {
				t.Fatal(err)
			}
		}
		valid := map[string]any{"commandID": "register", "challengeID": "00000000-0000-4000-8000-000000000001", "proof": "01234567890123456789012345678901", "purpose": "registration", "displayName": "New User", "password": "valid-password"}
		for _, mutation := range []map[string]any{{"newPassword": "forbidden-password"}, {"rememberMe": "true"}, {"password": "short"}, {"displayName": "x"}, {"purpose": "login"}, {"unexpected": true}, {"password": nil}} {
			candidate := make(map[string]any)
			for k, v := range valid {
				candidate[k] = v
			}
			for k, v := range mutation {
				candidate[k] = v
			}
			if err := registrationSchema(t, visibility, "EmailChallengeComplete").Validate(candidate); err == nil {
				t.Fatalf("accepted malformed registration: %v", mutation)
			}
		}
		for _, missing := range []string{"displayName", "password", "proof", "challengeID", "commandID"} {
			candidate := make(map[string]any)
			for k, v := range valid {
				if k != missing {
					candidate[k] = v
				}
			}
			if err := registrationSchema(t, visibility, "EmailChallengeComplete").Validate(candidate); err == nil {
				t.Fatal("accepted missing", missing)
			}
		}
	}
}
func TestRegistrationCompositionPreservesPredecessor(t *testing.T) {
	directory := t.TempDir()
	inputs := []string{"completion-v1-composed-public.json", "completion-v1-composed-private.json", "registration-v1-public.json", "registration-v1-private.json"}
	before := map[string][]byte{}
	for _, name := range inputs {
		b, err := os.ReadFile(filepath.Join(completionAPI(t), name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = b
		if err := os.WriteFile(filepath.Join(directory, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(completionAPI(t), "../../tests/http/tools/compose-registration-contract.mjs"), directory)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("composition: %v: %s", err, out)
	}
	for _, name := range inputs {
		b, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || !bytes.Equal(before[name], b) {
			t.Fatal("input changed", name, err)
		}
	}
	for _, visibility := range []string{"public", "private"} {
		name := "registration-v1-composed-" + visibility + ".json"
		actual, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		expected, err := os.ReadFile(filepath.Join(completionAPI(t), name))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatal("composition is not reproducible", err)
		}
		predecessor := registrationDocument(t, "completion-v1-composed-"+visibility+".json")
		composed := registrationDocument(t, name)
		if !reflect.DeepEqual(predecessor["paths"], composed["paths"]) {
			t.Fatal("registration changed existing operations")
		}
		components, ok := predecessor["components"].(map[string]any)
		if !ok {
			t.Fatal("missing components")
		}
		schemas, ok := components["schemas"].(map[string]any)
		if !ok {
			t.Fatal("missing schemas")
		}
		nextComponents, ok := composed["components"].(map[string]any)
		if !ok {
			t.Fatal("missing next components")
		}
		next, ok := nextComponents["schemas"].(map[string]any)
		if !ok {
			t.Fatal("missing next schemas")
		}
		if len(schemas) != len(next) {
			t.Fatal("unexpected schemas")
		}
		for key, value := range schemas {
			changed := !reflect.DeepEqual(value, next[key])
			if changed != (key == "ClientCommandID" || key == "EmailChallengeBegin" || key == "EmailChallengeComplete") {
				t.Fatal("unexpected schema change", key)
			}
		}
	}
}

func TestRegistrationCommandAndLegacyCompletionBounds(t *testing.T) {
	for _, visibility := range []string{"public", "private"} {
		schema := registrationSchema(t, visibility, "EmailChallengeComplete")
		base := map[string]any{"commandID": "valid", "challengeID": "00000000-0000-4000-8000-000000000001", "proof": "proof", "purpose": "login"}
		if err := schema.Validate(base); err != nil {
			t.Fatal(err)
		}
		for _, change := range []map[string]any{{"commandID": strings.Repeat("a", 257)}, {"purpose": "foo"}, {"newPassword": "password"}, {"purpose": "password_reset"}} {
			v := map[string]any{}
			for key, value := range base {
				v[key] = value
			}
			for key, value := range change {
				v[key] = value
			}
			if schema.Validate(v) == nil {
				t.Fatal("invalid completion admitted")
			}
		}
		base["purpose"] = "password_reset"
		base["newPassword"] = "password"
		if err := schema.Validate(base); err != nil {
			t.Fatal(err)
		}
		base["rememberMe"] = false
		if schema.Validate(base) == nil {
			t.Fatal("reset admitted rememberMe")
		}
	}
}
