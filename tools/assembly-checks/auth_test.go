package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func candidateArgs(factory string) []string {
	return []string{
		"--factory-dir", factory, "--candidate",
		"--warehouse-ref", strings.Repeat("a", 40), "--serving-ref", strings.Repeat("b", 40),
		"--gateway-ref", strings.Repeat("c", 40), "--frontend-ref", strings.Repeat("d", 40),
	}
}

func TestAuthAcceptanceIsACandidateMode(t *testing.T) {
	factory := t.TempDir()
	options, err := parseOptions("http-acceptance", append(candidateArgs(factory), "--auth"))
	if err != nil || !options.auth {
		t.Fatalf("candidate auth options = %+v, %v", options, err)
	}
	if _, err := parseOptions("http-acceptance", []string{"--factory-dir", factory, "--selected", "--auth"}); err == nil {
		t.Fatal("selected mode accepted --auth")
	}
}

func completeOutcomes(auth bool) []string {
	outcomes := make([]string, 0, 28)
	for number := 1; number <= 25; number++ {
		if number != 16 {
			outcomes = append(outcomes, fmt.Sprintf("H%02d:passed", number))
		}
	}
	if auth {
		outcomes = append(outcomes, "A01:passed", "A02:passed", "A03:passed", "A04:passed")
	}
	return outcomes
}

func TestAuthAcceptanceRequiresTheAuthScenarios(t *testing.T) {
	if err := validateHTTPOutcomes(completeOutcomes(true), true, false, false); err != nil {
		t.Fatalf("complete auth matrix rejected: %v", err)
	}
	withoutA03 := slices.DeleteFunc(completeOutcomes(true), func(outcome string) bool { return outcome == "A03:passed" })
	if err := validateHTTPOutcomes(withoutA03, true, false, false); err == nil {
		t.Fatal("auth matrix accepted a missing A03")
	}
	if err := validateHTTPOutcomes(completeOutcomes(true), false, false, false); err == nil {
		t.Fatal("legacy matrix accepted auth outcomes")
	}
	if err := validateHTTPOutcomes(completeOutcomes(false), false, false, false); err != nil {
		t.Fatalf("complete legacy matrix rejected: %v", err)
	}
}

func TestAuthMarkerMustBeExplicit(t *testing.T) {
	for name, content := range map[string]string{
		"exact":     "# comment\nx-umir-auth-acceptance: candidate\nservices: {}\n",
		"selected":  "x-umir-auth-acceptance: selected\n",
		"duplicate": "x-umir-auth-acceptance: candidate\nx-umir-auth-acceptance: candidate\n",
		"absent":    "services: {}\n",
		"indented":  "services:\n  x-umir-auth-acceptance: candidate\n",
	} {
		t.Run(name, func(t *testing.T) {
			factory := t.TempDir()
			if err := os.WriteFile(filepath.Join(factory, "compose.auth.yaml"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := verifyAuthMarker(factory)
			if (name == "exact") != (err == nil) {
				t.Fatalf("verifyAuthMarker = %v", err)
			}
		})
	}
	if err := verifyAuthMarker(t.TempDir()); err == nil {
		t.Fatal("a missing compose.auth.yaml was accepted")
	}
}

func publisher(url string, target, published int) map[string]any {
	return map[string]any{"URL": url, "TargetPort": target, "PublishedPort": published, "Protocol": "tcp"}
}

func TestOnlyTheFrontendMayPublishAPort(t *testing.T) {
	const port = 18090
	exposed := []any{publisher("", 8080, 0)}
	valid := map[string][]any{"warehouse": exposed, "serving": exposed, "gateway": exposed, "frontend": {publisher("127.0.0.1", 8080, port)}}
	fixtures := []string{"http-fault-serving", "http-fault-warehouse"}
	for _, test := range []struct {
		name     string
		change   func(map[string][]any)
		fixtures []string
		ok       bool
	}{
		{name: "frontend only", change: func(map[string][]any) {}, ok: true},
		{name: "gateway published", change: func(p map[string][]any) { p["gateway"] = []any{publisher("127.0.0.1", 8080, 18091)} }},
		{name: "warehouse published", change: func(p map[string][]any) { p["warehouse"] = []any{publisher("127.0.0.1", 8082, 18092)} }},
		{name: "another service published", change: func(p map[string][]any) { p["feedback"] = []any{publisher("0.0.0.0", 8080, 18766)} }},
		{name: "unpublished service", change: func(p map[string][]any) { p["feedback"] = exposed }, ok: true},
		{name: "fault controls on loopback", change: func(p map[string][]any) {
			p["http-fault-serving"] = []any{publisher("127.0.0.1", 9090, 18094)}
			p["http-fault-warehouse"] = []any{publisher("127.0.0.1", 9090, 18095)}
		}, fixtures: fixtures, ok: true},
		{name: "fault control outside the faults phase", change: func(p map[string][]any) { p["http-fault-serving"] = []any{publisher("127.0.0.1", 9090, 18094)} }},
		{name: "fault control on all hosts", change: func(p map[string][]any) { p["http-fault-serving"] = []any{publisher("0.0.0.0", 9090, 18094)} }, fixtures: fixtures},
		{name: "fault proxy port", change: func(p map[string][]any) { p["http-fault-serving"] = []any{publisher("127.0.0.1", 8080, 18094)} }, fixtures: fixtures},
		{name: "frontend all hosts", change: func(p map[string][]any) { p["frontend"] = []any{publisher("0.0.0.0", 8080, port)} }},
		{name: "frontend other port", change: func(p map[string][]any) { p["frontend"] = []any{publisher("127.0.0.1", 8080, port+1)} }},
		{name: "frontend two ports", change: func(p map[string][]any) { p["frontend"] = append(p["frontend"], publisher("127.0.0.1", 9000, 18093)) }},
		{name: "frontend unpublished", change: func(p map[string][]any) { p["frontend"] = exposed }},
	} {
		t.Run(test.name, func(t *testing.T) {
			publishers := make(map[string][]any, len(valid))
			for service, list := range valid {
				publishers[service] = slices.Clone(list)
			}
			test.change(publishers)
			var report []map[string]any
			for service, list := range publishers {
				report = append(report, map[string]any{"Service": service, "State": "running", "Health": "healthy", "Publishers": list})
			}
			array, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			// Compose 2.21 and later print one JSON object per line.
			var lines []byte
			for _, container := range report {
				line, err := json.Marshal(container)
				if err != nil {
					t.Fatal(err)
				}
				lines = append(append(lines, line...), '\n')
			}
			for format, data := range map[string][]byte{"array": array, "lines": lines} {
				err := checkPublishedPorts(data, port, test.fixtures)
				if test.ok != (err == nil) {
					t.Fatalf("%s: checkPublishedPorts = %v", format, err)
				}
				if strings.HasSuffix(test.name, " published") && !strings.Contains(err.Error(), "only frontend may publish") {
					t.Fatalf("%s: a published service was not named as such: %v", format, err)
				}
			}
		})
	}
}

// hashingExecutor answers the hash-password run and records what it received on stdin.
type hashingExecutor struct {
	args  []string
	stdin []byte
	out   string
}

func (e *hashingExecutor) Run(context.Context, string, []string, string, ...string) ([]byte, []byte, error) {
	return nil, nil, errors.New("hash-password must receive the password on stdin")
}

func (e *hashingExecutor) RunInput(_ context.Context, _ string, _ []string, stdin []byte, name string, args ...string) ([]byte, []byte, error) {
	e.args, e.stdin = append([]string{name}, args...), stdin
	return []byte(e.out), nil, nil
}

func TestAuthAssemblyHashesADisposablePasswordWithTheGatewayImage(t *testing.T) {
	executor := &hashingExecutor{out: "$argon2id$v=19$m=65536,t=2,p=1$c2FsdA$aGFzaA\n"}
	temp := t.TempDir()
	auth, err := prepareAuthAssembly(context.Background(), executor, t.TempDir(), temp, "umir-factory/gateway:sha-c", func() (int, error) { return 18090, nil })
	if err != nil {
		t.Fatal(err)
	}
	password, err := os.ReadFile(auth.passwordFile)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(auth.passwordFile); err != nil || info.Mode().Perm() != 0o600 || filepath.Dir(auth.passwordFile) != temp {
		t.Fatalf("password file %s is not a private file in the run workspace: %v", auth.passwordFile, err)
	}
	if len(strings.TrimSpace(string(password))) < 16 || string(executor.stdin) != string(password) {
		t.Fatalf("hash-password stdin %q does not carry the disposable password %q", executor.stdin, password)
	}
	want := []string{"docker", "run", "--rm", "-i", "--network", "none", "umir-factory/gateway:sha-c", "hash-password"}
	if !slices.Equal(executor.args, want) {
		t.Fatalf("hash-password command = %v, want %v", executor.args, want)
	}
	for _, arg := range executor.args {
		if strings.Contains(arg, strings.TrimSpace(string(password))) {
			t.Fatal("the password reached a command argument")
		}
	}
	env := auth.composeEnv()
	for _, want := range []string{
		"UMIR_WEB_PORT=18090", "OPERATOR_PUBLIC_ORIGIN=http://127.0.0.1:18090", "OPERATOR_COOKIE_MODE=loopback-development",
		"OPERATOR_BOOTSTRAP_PASSWORD_HASH=$argon2id$v=19$m=65536,t=2,p=1$c2FsdA$aGFzaA",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("auth environment %v lacks %s", env, want)
		}
	}
	for _, entry := range env {
		if strings.Contains(entry, strings.TrimSpace(string(password))) {
			t.Fatal("the password reached the Compose environment")
		}
	}
	executor.out = "not a verifier\n"
	if _, err := prepareAuthAssembly(context.Background(), executor, t.TempDir(), t.TempDir(), "image", func() (int, error) { return 18090, nil }); err == nil {
		t.Fatal("an invalid hash-password output was accepted")
	}
}

// authPhaseExecutor runs one auth phase against a test server standing in for frontend.
type authPhaseExecutor struct {
	httpRunnerExecutor
	address        string
	port           int
	publishGateway bool
	faultControls  bool
	realtime       bool
	composeEnvs    [][]string
	harnessEnv     []string
	portQueries    []string
}

func (e *authPhaseExecutor) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
	if name == "docker" && len(args) > 0 && args[0] == "compose" {
		e.composeEnvs = append(e.composeEnvs, env)
		if slices.Contains(args, "port") {
			e.portQueries = append(e.portQueries, args[len(args)-2])
			return []byte(e.address + "\n"), nil, nil
		}
		if slices.Contains(args, "ps") && !slices.Contains(args, "-q") {
			exposed := []any{publisher("", 8080, 0)}
			var report []map[string]any
			for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
				publishers := exposed
				if service == "frontend" || service == "gateway" && e.publishGateway {
					publishers = []any{publisher("127.0.0.1", 8080, e.port)}
				}
				report = append(report, map[string]any{"Service": service, "State": "running", "Health": "healthy", "Publishers": publishers})
			}
			if e.faultControls {
				fixtures := []string{"http-fault-serving", "http-fault-warehouse"}
				if e.realtime {
					fixtures = append(fixtures, "http-fault-warehouse-public")
				}
				for index, fixture := range fixtures {
					report = append(report, map[string]any{"Service": fixture, "State": "running", "Health": "healthy", "Publishers": []any{publisher("127.0.0.1", 9090, 18100+index)}})
				}
			}
			data, err := json.Marshal(report)
			return data, nil, err
		}
	}
	if strings.HasSuffix(name, "http-acceptance") {
		e.harnessEnv = env
		e.acceptanceArgs = append(e.acceptanceArgs, append([]string(nil), args...))
		data, err := json.Marshal(map[string]any{"outcomes": []string{"A01:passed", "A02:passed", "A03:passed", "A04:passed", "H01:passed"}})
		return data, nil, err
	}
	return e.httpRunnerExecutor.Run(ctx, dir, env, name, args...)
}

func TestRealtimeFaultPhasePassesPublicWarehouseControlURL(t *testing.T) {
	factory := runnerTestFactory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{`/api/serving/healthz`: `{"status":"ok","service":"umir-serving"}`, `/api/warehouse/healthz`: `{"status":"ok","service":"umir-warehouse"}`, `/`: `<div id="root"></div>`}[r.URL.Path]
		if body == "" {
			http.NotFound(w, r)
			return
		}
		if err := writeTestResponse(w, body); err != nil {
			t.Errorf("write smoke response: %v", err)
		}
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	portText := strings.TrimPrefix(address, "127.0.0.1:")
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("disposable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := &authAssembly{port: port, origin: server.URL, login: "operator", displayName: "Приёмка", passwordHash: "$argon2id$hash", passwordFile: passwordFile}
	executor := &authPhaseExecutor{address: address, port: port, faultControls: true, realtime: true}
	evidence := httpRunEvidence{PhaseCleanup: map[string]string{}, ContainerIDs: map[string]map[string]string{}, Images: map[string]imageEvidence{}}
	for _, service := range []string{"warehouse", "serving", "gateway", "frontend", "fault-fixture"} {
		evidence.Images[service] = imageEvidence{ID: "sha256:local"}
	}
	var stdout, stderr strings.Builder
	result, err := runHTTPPhase(context.Background(), factory, "umir-http-0123456789abcdef01234567-fault", filepath.Join(t.TempDir(), "faults.json"), "faults", "/bin/http-acceptance", "token", cliOptions{candidate: true, auth: true, realtime: true}, auth, &evidence, executor, &stdout, &stderr)
	if err != nil {
		t.Fatalf("realtime auth fault phase failed: %v; %s", err, stderr.String())
	}
	if len(result.Outcomes) == 0 {
		t.Fatal("realtime fault harness emitted no outcomes")
	}
	args := executor.acceptanceArgs[0]
	if !slices.Contains(args, "--realtime") || valueAfter(args, "--public-warehouse-fault-control-url") != server.URL {
		t.Fatalf("realtime fault arguments = %v", args)
	}
}

func TestAuthPhaseUsesOnlyTheFrontendAndTheOverride(t *testing.T) {
	factory := runnerTestFactory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{"/api/serving/healthz": `{"status":"ok","service":"umir-serving"}`, "/api/warehouse/healthz": `{"status":"ok","service":"umir-warehouse"}`, "/": `<div id="root"></div>`}[r.URL.Path]
		if body == "" {
			http.NotFound(w, r)
			return
		}
		if err := writeTestResponse(w, body); err != nil {
			t.Errorf("write smoke response: %v", err)
		}
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	_, portText, _ := strings.Cut(address, ":")
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("disposable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := &authAssembly{port: port, origin: server.URL, login: "operator", displayName: "Приёмка", passwordHash: "$argon2id$hash", passwordFile: passwordFile}
	executor := &authPhaseExecutor{address: address, port: port}
	evidence := httpRunEvidence{PhaseCleanup: map[string]string{}, ContainerIDs: map[string]map[string]string{}, Images: map[string]imageEvidence{}}
	for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
		evidence.Images[service] = imageEvidence{ID: "sha256:local"}
	}
	override := filepath.Join(t.TempDir(), "compose.normal.json")
	var stdout, stderr strings.Builder
	result, err := runHTTPPhase(context.Background(), factory, "umir-http-0123456789abcdef01234567", override, "normal", "/bin/http-acceptance", "token", cliOptions{candidate: true, auth: true, realtime: true}, auth, &evidence, executor, &stdout, &stderr)
	if err != nil {
		t.Fatalf("auth phase failed: %v; %s", err, stderr.String())
	}
	if len(result.Outcomes) != 5 {
		t.Fatalf("phase outcomes = %v", result.Outcomes)
	}
	if !slices.Equal(executor.portQueries, []string{"frontend"}) {
		t.Errorf("published ports queried for %v, want frontend only", executor.portQueries)
	}
	authFile := filepath.Join(factory, "compose.auth.yaml")
	down := executor.composeDownArgs
	if !slices.Contains(down, authFile) || !slices.Contains(down, "--volumes") {
		t.Errorf("cleanup %v does not use compose.auth.yaml and remove the disposable account volume", down)
	}
	for _, env := range executor.composeEnvs {
		if !slices.Contains(env, "OPERATOR_PUBLIC_ORIGIN="+server.URL) || !slices.Contains(env, "UMIR_WEB_PORT="+portText) {
			t.Fatalf("Compose ran without the auth environment: %v", env)
		}
	}
	args := executor.acceptanceArgs[0]
	if valueAfter(args, "--gateway-url") != server.URL || !slices.Contains(args, "--auth") || !slices.Contains(args, "--realtime") ||
		valueAfter(args, "--operator-login") != "operator" || valueAfter(args, "--operator-password-file") != passwordFile {
		t.Errorf("harness args = %v", args)
	}
	var overrides []string
	for index, arg := range args {
		if arg == "--compose-override" {
			overrides = append(overrides, args[index+1])
		}
	}
	if !slices.Equal(overrides, []string{authFile, override}) {
		t.Errorf("harness overrides = %v, want compose.auth.yaml then the generated override", overrides)
	}
	if !slices.Contains(executor.harnessEnv, "OPERATOR_PUBLIC_ORIGIN="+server.URL) {
		t.Error("the harness cannot restart services without the auth environment")
	}
	if !slices.Contains(evidence.RedactedComposeFacts, "api="+server.URL) || slices.ContainsFunc(evidence.RedactedComposeFacts, func(fact string) bool { return strings.HasPrefix(fact, "gateway=") }) {
		t.Errorf("auth Compose facts = %v, want the frontend-routed api fact only", evidence.RedactedComposeFacts)
	}
}

func TestAuthFaultsPhaseAcceptsOnlyTheFaultControls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{"/api/serving/healthz": `{"status":"ok","service":"umir-serving"}`, "/api/warehouse/healthz": `{"status":"ok","service":"umir-warehouse"}`, "/": `<div id="root"></div>`}[r.URL.Path]
		if body == "" {
			http.NotFound(w, r)
			return
		}
		if err := writeTestResponse(w, body); err != nil {
			t.Errorf("write smoke response: %v", err)
		}
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	_, portText, _ := strings.Cut(address, ":")
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("disposable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, publishGateway := range []bool{false, true} {
		auth := &authAssembly{port: port, origin: server.URL, login: "operator", passwordFile: passwordFile}
		executor := &authPhaseExecutor{address: address, port: port, faultControls: true, publishGateway: publishGateway}
		evidence := httpRunEvidence{PhaseCleanup: map[string]string{}, ContainerIDs: map[string]map[string]string{}, Images: map[string]imageEvidence{}}
		for _, service := range []string{"warehouse", "serving", "gateway", "frontend", "fault-fixture"} {
			evidence.Images[service] = imageEvidence{ID: "sha256:local"}
		}
		override := filepath.Join(t.TempDir(), "compose.faults.json")
		var stdout, stderr strings.Builder
		_, err := runHTTPPhase(context.Background(), runnerTestFactory(t), "umir-http-0123456789abcdef01234567-fault", override, "faults", "/bin/http-acceptance", "token", cliOptions{candidate: true, auth: true}, auth, &evidence, executor, &stdout, &stderr)
		if publishGateway {
			if err == nil || !strings.Contains(err.Error(), "one-port boundary") {
				t.Fatalf("a published gateway passed the auth faults phase: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("the fault controls failed the auth faults phase: %v; %s", err, stderr.String())
		}
	}
}

func TestAuthPhaseStopsWhenAnotherServicePublishesAPort(t *testing.T) {
	factory := runnerTestFactory(t)
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("disposable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := &authAssembly{port: 18090, origin: "http://127.0.0.1:18090", login: "operator", passwordFile: passwordFile}
	executor := &authPhaseExecutor{address: "127.0.0.1:18090", port: 18090, publishGateway: true}
	evidence := httpRunEvidence{PhaseCleanup: map[string]string{}, ContainerIDs: map[string]map[string]string{}, Images: map[string]imageEvidence{}}
	for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
		evidence.Images[service] = imageEvidence{ID: "sha256:local"}
	}
	var stdout, stderr strings.Builder
	_, err := runHTTPPhase(context.Background(), factory, "umir-http-0123456789abcdef01234567", "", "normal", "/bin/http-acceptance", "token", cliOptions{candidate: true, auth: true}, auth, &evidence, executor, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "one-port boundary") {
		t.Fatalf("a published gateway passed the auth phase: %v", err)
	}
	if len(executor.acceptanceArgs) != 0 {
		t.Fatal("the harness ran against an assembly that publishes the gateway")
	}
	if !executor.downCalled {
		t.Fatal("the rejected assembly was not removed")
	}
}
