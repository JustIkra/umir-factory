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
	"regexp"
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
		"RegistryEntry", "RuntimeReadiness", "RuntimeStatus", "ServiceAuthorizationRequest", "ServiceClaims", "WOrderAttempt",
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
	durablePatchedOperations = map[string]map[string][]string{
		"private": {
			"GET /internal/auth/v1/registry":                        {"description", "parameters", "responses"},
			"GET /internal/gateway/v2/runtime":                      {"deprecated", "x-umir-removal-gate"},
			"PUT /internal/gateway/v2/r/{restaurantID}/registry":    {"deprecated", "x-umir-removal-gate"},
			"DELETE /internal/gateway/v2/r/{restaurantID}/registry": {"deprecated", "x-umir-removal-gate"},
			"POST /internal/auth/v1/authorize":                      {"description"},
			"POST /internal/auth/v1/access-check":                   {"description"},
			"POST /internal/auth/v1/service-tokens":                 {"description"},
		},
		"public": {
			"GET /api/v2/r/{restaurantID}/live": {"responses"},
		},
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
	for _, name := range append(append([]string(nil), durableAddedSchemas...), "RegistryEntry", "WOrderAttempt", "DueExpiryResult", "Registry", "ServiceAuthorizationRequest", "ServiceClaims", "Error") {
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
		{"CommandConflict", "not_committed", 409},
		{"CommandConflict", "unavailable", 409},
		{"NotFound", "unavailable", 404},
		{"UpstreamTimeout", "unknown", 504},
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
		{"health-name-two-dots", "HealthReadiness", map[string]any{"capabilities": map[string]any{"delivery.smtp.x": "ok"}}},
		{"health-name-trailing-dot", "HealthReadiness", map[string]any{"capabilities": map[string]any{"delivery.": "ok"}}},
		{"health-name-upper", "HealthReadiness", map[string]any{"capabilities": map[string]any{"Delivery.smtp": "ok"}}},
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
				allowed, declared := durablePatchedOperations[visibility][key]
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
		wantChanged := make([]string, 0, len(durablePatchedOperations[visibility]))
		for key := range durablePatchedOperations[visibility] {
			wantChanged = append(wantChanged, key)
		}
		if visibility == "public" {
			wantAdded = nil
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
	// Each mutation must fail for the stated reason, not for any reason.
	mutateAll := func(names []string, reason string, edit func(map[string]any)) {
		t.Helper()
		directory, _ := durableCopyInputs(t)
		for _, name := range names {
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
		}
		out, err := durableRun(t, directory)
		if err == nil {
			t.Fatalf("composition accepted %q: %s", reason, out)
		}
		if !strings.Contains(string(out), reason) {
			t.Fatalf("composition failed for another reason than %q: %s", reason, out)
		}
	}
	mutate := func(name, reason string, edit func(map[string]any)) {
		t.Helper()
		mutateAll([]string{name}, reason, edit)
	}
	const privateOverlay, publicOverlay = "durable-v1-private.json", "durable-v1-public.json"
	patches := func(d map[string]any) map[string]any { return durableMap(t, d["x-operation-patches"]) }
	registry := func(d map[string]any) map[string]any {
		return durableMap(t, patches(d)["GET /internal/auth/v1/registry"])
	}
	registryResponses := func(d map[string]any) map[string]any { return durableMap(t, registry(d)["responses"]) }
	newOperation := func(d map[string]any, route string) map[string]any {
		return durableMap(t, durableMap(t, durableMap(t, d["paths"])[route])["get"])
	}
	live := func(d map[string]any) map[string]any {
		return durableMap(t, durableMap(t, patches(d)["GET /api/v2/r/{restaurantID}/live"])["responses"])
	}

	mutate("registration-v1-composed-private.json", "Expected REG1 predecessor", func(d map[string]any) {
		durableMap(t, d["info"])["version"] = "2.0.0-FIN.1"
	})
	both := []string{publicOverlay, privateOverlay}
	mutateAll(both, "Unexpected DUR1 replacements", func(d map[string]any) {
		durableMap(t, d["x-schema-replacements"])["Product"] = map[string]any{"type": "object"}
	})
	mutateAll(both, "Unexpected DUR1 replacements", func(d map[string]any) {
		delete(durableMap(t, d["x-schema-replacements"]), "ID")
	})
	mutate(privateOverlay, "Different public/private DUR1 x-schema-replacements", func(d map[string]any) {
		delete(durableMap(t, d["x-schema-replacements"]), "ID")
	})
	mutate(privateOverlay, "Unexpected private DUR1 operations", func(d map[string]any) {
		durableMap(t, d["paths"])["/internal/auth/v1/jwks"] = map[string]any{"get": map[string]any{"operationId": "jwks"}}
	})
	mutate(privateOverlay, "Unexpected fields of GET /internal/gateway/v2/runtime", func(d map[string]any) {
		durableMap(t, patches(d)["GET /internal/gateway/v2/runtime"])["description"] = "changed"
	})
	mutate(privateOverlay, "Unexpected private DUR1 operation patches", func(d map[string]any) {
		patches(d)["GET /internal/auth/v1/jwks"] = map[string]any{"deprecated": true}
	})
	mutate(publicOverlay, "Different public/private DUR1 schemas", func(d map[string]any) {
		durableMap(t, durableMap(t, d["components"])["schemas"])["Epoch"] = map[string]any{"type": "string"}
	})
	mutate(publicOverlay, "Different public/private DUR1 x-durable-policy", func(d map[string]any) {
		d["x-durable-policy"] = map[string]any{"other": true}
	})
	mutate(privateOverlay, "Unexpected overlay key x-extra", func(d map[string]any) { d["x-extra"] = true })
	mutate(publicOverlay, "Unexpected overlay key servers", func(d map[string]any) { d["servers"] = []any{} })

	// Patches of the registry operation only add to the predecessor.
	mutate(privateOverlay, "Empty responses of GET /internal/auth/v1/registry", func(d map[string]any) {
		registry(d)["responses"] = map[string]any{}
	})
	mutate(privateOverlay, "drops response default", func(d map[string]any) {
		delete(registryResponses(d), "default")
	})
	mutate(privateOverlay, "drops response 200", func(d map[string]any) {
		delete(registryResponses(d), "200")
	})
	mutate(privateOverlay, "changes the content of response 200", func(d map[string]any) {
		durableMap(t, registryResponses(d)["200"])["content"] = map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}}}
	})
	mutate(privateOverlay, "changes the default response", func(d map[string]any) {
		durableMap(t, registryResponses(d)["default"])["description"] = "changed"
	})
	mutate(privateOverlay, "Unexpected responses added to GET /internal/auth/v1/registry", func(d map[string]any) {
		registryResponses(d)["500"] = map[string]any{"description": "extra"}
	})
	mutate(privateOverlay, "Empty description of GET /internal/auth/v1/registry", func(d map[string]any) {
		registry(d)["description"] = " "
	})
	mutate(privateOverlay, "repeats a parameter", func(d map[string]any) {
		parameters := durableList(t, registry(d)["parameters"])
		registry(d)["parameters"] = append(parameters, parameters[0])
	})
	mutate(privateOverlay, "Incomplete removal gate", func(d map[string]any) {
		gate := durableMap(t, durableMap(t, patches(d)["GET /internal/gateway/v2/runtime"])["x-umir-removal-gate"])
		gate["requires"] = []any{"one"}
	})

	// New operations keep a complete form and do not open themselves.
	mutate(privateOverlay, "must not open itself with empty security", func(d map[string]any) {
		newOperation(d, "/internal/warehouse/v2/expiry-due")["security"] = []any{}
	})
	mutate(privateOverlay, "Incomplete new operation GET /internal/warehouse/v2/expiry-due", func(d map[string]any) {
		newOperation(d, "/internal/warehouse/v2/expiry-due")["description"] = ""
	})
	mutate(privateOverlay, "needs a 200 response", func(d map[string]any) {
		newOperation(d, "/internal/auth/v1/registry/{restaurantID}")["responses"] = map[string]any{}
	})
	mutate(privateOverlay, "must declare security []", func(d map[string]any) {
		delete(newOperation(d, "/readyz"), "security")
	})

	// The public live operation only gains the 429 answer.
	mutate(publicOverlay, "drops response 200", func(d map[string]any) { delete(live(d), "200") })
	mutate(publicOverlay, "drops response default", func(d map[string]any) { delete(live(d), "default") })
	mutate(publicOverlay, "Unexpected responses added to GET /api/v2/r/{restaurantID}/live", func(d map[string]any) {
		live(d)["503"] = map[string]any{"description": "extra"}
	})
	mutate(publicOverlay, "Empty responses of GET /api/v2/r/{restaurantID}/live", func(d map[string]any) {
		durableMap(t, patches(d)["GET /api/v2/r/{restaurantID}/live"])["responses"] = map[string]any{}
	})
}

func durableList(t *testing.T, value any) []any {
	t.Helper()
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("expected a non-empty list, got %T", value)
	}
	return list
}

func durableMember(t *testing.T, value any, path ...string) any {
	t.Helper()
	for _, key := range path {
		value = durableMap(t, value)[key]
	}
	return value
}

func durableResponseSchema(t *testing.T, operation map[string]any, code string) string {
	t.Helper()
	return fmt.Sprint(durableMap(t, durableMember(t, operation["responses"], code, "content", "application/json", "schema"))["$ref"])
}

func durableParameters(t *testing.T, operation map[string]any) map[string]map[string]any {
	t.Helper()
	result := map[string]map[string]any{}
	for _, item := range durableList(t, operation["parameters"]) {
		parameter := durableMap(t, item)
		result[fmt.Sprint(parameter["name"])] = parameter
	}
	return result
}

func TestDurableOperationForms(t *testing.T) {
	predecessor := durableOperations(t, registrationDocument(t, "registration-v1-composed-private.json"))
	private := durableOperations(t, registrationDocument(t, "durable-v1-composed-private.json"))
	const registryKey = "GET /internal/auth/v1/registry"

	// The patched registry keeps everything it had and only adds.
	registry := private[registryKey]
	if text, ok := registry["description"].(string); !ok || strings.TrimSpace(text) == "" {
		t.Fatal("registry has no description")
	}
	if !reflect.DeepEqual(registry["security"], predecessor[registryKey]["security"]) {
		t.Fatal("registry security changed")
	}
	if !reflect.DeepEqual(durableMember(t, registry["responses"], "default"), durableMember(t, predecessor[registryKey]["responses"], "default")) {
		t.Fatal("registry lost its default answer")
	}
	if !reflect.DeepEqual(durableMember(t, registry["responses"], "200", "content"), durableMember(t, predecessor[registryKey]["responses"], "200", "content")) {
		t.Fatal("registry 200 content changed")
	}
	if durableResponseSchema(t, registry, "200") != "#/components/schemas/Registry" {
		t.Fatal("registry 200 is not Registry")
	}
	if _, ok := durableMap(t, durableMember(t, registry["responses"], "200", "headers"))["ETag"]; !ok {
		t.Fatal("registry 200 has no ETag")
	}
	if _, ok := durableMap(t, registry["responses"])["304"]; !ok {
		t.Fatal("registry has no 304")
	}
	parameters := durableParameters(t, registry)
	for _, name := range []string{"limit", "after", "since", "If-None-Match"} {
		if _, ok := parameters[name]; !ok {
			t.Fatal("registry parameter missing", name)
		}
	}
	if len(parameters) != 4 {
		t.Fatal("registry has unexpected parameters")
	}
	if olds, ok := predecessor[registryKey]["parameters"].([]any); ok {
		for _, old := range olds {
			found := false
			for _, candidate := range durableList(t, registry["parameters"]) {
				found = found || reflect.DeepEqual(candidate, old)
			}
			if !found {
				t.Fatal("registry lost a predecessor parameter")
			}
		}
	}

	// registryPoint: 200 with ETag, 304 and a typed 404.
	point := private["GET /internal/auth/v1/registry/{restaurantID}"]
	if point["operationId"] != "registryPoint" || durableResponseSchema(t, point, "200") != "#/components/schemas/RegistryPoint" {
		t.Fatal("registryPoint is not wired to RegistryPoint")
	}
	for _, code := range []string{"404", "default"} {
		if durableResponseSchema(t, point, code) != "#/components/schemas/Error" {
			t.Fatal("registryPoint answer is not Error", code)
		}
	}
	if _, ok := durableMap(t, point["responses"])["304"]; !ok {
		t.Fatal("registryPoint has no 304")
	}
	if _, ok := durableMap(t, durableMember(t, point["responses"], "200", "headers"))["ETag"]; !ok {
		t.Fatal("registryPoint 200 has no ETag")
	}
	pointParameters := durableParameters(t, point)
	if pointParameters["restaurantID"]["in"] != "path" || pointParameters["restaurantID"]["required"] != true || pointParameters["If-None-Match"]["in"] != "header" {
		t.Fatal("registryPoint parameters are wrong")
	}

	// expiryDue: asOf is required; limit and after page the list.
	due := private["GET /internal/warehouse/v2/expiry-due"]
	if due["operationId"] != "expiryDue" || due["x-required-capability"] != "expiry.due" || durableResponseSchema(t, due, "200") != "#/components/schemas/ExpiryDueList" {
		t.Fatal("expiryDue is not wired to ExpiryDueList")
	}
	dueParameters := durableParameters(t, due)
	if dueParameters["asOf"]["required"] != true || dueParameters["limit"]["required"] == true || dueParameters["after"]["required"] == true {
		t.Fatal("expiryDue parameters are wrong")
	}
	for _, name := range []string{"asOf", "limit", "after"} {
		if dueParameters[name]["in"] != "query" {
			t.Fatal("expiryDue parameter is not a query parameter", name)
		}
	}
	if durableMember(t, dueParameters["limit"]["schema"], "maximum") != float64(1000) {
		t.Fatal("expiryDue limit is not bounded")
	}
	if security, ok := due["security"].([]any); ok && len(security) == 0 {
		t.Fatal("expiryDue opens itself")
	}

	// Health: no key, readyz answers 200 and 503 with the same body.
	for _, key := range []string{"GET /healthz", "GET /readyz"} {
		if security, ok := private[key]["security"].([]any); !ok || len(security) != 0 {
			t.Fatal("health operation must declare security []", key)
		}
	}
	for _, code := range []string{"200", "503"} {
		if durableResponseSchema(t, private["GET /readyz"], code) != "#/components/schemas/HealthReadiness" {
			t.Fatal("readyz answer is not HealthReadiness", code)
		}
	}

	// The public live operation adds the SSE quota answer with Retry-After.
	publicPredecessor := durableOperations(t, registrationDocument(t, "registration-v1-composed-public.json"))
	live := durableOperations(t, registrationDocument(t, "durable-v1-composed-public.json"))["GET /api/v2/r/{restaurantID}/live"]
	if durableMember(t, live["responses"], "429", "headers", "Retry-After", "$ref") != "#/components/headers/Retry-After" {
		t.Fatal("live 429 has no Retry-After")
	}
	if durableResponseSchema(t, live, "429") != "#/components/schemas/Error" {
		t.Fatal("live 429 is not Error")
	}
	for _, code := range []string{"200", "default"} {
		if !reflect.DeepEqual(durableMap(t, live["responses"])[code], durableMap(t, publicPredecessor["GET /api/v2/r/{restaurantID}/live"]["responses"])[code]) {
			t.Fatal("live answer changed", code)
		}
	}
	if len(durableMap(t, live["responses"])) != 3 {
		t.Fatal("live has unexpected answers")
	}
}

func TestDurableErrorCodesAndRetryRules(t *testing.T) {
	for _, visibility := range []string{"public", "private"} {
		schemas := durableSchemas(t, registrationDocument(t, "durable-v1-composed-"+visibility+".json"))
		codes := durableList(t, durableMember(t, schemas["Error"], "properties", "code", "enum"))
		have := map[string]bool{}
		for _, code := range codes {
			have[fmt.Sprint(code)] = true
		}
		for _, absent := range []string{"CommandIdentityConflict", "AdmissionUnavailable"} {
			if have[absent] {
				t.Fatal(visibility, "Error.code contains", absent)
			}
		}
		for _, present := range []string{"CommandConflict", "RateLimited", "ServiceUnavailable", "LimitExceeded", "InvalidArgument", "UpstreamTimeout", "NotFound"} {
			if !have[present] {
				t.Fatal(visibility, "Error.code lacks", present)
			}
		}
		description := fmt.Sprint(durableMap(t, schemas["Error"])["description"])
		for _, text := range []string{"not_committed for a POST", "unavailable for a passive GET", "has already cleared", "every RateLimited answer"} {
			if !strings.Contains(description, text) {
				t.Fatalf("%s Error description lacks %q", visibility, text)
			}
		}
	}
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
		examples, err := os.ReadFile(filepath.Join(completionAPI(t), "durable-v1-examples.json"))
		if err != nil {
			t.Fatal(err)
		}
		examplesSum := sha256.Sum256(examples)
		if !bytes.Contains(amendment, []byte(hex.EncodeToString(examplesSum[:]))) {
			t.Fatal("docs/durable-api-amendment.md does not record the digest of durable-v1-examples.json")
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

// durableNumbers are the contract numbers of C13, C15, C25, C36 and C53. The
// amendment table, x-durable-policy and the schemas must all agree.
var durableNumbers = map[string]float64{
	"registry.activeRestaurantsMax":                  10000,
	"registry.pageLimitMax":                          1000,
	"registry.revisionStartsAt":                      1,
	"limits.orderPositionsMax":                       200,
	"limits.activeProposalsPerRestaurantMax":         2000,
	"limits.idMaxLength":                             256,
	"limits.clientCommandIDMaxLength":                256,
	"sseQuotas.perSession":                           8,
	"sseQuotas.perOrganization":                      200,
	"sseQuotas.perCell":                              2000,
	"commandConflict.authResultRetentionDays":        30,
	"commandConflict.tombstoneDays":                  180,
	"pairRetention.warehouseAttemptReceiptsMinDays":  35,
	"pairRetention.servingAutoResolveStopsAfterDays": 30,
}

func TestDurablePolicyNumbersMatchAmendmentAndSchemas(t *testing.T) {
	amendment, err := os.ReadFile(filepath.Join(completionAPI(t), "../durable-api-amendment.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]float64{}
	for _, match := range regexp.MustCompile("(?m)^\\| `([A-Za-z.]+)` \\| (\\d+) \\|").FindAllStringSubmatch(string(amendment), -1) {
		var number float64
		if _, err := fmt.Sscan(match[2], &number); err != nil {
			t.Fatal(err)
		}
		rows[match[1]] = number
	}
	if !reflect.DeepEqual(rows, durableNumbers) {
		t.Fatalf("amendment number table %v differs from the contract numbers %v", rows, durableNumbers)
	}
	for _, visibility := range []string{"public", "private"} {
		document := registrationDocument(t, "durable-v1-composed-"+visibility+".json")
		for key, want := range durableNumbers {
			path := strings.Split(key, ".")
			if got := durableMember(t, document["x-durable-policy"], path...); got != want {
				t.Fatalf("%s policy %s = %v, want %v", visibility, key, got, want)
			}
		}
		// G1 (16.10.2026) approved the numbers; the SSE quotas are a ceiling reviewed at G2.
		if got, _ := durableMember(t, document["x-durable-policy"], "status").(string); !strings.Contains(got, "Approved at G1 on 2026-10-16") {
			t.Fatalf("%s policy status = %q, want the G1 approval", visibility, got)
		}
		if got, _ := durableMember(t, document["x-durable-policy"], "sseQuotas", "status").(string); !strings.Contains(got, "ceiling") || !strings.Contains(got, "G2") {
			t.Fatalf("%s SSE status = %q, want a ceiling reviewed at G2", visibility, got)
		}
		schemas := durableSchemas(t, document)
		checks := []struct {
			name string
			got  any
			want float64
		}{
			{"PrepareOrderRequest.positions.maxItems", durableMember(t, schemas["PrepareOrderRequest"], "properties", "positions", "maxItems"), durableNumbers["limits.orderPositionsMax"]},
			{"ID.maxLength", durableMap(t, schemas["ID"])["maxLength"], durableNumbers["limits.idMaxLength"]},
			{"ClientCommandID.maxLength", durableMap(t, schemas["ClientCommandID"])["maxLength"], durableNumbers["limits.clientCommandIDMaxLength"]},
			{"Registry.entries.maxItems", durableMember(t, schemas["Registry"], "properties", "entries", "maxItems"), durableNumbers["registry.pageLimitMax"]},
			{"Registry.tombstones.maxItems", durableMember(t, schemas["Registry"], "properties", "tombstones", "maxItems"), durableNumbers["registry.pageLimitMax"]},
			{"ExpiryDueList.items.maxItems", durableMember(t, schemas["ExpiryDueList"], "properties", "items", "maxItems"), durableNumbers["registry.pageLimitMax"]},
		}
		for _, c := range checks {
			if c.got != c.want {
				t.Fatalf("%s %s = %v, want %v", visibility, c.name, c.got, c.want)
			}
		}
		if durableMember(t, schemas["RegistryEntry"], "properties", "services", "maxItems") != float64(2) {
			t.Fatal("RegistryEntry.services must hold warehouse and serving only")
		}
	}
}
