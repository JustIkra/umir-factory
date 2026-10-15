package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The declared DUR1 change set. The same names are fixed in
// tests/http/tools/compose-durable-contract.mjs; a change to either list
// is a contract change and must be reviewed on both sides.
var (
	durableReplacedSchemas = []string{
		"AuthPageIdentity", "DueExpiryResult", "DurableResult", "Epoch", "Error", "ID",
		"IdempotencyResult", "LiveEvent", "PrepareOrderRequest", "ReadinessAck", "Registry",
		"RuntimeReadiness", "RuntimeStatus", "ServiceAuthorizationRequest", "ServiceClaims", "WOrderAttempt",
	}
	durableAddedSchemas = []string{
		"ExpiryDueList", "ExpiryDueRestaurant", "HealthReadiness", "RegistryPoint", "RegistryTombstone",
	}
	durableNewOperations = []string{
		"GET /healthz", "GET /internal/auth/v1/registry/{restaurantID}",
		"GET /internal/warehouse/v2/expiry-due", "GET /readyz",
	}
	// Existing operations whose listed top-level fields DUR1 sets. Everything
	// else in those operations must stay byte-for-byte equivalent.
	durablePatchedOperations = map[string][]string{
		"GET /internal/auth/v1/registry":                        {"description", "parameters", "responses"},
		"GET /internal/gateway/v2/runtime":                      {"deprecated", "x-umir-removal-gate"},
		"PUT /internal/gateway/v2/r/{restaurantID}/registry":    {"deprecated", "x-umir-removal-gate"},
		"DELETE /internal/gateway/v2/r/{restaurantID}/registry": {"deprecated", "x-umir-removal-gate"},
		"POST /internal/auth/v1/authorize":                      {"description"},
		"POST /internal/auth/v1/access-check":                   {"description"},
		"POST /internal/auth/v1/service-tokens":                 {"description"},
	}
	durableDeprecatedOperations = []string{
		"GET /internal/gateway/v2/runtime",
		"PUT /internal/gateway/v2/r/{restaurantID}/registry",
		"DELETE /internal/gateway/v2/r/{restaurantID}/registry",
	}
	durableInputs = []string{
		"registration-v1-composed-public.json", "registration-v1-composed-private.json",
		"durable-v1-public.json", "durable-v1-private.json",
	}
)

func durableSchema(t *testing.T, visibility, name string) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("durable.json", registrationDocument(t, "durable-v1-composed-"+visibility+".json")); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("durable.json#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

type durableExample struct {
	Name    string            `json:"name"`
	Schema  string            `json:"schema"`
	Value   any               `json:"value"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}

func durableExamples(t *testing.T) []durableExample {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(completionAPI(t), "durable-v1-examples.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Profile  string           `json:"profile"`
		Examples []durableExample `json:"examples"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if document.Profile != "DUR1" {
		t.Fatal("examples profile is not DUR1")
	}
	return document.Examples
}

// durableOperations indexes "METHOD /path" to the operation object.
func durableOperations(t *testing.T, document map[string]any) map[string]map[string]any {
	t.Helper()
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatal("missing paths")
	}
	result := map[string]map[string]any{}
	for route, item := range paths {
		methods, ok := item.(map[string]any)
		if !ok {
			t.Fatal("invalid path item", route)
		}
		for method, operation := range methods {
			if method == "parameters" {
				continue
			}
			object, ok := operation.(map[string]any)
			if !ok {
				t.Fatal("invalid operation", route, method)
			}
			result[strings.ToUpper(method)+" "+route] = object
		}
	}
	return result
}

func durableSchemas(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	components, ok := document["components"].(map[string]any)
	if !ok {
		t.Fatal("missing components")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("missing schemas")
	}
	return schemas
}

func durableClone(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(body, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func durableMap(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected an object, got %T", value)
	}
	return object
}

func durableSorted(values []string) []string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	return copied
}

func durableRun(t *testing.T, directory string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	script := filepath.Join(completionAPI(t), "../../tests/http/tools/compose-durable-contract.mjs")
	return exec.CommandContext(ctx, "node", script, directory).CombinedOutput()
}

func durableCopyInputs(t *testing.T) (string, map[string][]byte) {
	t.Helper()
	directory := t.TempDir()
	before := map[string][]byte{}
	for _, name := range durableInputs {
		body, err := os.ReadFile(filepath.Join(completionAPI(t), name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = body
		if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return directory, before
}

func TestDurableExamplesAndStrictSchemas(t *testing.T) {
	examples := durableExamples(t)
	seen := map[string]bool{}
	for _, visibility := range []string{"public", "private"} {
		for _, example := range examples {
			if example.Name == "" || example.Schema == "" {
				t.Fatal("example without name or schema")
			}
			if err := durableSchema(t, visibility, example.Schema).Validate(example.Value); err != nil {
				t.Fatalf("%s/%s: %v", visibility, example.Name, err)
			}
			seen[example.Schema] = true
			if example.Status == 429 {
				value, ok := example.Value.(map[string]any)
				if !ok {
					t.Fatal("invalid 429 example", example.Name)
				}
				details, ok := value["details"].(map[string]any)
				if !ok {
					t.Fatal("429 example without details", example.Name)
				}
				seconds, ok := details["retryAfterSeconds"].(float64)
				if !ok || example.Headers["Retry-After"] != fmt.Sprint(int(seconds)) {
					t.Fatal("429 example needs Retry-After equal to details.retryAfterSeconds", example.Name)
				}
			}
		}
	}
	for _, name := range append(append([]string(nil), durableAddedSchemas...), "WOrderAttempt", "DueExpiryResult", "Registry", "ServiceAuthorizationRequest", "ServiceClaims", "Error") {
		if !seen[name] {
			t.Fatal("no example for", name)
		}
	}
	type errorShape struct {
		code, outcome string
		status        int
	}
	found := map[errorShape]bool{}
	for _, example := range examples {
		if example.Schema != "Error" {
			continue
		}
		value, ok := example.Value.(map[string]any)
		if !ok {
			t.Fatal("invalid error example")
		}
		found[errorShape{fmt.Sprint(value["code"]), fmt.Sprint(value["outcome"]), example.Status}] = true
	}
	for _, want := range []errorShape{
		{"CommandConflict", "unknown", 409},
		{"LimitExceeded", "not_committed", 409},
		{"InvalidArgument", "not_committed", 400},
		{"RateLimited", "not_committed", 429},
		{"ServiceUnavailable", "not_committed", 503},
		{"ServiceUnavailable", "unavailable", 503},
	} {
		if !found[want] {
			t.Fatalf("missing error example %+v", want)
		}
	}

	const restaurant = "33333333-3333-4333-8333-333333333333"
	const org = "22222222-2222-4222-8222-222222222222"
	const epoch = "44444444-4444-4444-8444-444444444444"
	entry := map[string]any{
		"restaurant": map[string]any{"restaurantID": restaurant, "orgID": org, "name": "Cafe", "lifecycle": "ready", "lifecycleVersion": "2"},
		"settings":   map[string]any{"timeZone": "Europe/Moscow", "settingsVersion": "1"},
		"services":   []any{},
	}
	tombstone := map[string]any{"restaurantID": restaurant, "orgID": org, "lifecycleVersion": "3"}
	positions := func(n int) []any {
		items := make([]any, n)
		for i := range items {
			items[i] = map[string]any{"dishID": fmt.Sprintf("dish-%d", i), "portions": "1"}
		}
		return items
	}
	rejects := []struct {
		name, schema string
		value        any
	}{
		{"id-257", "ID", strings.Repeat("a", 257)},
		{"id-empty", "ID", ""},
		{"command-id-257", "ClientCommandID", strings.Repeat("a", 257)},
		{"command-id-expiration", "ClientCommandID", "expiration/x"},
		{"positions-201", "PrepareOrderRequest", map[string]any{"positions": positions(201)}},
		{"attempt-abandoned", "WOrderAttempt", map[string]any{"state": "Abandoned"}},
		{"attempt-interrupted-extra", "WOrderAttempt", map[string]any{"state": "Interrupted", "result": map[string]any{}}},
		{"status-gateway", "RuntimeStatus", map[string]any{"service": "gateway", "epoch": epoch, "ready": true, "tzdataVersion": "2026a"}},
		{"readiness-gateway", "RuntimeReadiness", map[string]any{"restaurantID": restaurant, "service": "gateway", "epoch": epoch, "state": "ready", "lifecycleVersion": "1"}},
		{"ack-gateway", "ReadinessAck", map[string]any{"restaurantID": restaurant, "service": "gateway", "epoch": epoch, "state": "ready", "lifecycleVersion": "1"}},
		{"rate-limited-without-retry-after", "Error", map[string]any{"code": "RateLimited", "outcome": "not_committed", "message": "slow down", "retryable": true}},
		{"rate-limited-committed", "Error", map[string]any{"code": "RateLimited", "outcome": "committed", "message": "slow down", "retryable": true, "details": map[string]any{"retryAfterSeconds": 3}}},
		{"retry-after-zero", "Error", map[string]any{"code": "RateLimited", "outcome": "not_committed", "message": "slow down", "retryable": true, "details": map[string]any{"retryAfterSeconds": 0}}},
		{"admission-code", "Error", map[string]any{"code": "AdmissionUnavailable", "outcome": "not_committed", "message": "x", "retryable": true}},
		{"registry-incomplete-without-cursor", "Registry", map[string]any{"registryRevision": "5", "complete": false, "entries": []any{}, "tombstones": []any{}}},
		{"registry-complete-with-cursor", "Registry", map[string]any{"registryRevision": "5", "complete": true, "nextAfter": restaurant, "entries": []any{}, "tombstones": []any{}}},
		{"registry-1001-entries", "Registry", map[string]any{"registryRevision": "5", "complete": true, "entries": func() []any {
			items := make([]any, 1001)
			for i := range items {
				items[i] = entry
			}
			return items
		}(), "tombstones": []any{}}},
		{"point-live-without-entry", "RegistryPoint", map[string]any{"registryRevision": "5", "state": "live"}},
		{"point-live-with-tombstone", "RegistryPoint", map[string]any{"registryRevision": "5", "state": "live", "entry": entry, "tombstone": tombstone}},
		{"point-tombstone-with-entry", "RegistryPoint", map[string]any{"registryRevision": "5", "state": "tombstone", "entry": entry}},
		{"point-status", "RegistryPoint", map[string]any{"registryRevision": "5", "state": "live", "entry": entry, "status": "ready"}},
		{"cell-token-with-restaurant", "ServiceAuthorizationRequest", map[string]any{"operation": "expiry.due", "restaurantID": restaurant}},
		{"order-resolve-without-restaurant", "ServiceAuthorizationRequest", map[string]any{"operation": "order.resolve", "epoch": epoch}},
		{"order-resolve-without-epoch", "ServiceAuthorizationRequest", map[string]any{"operation": "order.resolve", "restaurantID": restaurant}},
		{"unknown-service-operation", "ServiceAuthorizationRequest", map[string]any{"operation": "order.commit", "restaurantID": restaurant, "epoch": epoch}},
		{"health-unknown-state", "HealthReadiness", map[string]any{"capabilities": map[string]any{"static": "unknown"}}},
		{"health-no-capabilities", "HealthReadiness", map[string]any{"capabilities": map[string]any{}}},
		{"health-extra", "HealthReadiness", map[string]any{"capabilities": map[string]any{"static": "ok"}, "build": "x"}},
		{"due-without-epoch", "ExpiryDueList", map[string]any{"asOf": "2026-10-15T10:00:00Z", "items": []any{}}},
		{"due-restaurant-extra", "ExpiryDueRestaurant", map[string]any{"restaurantID": restaurant, "nextDueAt": "2026-10-15T10:00:00Z", "orgID": org}},
		{"expiry-result-next-due-not-instant", "DueExpiryResult", map[string]any{"transitionedBatchIDs": []any{}, "finalWriteoffs": 0, "committedVersion": "1", "invalidationRequired": false, "nextDueAt": "tomorrow"}},
	}
	accepts := []struct {
		name, schema string
		value        any
	}{
		{"id-256", "ID", strings.Repeat("я", 256)},
		{"command-id-256", "ClientCommandID", strings.Repeat("a", 256)},
		{"positions-200", "PrepareOrderRequest", map[string]any{"positions": positions(200)}},
		{"positions-0", "PrepareOrderRequest", map[string]any{"positions": []any{}}},
		{"attempt-pending", "WOrderAttempt", map[string]any{"state": "Pending"}},
		{"attempt-interrupted", "WOrderAttempt", map[string]any{"state": "Interrupted"}},
		{"health-frontend", "HealthReadiness", map[string]any{"capabilities": map[string]any{"static": "ok"}}},
		{"registry-1000-entries", "Registry", map[string]any{"registryRevision": "5", "complete": true, "entries": func() []any {
			items := make([]any, 1000)
			for i := range items {
				items[i] = entry
			}
			return items
		}(), "tombstones": []any{}}},
	}
	for _, visibility := range []string{"public", "private"} {
		for _, c := range rejects {
			if err := durableSchema(t, visibility, c.schema).Validate(c.value); err == nil {
				t.Fatalf("%s accepted malformed %s", visibility, c.name)
			}
		}
		for _, c := range accepts {
			if err := durableSchema(t, visibility, c.schema).Validate(c.value); err != nil {
				t.Fatalf("%s rejected valid %s: %v", visibility, c.name, err)
			}
		}
	}
}

func TestDurableCompositionPreservesPredecessor(t *testing.T) {
	directory, before := durableCopyInputs(t)
	if out, err := durableRun(t, directory); err != nil {
		t.Fatalf("composition: %v: %s", err, out)
	}
	for _, name := range durableInputs {
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || !bytes.Equal(before[name], body) {
			t.Fatal("input changed", name, err)
		}
	}
	for _, visibility := range []string{"public", "private"} {
		predecessor := registrationDocument(t, "registration-v1-composed-"+visibility+".json")
		composed := registrationDocument(t, "durable-v1-composed-"+visibility+".json")
		overlay := registrationDocument(t, "durable-v1-"+visibility+".json")

		oldOperations := durableOperations(t, predecessor)
		newOperations := durableOperations(t, composed)
		var added, changed []string
		for key := range newOperations {
			if _, ok := oldOperations[key]; !ok {
				added = append(added, key)
			}
		}
		for key, old := range oldOperations {
			next, ok := newOperations[key]
			if !ok {
				t.Fatal("DUR1 removed an operation", key)
			}
			if !reflect.DeepEqual(old, next) {
				changed = append(changed, key)
				allowed, declared := durablePatchedOperations[key]
				if !declared {
					t.Fatal("undeclared operation change", key)
				}
				// Outside the declared fields the operation is the predecessor's.
				for _, field := range allowed {
					delete(old, field)
					delete(next, field)
				}
				if !reflect.DeepEqual(old, next) {
					t.Fatal("operation changed outside its declared fields", key)
				}
			}
		}
		wantAdded := durableNewOperations
		wantChanged := make([]string, 0, len(durablePatchedOperations))
		for key := range durablePatchedOperations {
			wantChanged = append(wantChanged, key)
		}
		if visibility == "public" {
			wantAdded, wantChanged = nil, nil
		}
		if !reflect.DeepEqual(durableSorted(added), durableSorted(wantAdded)) {
			t.Fatalf("%s: added operations %v, want %v", visibility, durableSorted(added), durableSorted(wantAdded))
		}
		if !reflect.DeepEqual(durableSorted(changed), durableSorted(wantChanged)) {
			t.Fatalf("%s: changed operations %v, want %v", visibility, durableSorted(changed), durableSorted(wantChanged))
		}

		oldSchemas := durableSchemas(t, predecessor)
		newSchemas := durableSchemas(t, composed)
		if len(newSchemas) != len(oldSchemas)+len(durableAddedSchemas) {
			t.Fatal("unexpected number of schemas", len(oldSchemas), len(newSchemas))
		}
		var replaced, introduced []string
		for key, value := range newSchemas {
			old, existed := oldSchemas[key]
			switch {
			case !existed:
				introduced = append(introduced, key)
			case !reflect.DeepEqual(old, value):
				replaced = append(replaced, key)
			}
		}
		if !reflect.DeepEqual(durableSorted(replaced), durableSorted(durableReplacedSchemas)) {
			t.Fatalf("replaced schemas %v, want %v", durableSorted(replaced), durableSorted(durableReplacedSchemas))
		}
		if !reflect.DeepEqual(durableSorted(introduced), durableSorted(durableAddedSchemas)) {
			t.Fatalf("added schemas %v, want %v", durableSorted(introduced), durableSorted(durableAddedSchemas))
		}
		replacements, ok := overlay["x-schema-replacements"].(map[string]any)
		if !ok {
			t.Fatal("overlay without replacements")
		}
		for name, schema := range replacements {
			if !reflect.DeepEqual(schema, newSchemas[name]) {
				t.Fatal("composed schema differs from overlay", name)
			}
		}
		// Every other top-level member survives; only info and the DUR1
		// extensions are new or replaced.
		for key, value := range predecessor {
			if key == "info" || key == "paths" || key == "components" {
				continue
			}
			if !reflect.DeepEqual(value, composed[key]) {
				t.Fatal("top-level member changed", key)
			}
		}
		oldComponents := durableMap(t, predecessor["components"])
		newComponents := durableMap(t, composed["components"])
		for key, value := range oldComponents {
			if key != "schemas" && !reflect.DeepEqual(value, newComponents[key]) {
				t.Fatal("component group changed", key)
			}
		}
		if len(oldComponents) != len(newComponents) {
			t.Fatal("component groups changed")
		}
	}
}

func TestDurableCompositionRejectsUndeclaredChanges(t *testing.T) {
	mutate := func(name string, edit func(map[string]any)) {
		directory, _ := durableCopyInputs(t)
		path := filepath.Join(directory, name)
		var document map[string]any
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &document); err != nil {
			t.Fatal(err)
		}
		edit(document)
		body, err = json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := durableRun(t, directory); err == nil {
			t.Fatalf("composition accepted %s: %s", name, out)
		}
	}
	mutate("registration-v1-composed-private.json", func(d map[string]any) {
		durableMap(t, d["info"])["version"] = "2.0.0-FIN.1"
	})
	mutate("durable-v1-private.json", func(d map[string]any) {
		durableMap(t, d["x-schema-replacements"])["Product"] = map[string]any{"type": "object"}
	})
	mutate("durable-v1-private.json", func(d map[string]any) {
		delete(durableMap(t, d["x-schema-replacements"]), "ID")
	})
	mutate("durable-v1-private.json", func(d map[string]any) {
		durableMap(t, d["paths"])["/internal/auth/v1/jwks"] = map[string]any{"get": map[string]any{"operationId": "jwks"}}
	})
	mutate("durable-v1-private.json", func(d map[string]any) {
		durableMap(t, durableMap(t, d["x-operation-patches"])["GET /internal/gateway/v2/runtime"])["description"] = "changed"
	})
	mutate("durable-v1-private.json", func(d map[string]any) {
		durableMap(t, d["x-operation-patches"])["GET /internal/auth/v1/jwks"] = map[string]any{"deprecated": true}
	})
	mutate("durable-v1-public.json", func(d map[string]any) {
		durableMap(t, durableMap(t, d["components"])["schemas"])["Epoch"] = map[string]any{"type": "string"}
	})
	mutate("durable-v1-public.json", func(d map[string]any) {
		d["x-durable-policy"] = map[string]any{"other": true}
	})
}

func TestDurableCompositionIsReproducible(t *testing.T) {
	directory, before := durableCopyInputs(t)
	if out, err := durableRun(t, directory); err != nil {
		t.Fatalf("composition: %v: %s", err, out)
	}
	digests := map[string]string{}
	for name, body := range before {
		sum := sha256.Sum256(body)
		digests[name] = hex.EncodeToString(sum[:])
	}
	for _, visibility := range []string{"public", "private"} {
		name := "durable-v1-composed-" + visibility + ".json"
		actual, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		committed, err := os.ReadFile(filepath.Join(completionAPI(t), name))
		if err != nil || !bytes.Equal(actual, committed) {
			t.Fatal("composition is not reproducible", name, err)
		}
		var document struct {
			Info    struct{ Version string }
			Sources map[string]string `json:"x-durable-source"`
			Policy  map[string]any    `json:"x-durable-policy"`
			Chain   map[string]any    `json:"x-registration-source"`
		}
		if err := json.Unmarshal(actual, &document); err != nil {
			t.Fatal(err)
		}
		// The normative text records the digest of every committed profile and input.
		amendment, err := os.ReadFile(filepath.Join(completionAPI(t), "../durable-api-amendment.md"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(committed)
		if !bytes.Contains(amendment, []byte(hex.EncodeToString(sum[:]))) {
			t.Fatal("docs/durable-api-amendment.md does not record the digest of", name)
		}
		for input, digest := range digests {
			if !bytes.Contains(amendment, []byte(digest)) {
				t.Fatal("docs/durable-api-amendment.md does not record the digest of", input)
			}
		}
		if document.Info.Version != "2.0.0-DUR.1" {
			t.Fatal("unexpected version", document.Info.Version)
		}
		if !reflect.DeepEqual(document.Sources, digests) {
			t.Fatalf("profile lost its exact source identity: %v", document.Sources)
		}
		if len(document.Policy) == 0 || len(document.Chain) == 0 {
			t.Fatal("profile lost its policy or the REG1 source chain")
		}
		// The same script run twice produces the same bytes.
		if out, err := durableRun(t, directory); err != nil {
			t.Fatalf("second composition: %v: %s", err, out)
		}
		again, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || !bytes.Equal(again, actual) {
			t.Fatal("composition is not idempotent", name, err)
		}
	}
}

func TestDurableDeprecatedGatewayOperationsAreOnlyMarked(t *testing.T) {
	predecessor := durableOperations(t, registrationDocument(t, "registration-v1-composed-private.json"))
	composed := durableOperations(t, registrationDocument(t, "durable-v1-composed-private.json"))
	deprecated := map[string]bool{}
	for _, key := range durableDeprecatedOperations {
		deprecated[key] = true
	}
	for key, operation := range composed {
		if operation["deprecated"] == true && !deprecated[key] {
			t.Fatal("unexpected deprecated operation", key)
		}
	}
	for _, key := range durableDeprecatedOperations {
		old, next := durableClone(t, predecessor[key]), durableClone(t, composed[key])
		if old == nil || next == nil {
			t.Fatal("missing gateway operation", key)
		}
		if next["deprecated"] != true {
			t.Fatal("not deprecated", key)
		}
		gate, ok := next["x-umir-removal-gate"].(map[string]any)
		if !ok || gate["removeIn"] != "2.0.0-DUR.2" || gate["order"] != "C7" {
			t.Fatal("removal gate missing or wrong", key)
		}
		steps, ok := gate["requires"].([]any)
		if !ok || len(steps) != 3 {
			t.Fatal("removal gate must name the three preceding steps", key)
		}
		delete(next, "deprecated")
		delete(next, "x-umir-removal-gate")
		if !reflect.DeepEqual(old, next) {
			t.Fatal("deprecation changed more than the markers", key)
		}
	}
	// The public profile has no gateway operation to mark.
	for key, operation := range durableOperations(t, registrationDocument(t, "durable-v1-composed-public.json")) {
		if operation["deprecated"] == true || operation["x-umir-removal-gate"] != nil {
			t.Fatal("public profile marks an operation", key)
		}
	}
	// Narrowed enums leave no gateway peer in runtime schemas.
	schemas := durableSchemas(t, registrationDocument(t, "durable-v1-composed-private.json"))
	for _, name := range []string{"RuntimeStatus", "RuntimeReadiness", "ReadinessAck"} {
		body, err := json.Marshal(schemas[name])
		if err != nil || strings.Contains(string(body), "gateway") {
			t.Fatal("gateway remains in", name, err)
		}
		enum := durableMap(t, durableMap(t, durableMap(t, durableMap(t, schemas[name])["properties"])["service"]))["enum"]
		if !reflect.DeepEqual(enum, []any{"warehouse", "serving"}) {
			t.Fatal("service enum is not [warehouse, serving]", name, enum)
		}
	}
}
