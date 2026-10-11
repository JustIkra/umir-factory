package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSuiteNowUsesWarehouseEvaluatedAtInsteadOfHostTime(t *testing.T) {
	const evaluatedAt = "2020-03-04T05:06:07.987654321+03:00"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/warehouse/inventory" {
			http.NotFound(writer, request)
			return
		}
		writeClockTestResponse(t, writer, http.StatusOK, `{"evaluatedAt":"`+evaluatedAt+`"}`)
	}))
	defer server.Close()

	s := suite{cfg: config{gatewayURL: server.URL}, client: server.Client()}
	got, err := s.now()
	if err != nil {
		t.Fatal(err)
	}
	want, err := time.Parse(time.RFC3339Nano, evaluatedAt)
	if err != nil {
		t.Fatal(err)
	}
	want = want.Truncate(time.Second)
	if !got.Equal(want) {
		t.Fatalf("suite clock = %s, want warehouse clock %s", got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}

func TestSuiteNowRejectsUnavailableOrInvalidWarehouseClock(t *testing.T) {
	tests := map[string]struct {
		status int
		body   string
	}{
		"unavailable": {status: http.StatusServiceUnavailable, body: `{"code":"Unavailable"}`},
		"missing":     {status: http.StatusOK, body: `{"products":[]}`},
		"malformed":   {status: http.StatusOK, body: `{"evaluatedAt":"not-a-time"}`},
		"wrong zone":  {status: http.StatusOK, body: `{"evaluatedAt":"2020-03-04T02:06:07Z"}`},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/api/warehouse/inventory" {
					http.Error(writer, "unexpected request", http.StatusNotFound)
					return
				}
				writeClockTestResponse(t, writer, test.status, test.body)
			}))
			defer server.Close()

			s := suite{cfg: config{gatewayURL: server.URL}, client: server.Client()}
			got, err := s.now()
			if err == nil {
				t.Fatalf("suite clock = %s, want an error", got.Format(time.RFC3339Nano))
			}
			if !got.IsZero() {
				t.Fatalf("suite clock = %s after error, want zero", got.Format(time.RFC3339Nano))
			}
		})
	}
}

func TestDeliveryFailureIncludesRequestAndClockDiagnosticsWithoutRetry(t *testing.T) {
	const evaluatedAt = "2020-03-04T05:06:07.987654321+03:00"
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/warehouse/deliveries":
			posts++
			writeClockTestResponse(t, writer, http.StatusBadRequest, `{"code":"InvalidArgument"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/api/warehouse/inventory":
			writeClockTestResponse(t, writer, http.StatusOK, `{"evaluatedAt":"`+evaluatedAt+`","products":[]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	s := suite{cfg: config{gatewayURL: server.URL}, client: server.Client()}
	_, err := s.delivery("H13", "delivery-H13", "product-H13", "1000", time.Date(2020, time.March, 4, 4, 6, 7, 0, time.FixedZone("MSK", 3*60*60)), time.Date(2020, time.March, 4, 5, 6, 7, 0, time.FixedZone("MSK", 3*60*60)))
	if err == nil {
		t.Fatal("delivery succeeded, want the fixture HTTP refusal")
	}
	for _, expected := range []string{
		`request={"commandID":"receive-H13"`,
		`"createdAt":"2020-03-04T05:06:07+03:00"`,
		`"receivedAt":"2020-03-04T05:06:07+03:00"`,
		"hostSendAt=",
		"warehouseEvaluatedAtAfterFailure=2020-03-04T05:06:07.987654321+03:00",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("delivery error %q does not contain %q", err, expected)
		}
	}
	if posts != 1 {
		t.Fatalf("delivery POST count = %d, want one without retries", posts)
	}
}

func writeClockTestResponse(t *testing.T, writer http.ResponseWriter, status int, body string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Errorf("write test response: %v", err)
	}
}

func TestHistoryEventCountersKeepScenarioScopesSeparate(t *testing.T) {
	history := map[string]any{"events": []any{
		map[string]any{"type": "OrderPlaced", "commandID": "C1", "payload": map[string]any{"order": map[string]any{"proposalID": "P1"}}},
		map[string]any{"type": "OrderPlaced", "commandID": "C2", "payload": map[string]any{"order": map[string]any{"proposalID": "P2"}}},
		map[string]any{"type": "DeliveryReceived", "commandID": "D1", "payload": map[string]any{}},
	}}
	if got := eventCount(history, "OrderPlaced"); got != 2 {
		t.Fatalf("all OrderPlaced count = %d, want 2", got)
	}
	if got := eventCountForCommand(history, "OrderPlaced", "C1"); got != 1 {
		t.Fatalf("C1 OrderPlaced count = %d, want 1", got)
	}
	if got := eventCountForProposal(history, "OrderPlaced", "P2"); got != 1 {
		t.Fatalf("P2 OrderPlaced count = %d, want 1", got)
	}
	if got := eventCountForProposal(history, "OrderPlaced", "P3"); got != 0 {
		t.Fatalf("unrelated proposal count = %d, want 0", got)
	}
}

func TestAcceptanceCLIRejectsInvalidPhaseAndExternalURLs(t *testing.T) {
	tests := map[string][]string{
		"phase":                  {"--phase", "all", "--gateway-url", "http://127.0.0.1:8081"},
		"external gateway":       {"--phase", "normal", "--gateway-url", "http://example.com", "--factory-dir", "/tmp", "--compose-project", "project"},
		"missing fault controls": {"--phase", "faults", "--gateway-url", "http://127.0.0.1:8081"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr=%s", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("invalid CLI emitted evidence: %s", stdout.String())
			}
		})
	}
}

func TestAcceptanceFaultCLIRequiresOwnedProjectAndExistingOverride(t *testing.T) {
	factory := t.TempDir()
	for name, content := range map[string]string{"compose.yaml": "services: {}\n", ".env.example": "UMIR_API_PORT=0\n"} {
		if err := os.WriteFile(filepath.Join(factory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := []string{
		"--phase", "faults",
		"--gateway-url", "http://127.0.0.1:8081",
		"--serving-fault-control-url", "http://127.0.0.1:8082",
		"--warehouse-fault-control-url", "http://127.0.0.1:8083",
		"--control-token", "test-token",
		"--factory-dir", factory,
	}
	for name, extra := range map[string][]string{
		"missing project":        nil,
		"unowned project":        {"--compose-project", "umir"},
		"missing fault override": {"--compose-project", "umir-http-0123456789abcdef01234567-fault"},
		"missing override file":  {"--compose-project", "umir-http-0123456789abcdef01234567-fault", "--compose-override", filepath.Join(factory, "missing.json")},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(append(append([]string(nil), base...), extra...), &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr=%s", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("invalid fault context emitted evidence: %s", stdout.String())
			}
		})
	}
}

func TestAcceptanceCLIRejectsPhaseProjectMismatchBeforeDocker(t *testing.T) {
	root := t.TempDir()
	factory := filepath.Join(root, "factory")
	if err := os.MkdirAll(factory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"compose.yaml": "services: {}\n", ".env.example": "UMIR_API_PORT=0\n"} {
		if err := os.WriteFile(filepath.Join(factory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "docker-called")
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nprintf called > "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	baseProject := "umir-http-0123456789abcdef01234567"
	tests := map[string][]string{
		"normal with fault project":  {"--phase", "normal", "--gateway-url", "http://127.0.0.1:8081", "--factory-dir", factory, "--compose-project", baseProject + "-fault"},
		"faults with normal project": {"--phase", "faults", "--gateway-url", "http://127.0.0.1:8081", "--factory-dir", factory, "--compose-project", baseProject, "--serving-fault-control-url", "http://127.0.0.1:8082", "--warehouse-fault-control-url", "http://127.0.0.1:8083", "--control-token", "test-token"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr=%s", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("phase mismatch emitted evidence: %s", stdout.String())
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("Docker ran for a phase/project mismatch; stat error=%v", err)
			}
		})
	}
}

func TestDefiniteCommandConflictRequiresTheExactFlatErrorAndOutcome(t *testing.T) {
	for name, result := range map[string]response{
		"accepted":        {status: 409, body: map[string]any{"code": "CommandIdentityConflict", "outcome": "not_committed"}},
		"wrong conflict":  {status: 409, body: map[string]any{"code": "ProposalNotFound", "outcome": "not_committed"}},
		"missing outcome": {status: 409, body: map[string]any{"code": "CommandIdentityConflict"}},
		"pending outcome": {status: 409, body: map[string]any{"code": "CommandIdentityConflict", "outcome": "pending"}},
		"wrong status":    {status: 202, body: map[string]any{"code": "CommandIdentityConflict", "outcome": "not_committed"}},
		"empty response":  {status: 409, body: map[string]any{}},
	} {
		t.Run(name, func(t *testing.T) {
			got := isDefiniteCommandIdentityConflict(result)
			want := name == "accepted"
			if got != want {
				t.Fatalf("isDefiniteCommandIdentityConflict(%#v) = %t, want %t", result, got, want)
			}
		})
	}
}

func TestPendingConfirmResultRequiresTaggedPendingKind(t *testing.T) {
	for name, result := range map[string]response{
		"tagged pending": {status: 202, body: map[string]any{"kind": "ConfirmationPending"}},
		"missing kind":   {status: 202, body: map[string]any{}},
		"wrong kind":     {status: 202, body: map[string]any{"kind": "OrderConfirmed"}},
		"wrong status":   {status: 200, body: map[string]any{"kind": "ConfirmationPending"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := isPendingConfirmationResult(result)
			if want := name == "tagged pending"; got != want {
				t.Fatalf("isPendingConfirmationResult(%#v) = %t, want %t", result, got, want)
			}
		})
	}
}

func TestRestartRejectsNonOwnedContextsBeforeDocker(t *testing.T) {
	root := t.TempDir()
	factory := filepath.Join(root, "factory")
	if err := os.MkdirAll(factory, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"compose.yaml": "services: {}\n", ".env.example": "UMIR_API_PORT=0\n"} {
		if err := os.WriteFile(filepath.Join(factory, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "docker-called")
	docker := filepath.Join(bin, "docker")
	script := "#!/bin/sh\nprintf called > " + marker + "\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	validProject := "umir-http-0123456789abcdef01234567"
	validOverride := filepath.Join(root, "compose.override.json")
	if err := os.WriteFile(validOverride, []byte(`{"services":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]restartConfig{
		"empty project":       {factoryDir: factory, project: "", override: validOverride},
		"default project":     {factoryDir: factory, project: "umir", override: validOverride},
		"relative factory":    {factoryDir: "factory", project: validProject},
		"relative override":   {factoryDir: factory, project: validProject, override: "compose.override.json"},
		"missing override":    {factoryDir: factory, project: validProject + "-fault", override: filepath.Join(root, "missing.json")},
		"missing base config": {factoryDir: root, project: validProject},
	} {
		t.Run(name, func(t *testing.T) {
			if err := restart(t.Context(), cfg, "warehouse"); err == nil {
				t.Fatal("unsafe restart context was accepted")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("docker was invoked for invalid ownership context; stat error=%v", err)
			}
		})
	}
}

func TestRestartUsesExactOwnedFaultProjectAndOverride(t *testing.T) {
	root := t.TempDir()
	factory := filepath.Join(root, "factory")
	if err := os.MkdirAll(factory, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"compose.yaml": "services: {}\n", ".env.example": "UMIR_API_PORT=0\n"} {
		if err := os.WriteFile(filepath.Join(factory, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	override := filepath.Join(root, "faults.json")
	if err := os.WriteFile(override, []byte(`{"services":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(root, "docker-args")
	docker := filepath.Join(bin, "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	project := "umir-http-0123456789abcdef01234567-fault"
	if err := restart(t.Context(), restartConfig{factoryDir: factory, project: project, override: override}, "warehouse"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"compose", "--project-name", project, "--file", filepath.Join(factory, "compose.yaml"), "--file", override, "--env-file", filepath.Join(factory, ".env.example"), "restart", "--timeout", "10", "warehouse", ""}, "\n")
	if string(data) != want {
		t.Fatalf("restart docker args = %q, want %q", string(data), want)
	}
}
