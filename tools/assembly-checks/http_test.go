package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHTTPAcceptanceRequiresOneModeAndFourExactRefs(t *testing.T) {
	factory := t.TempDir()
	valid := []string{
		"--factory-dir", factory,
		"--candidate",
		"--warehouse-ref", strings.Repeat("a", 40),
		"--serving-ref", strings.Repeat("b", 40),
		"--gateway-ref", strings.Repeat("c", 40),
		"--frontend-ref", strings.Repeat("d", 40),
	}
	if _, err := parseOptions("http-acceptance", valid); err != nil {
		t.Fatalf("valid candidate options rejected: %v", err)
	}

	for name, args := range map[string][]string{
		"missing mode": {"--factory-dir", factory},
		"both modes":   append(append([]string(nil), valid...), "--selected"),
		"missing ref": {
			"--factory-dir", factory, "--candidate",
			"--warehouse-ref", strings.Repeat("a", 40),
			"--serving-ref", strings.Repeat("b", 40),
			"--gateway-ref", strings.Repeat("c", 40),
		},
		"uppercase ref": {
			"--factory-dir", factory, "--candidate",
			"--warehouse-ref", strings.Repeat("A", 40),
			"--serving-ref", strings.Repeat("b", 40),
			"--gateway-ref", strings.Repeat("c", 40),
			"--frontend-ref", strings.Repeat("d", 40),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseOptions("http-acceptance", args); err == nil {
				t.Fatal("invalid HTTP acceptance options were accepted")
			}
		})
	}
}

func TestHTTPSelectedModeDoesNotAcceptCandidateRefs(t *testing.T) {
	_, err := parseOptions("http-acceptance", []string{"--factory-dir", t.TempDir(), "--selected", "--warehouse-ref", strings.Repeat("a", 40)})
	if err == nil {
		t.Fatal("selected mode accepted candidate refs")
	}
}

func TestHTTPAcceptanceRequiresEveryBlackBoxMatrixOutcome(t *testing.T) {
	outcomes := make([]string, 0, 24)
	for number := 1; number <= 25; number++ {
		if number == 16 || number == 25 {
			continue
		}
		outcomes = append(outcomes, fmt.Sprintf("H%02d:passed", number))
	}
	if err := validateHTTPOutcomes(outcomes, false); err == nil {
		t.Fatal("HTTP matrix accepted missing runner-owned H25 evidence")
	}
	outcomes = append(outcomes, "H25:passed")
	if err := validateHTTPOutcomes(outcomes, false); err != nil {
		t.Fatalf("complete black-box matrix rejected: %v", err)
	}
	for name, invalid := range map[string][]string{
		"missing required scenario": append([]string(nil), outcomes[:5]...),
		"duplicate scenario":        append(append([]string(nil), outcomes...), "H02:passed"),
		"counterfeit service-only":  append(append([]string(nil), outcomes...), "H16:passed"),
		"failed scenario":           append(append([]string(nil), outcomes[:len(outcomes)-1]...), "H25:failed"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateHTTPOutcomes(invalid, false); err == nil {
				t.Fatal("invalid HTTP matrix was accepted")
			}
		})
	}
}

func TestHTTPOverrideIsValidComposeJSONWithOneConfigPerService(t *testing.T) {
	options := cliOptions{candidate: true, warehouse: strings.Repeat("a", 40), serving: strings.Repeat("b", 40), gateway: strings.Repeat("c", 40), frontend: strings.Repeat("d", 40)}
	path := filepath.Join(t.TempDir(), "override.json")
	if err := writeHTTPOverride(path, options, "umir-http-test", "fault-image", "ephemeral-token"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("Compose override is invalid JSON/YAML: %v", err)
	}
	if len(config.Services) != 6 {
		t.Fatalf("override services = %d, want four candidate services and two fixtures", len(config.Services))
	}
	for service, ref := range map[string]string{
		"warehouse": candidateImageRef("warehouse", options.warehouse, "umir-http-test"),
		"serving":   candidateImageRef("serving", options.serving, "umir-http-test"),
		"gateway":   candidateImageRef("gateway", options.gateway, "umir-http-test"),
		"frontend":  candidateImageRef("frontend", options.frontend, "umir-http-test"),
	} {
		if got := config.Services[service]["image"]; got != ref {
			t.Errorf("%s image = %v, want %s", service, got, ref)
		}
	}
	servingEnv, ok := config.Services["serving"]["environment"].(map[string]any)
	if !ok || servingEnv["WAREHOUSE_INTERNAL_URL"] != "http://http-fault-warehouse:8082" {
		t.Fatalf("serving internal URL override = %#v", config.Services["serving"]["environment"])
	}
	if _, exposed := config.Services["warehouse"]["ports"]; exposed {
		t.Fatal("warehouse internal listener was published to the host")
	}
	for _, fixture := range []string{"http-fault-serving", "http-fault-warehouse"} {
		ports, ok := config.Services[fixture]["ports"].([]any)
		if !ok || len(ports) != 1 || !strings.HasPrefix(fmt.Sprint(ports[0]), "127.0.0.1::") {
			t.Errorf("%s control port is not loopback/ephemeral: %#v", fixture, config.Services[fixture]["ports"])
		}
	}
}

func TestCandidateImageTagsReuseExactSHAAcrossUniqueProjects(t *testing.T) {
	sha := strings.Repeat("c", 40)
	first := candidateImageRef("gateway", sha, "umir-http-first")
	second := candidateImageRef("gateway", sha, "umir-http-second")
	if first != second || first != "umir-factory/gateway:sha-"+sha {
		t.Fatalf("candidate cache refs = %q and %q", first, second)
	}
	projectOne, err := uniqueComposeProject()
	if err != nil {
		t.Fatal(err)
	}
	projectTwo, err := uniqueComposeProject()
	if err != nil {
		t.Fatal(err)
	}
	if projectOne == projectTwo {
		t.Fatal("Compose projects are not isolated")
	}
}

func TestFaultFixtureImageTagIncludesFactoryRevisionAndDirtyIdentity(t *testing.T) {
	first := faultFixtureImageRef(strings.Repeat("a", 40), "same-dirty-content")
	if got := faultFixtureImageRef(strings.Repeat("a", 40), "same-dirty-content"); got != first {
		t.Fatalf("identical factory input changed tag: %q != %q", got, first)
	}
	if got := faultFixtureImageRef(strings.Repeat("b", 40), "same-dirty-content"); got == first {
		t.Fatalf("different clean factory revision reused tag %q", got)
	}
	if got := faultFixtureImageRef(strings.Repeat("a", 40), "different-dirty-content"); got == first {
		t.Fatalf("different dirty identity reused tag %q", got)
	}
}

func TestDecodeComposeContainersAcceptsArrayAndJSONLines(t *testing.T) {
	const first = `{"Service":"warehouse","State":"running","Health":"healthy"}`
	const second = `{"Service":"serving","State":"running","Health":"healthy"}`
	for name, input := range map[string]string{
		"array":      "[" + first + "," + second + "]",
		"json lines": first + "\n" + second + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			containers, err := decodeComposeContainers([]byte(input))
			if err != nil || len(containers) != 2 || containers[0].Service != "warehouse" || containers[1].Service != "serving" {
				t.Fatalf("decoded containers = %#v, err=%v", containers, err)
			}
		})
	}
	if _, err := decodeComposeContainers([]byte(first + "\nnot-json\n")); err == nil {
		t.Fatal("malformed Compose JSONL was accepted")
	}
}

func TestSelectedHTTPOverrideDoesNotSubstituteImages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "override.json")
	if err := writeHTTPOverride(path, cliOptions{selected: true}, "umir-http-selected", "fault-image", "ephemeral-token"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
		if _, exists := config.Services[service]["image"]; exists {
			t.Errorf("selected mode overrides the published %s image", service)
		}
	}
}

func TestCandidateImageCheckRequiresExactMetadataAndHealthcheck(t *testing.T) {
	ref := "candidate-image"
	for name, image := range map[string]string{
		"valid":               `[{"Id":"sha256:image","RepoDigests":["repo@sha256:digest"],"Config":{"Labels":{"org.opencontainers.image.source":"https://github.com/JustIkra/umir-serving","org.opencontainers.image.revision":"` + strings.Repeat("a", 40) + `"},"Healthcheck":{"Test":["CMD","/server","healthcheck"]}}}]`,
		"missing healthcheck": `[{"Id":"sha256:image","Config":{"Labels":{"org.opencontainers.image.source":"https://github.com/JustIkra/umir-serving","org.opencontainers.image.revision":"` + strings.Repeat("a", 40) + `"}}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			executor := &imageInspectExecutor{output: []byte(image)}
			err := verifyDockerImage(context.Background(), executor, t.TempDir(), ref, "https://github.com/JustIkra/umir-serving.git", strings.Repeat("a", 40), true)
			if name == "valid" && err != nil {
				t.Fatalf("valid image rejected: %v", err)
			}
			if name != "valid" && err == nil {
				t.Fatal("image without healthcheck was accepted")
			}
		})
	}
}

type imageInspectExecutor struct{ output []byte }

func (e *imageInspectExecutor) Run(_ context.Context, _ string, _ []string, _ string, _ ...string) ([]byte, []byte, error) {
	return e.output, nil, nil
}

func TestSelectedHTTPRunnerUsesIsolatedProjectAndCleansAfterAcceptanceFailure(t *testing.T) {
	factory := runnerTestFactory(t)
	var observed []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed = append(observed, r.URL.Path)
		switch r.URL.Path {
		case "/api/serving/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-serving"}`); err != nil {
				t.Errorf("write serving health: %v", err)
			}
		case "/api/warehouse/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-warehouse"}`); err != nil {
				t.Errorf("write warehouse health: %v", err)
			}
		case "/":
			if err := writeTestResponse(w, `<div id="root"></div>`); err != nil {
				t.Errorf("write frontend body: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "http://")
	evidenceDir := filepath.Join(t.TempDir(), "evidence")
	t.Setenv("UMIR_HTTP_EVIDENCE_DIR", evidenceDir)
	executor := &httpRunnerExecutor{port: port, failAcceptancePhase: "faults"}
	var stdout, stderr strings.Builder
	code := runHTTPAcceptanceWithCleanup(context.Background(), factory, cliOptions{selected: true}, executor, &stdout, &stderr, func(string, string) (string, error) { return t.TempDir(), nil }, os.RemoveAll)
	if code != 19 {
		t.Fatalf("runner exit code = %d, want preserved acceptance code 19: %s", code, stderr.String())
	}
	if !executor.downCalled {
		t.Fatal("owned Compose project was not removed after acceptance failed")
	}
	if executor.project == "" || !strings.HasPrefix(executor.project, "umir-http-") {
		t.Fatalf("Compose project name was not unique: %q", executor.project)
	}
	if slices.Contains(executor.composeDownArgs, "-v") || slices.Contains(executor.composeDownArgs, "--volumes") {
		t.Fatalf("cleanup would delete volumes: %v", executor.composeDownArgs)
	}
	for _, path := range []string{"/api/serving/healthz", "/api/warehouse/healthz", "/"} {
		if !slices.Contains(observed, path) {
			t.Errorf("finite health/smoke never requested %s; got %v", path, observed)
		}
	}
	if !strings.Contains(stdout.String(), "owned test-project logs") {
		t.Fatalf("Compose stdout logs were lost on failure: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "fault acceptance phase failed") || !strings.Contains(stderr.String(), "public-gateway scenarios failed") {
		t.Fatalf("phase failure diagnostics were omitted: %s", stderr.String())
	}
	if len(executor.acceptanceArgs) != 2 {
		t.Fatalf("acceptance phases run = %d, want normal and faults", len(executor.acceptanceArgs))
	}
	project := strings.TrimSuffix(executor.project, "-fault")
	for index, phase := range []string{"normal", "faults"} {
		args := executor.acceptanceArgs[index]
		if valueAfter(args, "--phase") != phase || valueAfter(args, "--factory-dir") != factory || valueAfter(args, "--compose-project") != project+map[string]string{"normal": "", "faults": "-fault"}[phase] {
			t.Errorf("%s harness ownership args are incorrect: %v", phase, args)
		}
		if phase == "faults" && !filepath.IsAbs(valueAfter(args, "--compose-override")) {
			t.Errorf("fault harness did not receive the owned absolute override: %v", args)
		}
	}
	entries, err := os.ReadDir(evidenceDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("evidence entries = %v, err=%v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(evidenceDir, project+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence httpRunEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.PhaseCleanup["normal"] != "complete" || evidence.PhaseCleanup["faults"] != "complete" || !evidence.Published {
		t.Fatalf("selected failure evidence = %#v", evidence)
	}
	if evidence.FailureStage != "fault-phase" || !strings.Contains(evidence.Failure, "public-gateway scenarios failed") {
		t.Fatalf("phase failure was not retained in evidence: stage=%q failure=%q", evidence.FailureStage, evidence.Failure)
	}
	if !slices.Contains(evidence.Outcomes, "H01:passed") || !slices.Contains(evidence.Outcomes, "H13:passed") || len(evidence.Observations) == 0 {
		t.Fatalf("partial phase evidence was dropped: outcomes=%v observations=%v", evidence.Outcomes, evidence.Observations)
	}
	if evidence.ServiceTests["warehouse"].Status != "passed" || evidence.ServiceTests["warehouse"].SourceSHA != strings.Repeat("a", 40) {
		t.Fatalf("warehouse service evidence missing or mismatched: %#v", evidence.ServiceTests["warehouse"])
	}
	if _, err := os.Stat(evidence.ServiceTests["warehouse"].RawJSONFile); err != nil {
		t.Fatalf("raw service test JSON missing: %v", err)
	}
}

func TestSelectedHTTPRunnerCleansPartialComposeStartAndKeepsStartStatus(t *testing.T) {
	factory := runnerTestFactory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/serving/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-serving"}`); err != nil {
				t.Errorf("write serving health: %v", err)
			}
		case "/api/warehouse/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-warehouse"}`); err != nil {
				t.Errorf("write warehouse health: %v", err)
			}
		case "/":
			if err := writeTestResponse(w, `<div id="root"></div>`); err != nil {
				t.Errorf("write frontend body: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("UMIR_HTTP_EVIDENCE_DIR", filepath.Join(t.TempDir(), "evidence"))
	executor := &httpRunnerExecutor{port: strings.TrimPrefix(server.URL, "http://"), failUp: true}
	var stdout, stderr strings.Builder
	code := runHTTPAcceptanceWithCleanup(context.Background(), factory, cliOptions{selected: true}, executor, &stdout, &stderr, func(string, string) (string, error) { return t.TempDir(), nil }, os.RemoveAll)
	if code != 23 {
		t.Fatalf("runner exit code = %d, want original partial-start code 23: %s", code, stderr.String())
	}
	if !executor.downCalled {
		t.Fatal("partial Compose start did not clean its own project")
	}
	if !strings.Contains(stdout.String(), "owned test-project logs") {
		t.Fatalf("partial-start logs were not captured from Compose stdout: %q", stdout.String())
	}
}

func TestSelectedHTTPRunnerPreservesAcceptanceCodeWhenCleanupFails(t *testing.T) {
	factory := runnerTestFactory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/serving/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-serving"}`); err != nil {
				t.Errorf("write serving health: %v", err)
			}
		case "/api/warehouse/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-warehouse"}`); err != nil {
				t.Errorf("write warehouse health: %v", err)
			}
		case "/":
			if err := writeTestResponse(w, `<div id="root"></div>`); err != nil {
				t.Errorf("write frontend body: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	evidenceDir := filepath.Join(t.TempDir(), "evidence")
	t.Setenv("UMIR_HTTP_EVIDENCE_DIR", evidenceDir)
	executor := &httpRunnerExecutor{port: strings.TrimPrefix(server.URL, "http://"), failAcceptance: true, failCleanup: true}
	var stdout, stderr strings.Builder
	code := runHTTPAcceptanceWithCleanup(context.Background(), factory, cliOptions{selected: true}, executor, &stdout, &stderr, func(string, string) (string, error) { return t.TempDir(), nil }, os.RemoveAll)
	if code != 19 {
		t.Fatalf("runner exit code = %d, want primary scenario code 19: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cleanup failed") {
		t.Fatalf("cleanup failure was hidden: %s", stderr.String())
	}
	entries, err := os.ReadDir(evidenceDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("cleanup evidence files=%v err=%v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(evidenceDir, executor.project+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence httpRunEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.PhaseCleanup["normal"] != "failed" {
		t.Fatalf("cleanup evidence = %#v", evidence.PhaseCleanup)
	}
}

func TestSelectedHTTPRunnerCleansOwnedProjectAfterCancellation(t *testing.T) {
	factory := runnerTestFactory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/serving/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-serving"}`); err != nil {
				t.Errorf("write serving health: %v", err)
			}
		case "/api/warehouse/healthz":
			if err := writeTestResponse(w, `{"status":"ok","service":"umir-warehouse"}`); err != nil {
				t.Errorf("write warehouse health: %v", err)
			}
		case "/":
			if err := writeTestResponse(w, `<div id="root"></div>`); err != nil {
				t.Errorf("write frontend body: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor := &httpRunnerExecutor{port: strings.TrimPrefix(server.URL, "http://"), cancelOnUp: cancel}
	var stdout, stderr strings.Builder
	code := runHTTPAcceptanceWithCleanup(ctx, factory, cliOptions{selected: true}, executor, &stdout, &stderr, func(string, string) (string, error) { return t.TempDir(), nil }, os.RemoveAll)
	if code == 0 {
		t.Fatal("cancelled Compose start was reported as successful")
	}
	if !executor.downCalled {
		t.Fatal("cancelled start did not clean its owned Compose project")
	}
	if slices.Contains(executor.composeDownArgs, "-v") || slices.Contains(executor.composeDownArgs, "--volumes") {
		t.Fatalf("cancellation cleanup targeted volumes: %v", executor.composeDownArgs)
	}
}

func runnerTestFactory(t *testing.T) string {
	t.Helper()
	factory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(factory, "tests", "http"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factory, "tests", "http", "go.mod"), []byte("module example.test/http\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factory, ".git"), []byte("test checkout"), 0o600); err != nil {
		t.Fatal(err)
	}
	warehouse := filepath.Join(filepath.Dir(factory), "umir-warehouse")
	if err := os.MkdirAll(warehouse, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{".git": "test checkout", "go.mod": "module github.com/JustIkra/umir-warehouse\n", "Dockerfile": "FROM scratch\n"} {
		if err := os.WriteFile(filepath.Join(warehouse, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return factory
}

func TestCandidateSourceRequiresExactCleanKnownSibling(t *testing.T) {
	parent := t.TempDir()
	factory := filepath.Join(parent, "umir-factory")
	source := filepath.Join(parent, "umir-gateway")
	if err := os.MkdirAll(factory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"go.mod": "module github.com/JustIkra/umir-gateway\n\ngo 1.26.5\n", "Dockerfile": "FROM scratch\n"} {
		if err := os.WriteFile(filepath.Join(source, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "factory-test@example.invalid"}, {"config", "user.name", "Factory Test"}, {"remote", "add", "origin", httpRepositories["gateway"]}, {"add", "go.mod", "Dockerfile"}, {"commit", "-qm", "candidate"}} {
		command := exec.Command("git", args...)
		command.Dir = source
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
	}
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = source
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	ref := strings.TrimSpace(string(output))
	if got, err := candidateSource(context.Background(), factory, "gateway", ref, filepath.Join(parent, "clone"), &osExecutor{}); err != nil || got != source {
		t.Fatalf("exact sibling commit rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("uncommitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCandidateCheckout(context.Background(), source, "gateway", ref, &osExecutor{}); err == nil {
		t.Fatal("dirty sibling checkout was accepted as exact build source")
	}
}

type httpRunnerExecutor struct {
	port                string
	project             string
	downCalled          bool
	composeDownArgs     []string
	failAcceptance      bool
	failAcceptancePhase string
	acceptanceArgs      [][]string
	failCleanup         bool
	failUp              bool
	cancelOnUp          context.CancelFunc
}

func (e *httpRunnerExecutor) Run(ctx context.Context, _ string, _ []string, name string, args ...string) ([]byte, []byte, error) {
	if name == "git" {
		switch args[0] {
		case "diff", "ls-files":
			return nil, nil, nil
		case "rev-parse":
			return []byte(strings.Repeat("a", 40)), nil, nil
		case "remote":
			return []byte(httpRepositories["warehouse"]), nil, nil
		case "status":
			return nil, nil, nil
		case "show":
			return []byte("module github.com/JustIkra/umir-warehouse\n"), nil, nil
		}
	}
	if name == "bash" {
		return nil, nil, nil
	}
	if name == "go" && slices.Contains(args, "build") {
		return nil, nil, nil
	}
	if name == "go" && slices.Contains(args, "test") {
		return []byte(strings.Join([]string{
			testEvent("run", "github.com/JustIkra/umir-warehouse/internal/application", "TestOrderAttemptDuplicateDuringOwnerExecutionReturnsPending"),
			testEvent("pass", "github.com/JustIkra/umir-warehouse/internal/application", "TestOrderAttemptDuplicateDuringOwnerExecutionReturnsPending"),
			testEvent("run", "github.com/JustIkra/umir-warehouse/internal/application", "TestOrderAttemptCachesRecheckAndRejectsCrossProposalReuse"),
			testEvent("pass", "github.com/JustIkra/umir-warehouse/internal/application", "TestOrderAttemptCachesRecheckAndRejectsCrossProposalReuse"),
			testEvent("run", "github.com/JustIkra/umir-warehouse/internal/adapters/httpapi", "TestOrderCommitPendingUsesHTTP202AndTaggedPayloadOnly"),
			testEvent("pass", "github.com/JustIkra/umir-warehouse/internal/adapters/httpapi", "TestOrderCommitPendingUsesHTTP202AndTaggedPayloadOnly"),
		}, "\n")), nil, nil
	}
	if strings.HasSuffix(name, "http-acceptance") {
		e.acceptanceArgs = append(e.acceptanceArgs, append([]string(nil), args...))
		phase := "normal"
		for index, arg := range args {
			if arg == "--phase" && index+1 < len(args) {
				phase = args[index+1]
			}
		}
		if e.failAcceptance || e.failAcceptancePhase == phase {
			id := "H01"
			if phase == "faults" {
				id = "H13"
			}
			data, marshalErr := json.Marshal(map[string]any{"outcomes": []string{id + ":passed"}, "observations": []map[string]any{{"id": id, "observations": map[string]string{"stock": "0"}}}})
			if marshalErr != nil {
				return nil, nil, marshalErr
			}
			return data, []byte("scenario failed"), testExitError{code: 19}
		}
		ids := []string{"H01", "H02", "H03", "H04", "H05", "H06", "H07", "H09", "H10", "H11", "H12", "H21", "H22", "H23", "H24"}
		if phase == "faults" {
			ids = []string{"H08", "H13", "H14", "H15", "H17", "H18", "H19", "H20"}
		}
		outcomes := make([]string, len(ids))
		for index, id := range ids {
			outcomes[index] = id + ":passed"
		}
		data, err := json.Marshal(map[string]any{"outcomes": outcomes})
		if err != nil {
			return nil, nil, err
		}
		return data, nil, nil
	}
	if name != "docker" {
		return nil, []byte("unexpected command"), errors.New("unexpected command")
	}
	if len(args) >= 3 && args[0] == "image" && args[1] == "inspect" {
		ref := args[2]
		service := "fault"
		revision := strings.Repeat("a", 40)
		for _, candidate := range []string{"warehouse", "serving", "gateway", "frontend"} {
			if strings.Contains(ref, "umir-"+candidate) {
				service = candidate
			}
		}
		payload := []byte(fmt.Sprintf(`[{"Id":"sha256:local","RepoDigests":["%s@sha256:digest"],"Config":{"Labels":{"org.opencontainers.image.source":"https://github.com/JustIkra/umir-%s","org.opencontainers.image.revision":"%s"},"Healthcheck":{"Test":["CMD","/server","healthcheck"]}}}]`, ref, service, revision))
		return payload, nil, nil
	}
	if args[0] == "inspect" && len(args) == 2 {
		service := strings.TrimPrefix(args[1], "container-")
		payload := []byte(fmt.Sprintf(`[{"Id":%q,"Image":"sha256:local","Config":{"Labels":{"com.docker.compose.service":%q}}}]`, args[1], service))
		return payload, nil, nil
	}
	if len(args) > 0 && args[0] == "build" {
		return nil, nil, nil
	}
	if len(args) > 0 && args[0] == "compose" {
		for i, arg := range args {
			if arg == "--project-name" && i+1 < len(args) {
				e.project = args[i+1]
			}
		}
		last := args[len(args)-1]
		switch {
		case slices.Contains(args, "ps") && slices.Contains(args, "-q"):
			return []byte("container-" + last + "\n"), nil, nil
		case slices.Contains(args, "ps"):
			containers := make([]map[string]string, 0, 6)
			services := []string{"warehouse", "serving", "gateway", "frontend"}
			if strings.HasSuffix(e.project, "-fault") {
				services = append(services, "http-fault-serving", "http-fault-warehouse")
			}
			for _, service := range services {
				containers = append(containers, map[string]string{"Service": service, "State": "running", "Health": "healthy"})
			}
			data, err := json.Marshal(containers)
			if err != nil {
				return nil, nil, err
			}
			return data, nil, nil
		case last == "--format" || len(args) > 3 && slices.Contains(args, "--format"):
			services := map[string]composeService{}
			for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
				services[service] = composeService{Image: "ghcr.io/justikra/umir-" + service + ":sha-" + strings.Repeat("a", 40)}
			}
			data, err := json.Marshal(map[string]any{"services": services})
			if err != nil {
				return nil, nil, err
			}
			return data, nil, nil
		case slices.Contains(args, "port"):
			return []byte(e.port + "\n"), nil, nil
		case slices.Contains(args, "up"):
			if e.cancelOnUp != nil {
				e.cancelOnUp()
				return nil, []byte("Compose start cancelled"), ctx.Err()
			}
			if e.failUp {
				return nil, []byte("partial Compose start"), testExitError{code: 23}
			}
			return nil, nil, nil
		case slices.Contains(args, "down"):
			e.downCalled = true
			e.composeDownArgs = append([]string(nil), args...)
			if e.failCleanup {
				return nil, []byte("cleanup failed"), testExitError{code: 31}
			}
			return nil, nil, nil
		case slices.Contains(args, "logs"):
			return []byte("owned test-project logs"), nil, nil
		}
	}
	return nil, []byte(fmt.Sprint(args)), nil
}

func valueAfter(args []string, key string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key {
			return args[index+1]
		}
	}
	return ""
}

type testExitError struct{ code int }

func (e testExitError) Error() string { return "child process failed" }
func (e testExitError) ExitCode() int { return e.code }
