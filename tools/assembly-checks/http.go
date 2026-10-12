package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var httpRepositories = map[string]string{
	"warehouse": "https://github.com/JustIkra/umir-warehouse.git",
	"serving":   "https://github.com/JustIkra/umir-serving.git",
	"gateway":   "https://github.com/JustIkra/umir-gateway.git",
	"frontend":  "https://github.com/JustIkra/umir-frontend.git",
	"feedback":  feedbackRepository + ".git",
}

type httpRunEvidence struct {
	Mode                 string                         `json:"mode"`
	Auth                 bool                           `json:"auth,omitempty"`
	Feedback             bool                           `json:"feedback,omitempty"`
	Project              string                         `json:"project"`
	Projects             map[string]string              `json:"projects"`
	Sources              map[string]string              `json:"sourceSHAs"`
	ServiceTests         map[string]serviceTestEvidence `json:"serviceTests,omitempty"`
	Images               map[string]imageEvidence       `json:"images"`
	Outcomes             []string                       `json:"outcomes,omitempty"`
	Observations         []json.RawMessage              `json:"observations,omitempty"`
	PhaseCleanup         map[string]string              `json:"phaseCleanup"`
	ContainerIDs         map[string]map[string]string   `json:"containerIDs,omitempty"`
	Smoke                string                         `json:"smoke,omitempty"`
	Cleanup              string                         `json:"cleanup"`
	Published            bool                           `json:"published"`
	FactoryDirtySHA256   string                         `json:"factoryDirtySHA256"`
	RedactedComposeFacts []string                       `json:"redactedComposeFacts,omitempty"`
	FailureStage         string                         `json:"failureStage,omitempty"`
	Failure              string                         `json:"failure,omitempty"`
	Stage                string                         `json:"stage"`
}

type composeContainerReport struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
}

type serviceTestEvidence struct {
	SourceSHA   string   `json:"sourceSHA"`
	Status      string   `json:"status"`
	TestNames   []string `json:"testNames"`
	RawJSONFile string   `json:"rawJSONFile"`
	StderrFile  string   `json:"stderrFile,omitempty"`
	Failure     string   `json:"failure,omitempty"`
}

type imageEvidence struct {
	Ref         string   `json:"ref"`
	ID          string   `json:"id"`
	Digests     []string `json:"digests,omitempty"`
	Source      string   `json:"source,omitempty"`
	Revision    string   `json:"revision,omitempty"`
	Healthcheck []string `json:"healthcheck,omitempty"`
}

type dockerImage struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
	Config      struct {
		Labels      map[string]string `json:"Labels"`
		Healthcheck *struct {
			Test []string `json:"Test"`
		} `json:"Healthcheck"`
	} `json:"Config"`
}

func runHTTPAcceptance(ctx context.Context, factoryDir string, options cliOptions, executor commandExecutor, stdout, stderr io.Writer) int {
	return runHTTPAcceptanceWithCleanup(ctx, factoryDir, options, executor, stdout, stderr, os.MkdirTemp, os.RemoveAll)
}

func runHTTPAcceptanceWithCleanup(ctx context.Context, factoryDir string, options cliOptions, executor commandExecutor, stdout, stderr io.Writer, createTemp func(string, string) (string, error), removeTemp func(string) error) (code int) {
	if options.candidate == options.selected {
		writeFailure(stderr, "choose exactly one HTTP acceptance mode\n")
		return 2
	}
	testModule := filepath.Join(factoryDir, "tests", "http")
	if _, err := os.Stat(filepath.Join(testModule, "go.mod")); err != nil {
		writeFailure(stderr, "HTTP test module is unavailable: %v\n", err)
		return 1
	}
	tempDir, err := createTemp("", "umir-http-acceptance-")
	if err != nil {
		writeFailure(stderr, "create isolated HTTP acceptance workspace: %v\n", err)
		return 1
	}
	defer func() {
		if err := removeTemp(tempDir); err != nil {
			if code == 0 {
				code = 1
			}
			writeFailure(stderr, "remove owned HTTP acceptance workspace: %v\n", err)
		}
	}()
	project, err := uniqueComposeProject()
	if err != nil {
		writeFailure(stderr, "create Compose project name: %v\n", err)
		return 1
	}
	evidence := httpRunEvidence{Project: project, Projects: make(map[string]string), Sources: make(map[string]string), ServiceTests: make(map[string]serviceTestEvidence), Images: make(map[string]imageEvidence), PhaseCleanup: make(map[string]string), ContainerIDs: make(map[string]map[string]string), Cleanup: "not-started"}
	defer func() {
		if code != 0 && evidence.Failure == "" {
			evidence.FailureStage = evidence.Stage
			evidence.Failure = fmt.Sprintf("HTTP acceptance exited with status %d at stage %q; see runner stderr and owned Compose logs", code, evidence.Stage)
		}
		if err := writeHTTPEvidence(factoryDir, evidence); err != nil {
			if code == 0 {
				code = 1
			}
			writeFailure(stderr, "write HTTP acceptance evidence: %v\n", err)
		}
	}()
	if options.auth {
		evidence.Auth = true
		evidence.Stage = "verify-auth-marker"
		if err := verifyAuthMarker(factoryDir); err != nil {
			writeFailure(stderr, "auth candidate marker: %v\n", err)
			return 2
		}
	}
	if options.feedbackRef != "" {
		evidence.Feedback = true
		evidence.Stage = "verify-feedback-marker"
		if err := verifyFeedbackMarker(factoryDir); err != nil {
			writeFailure(stderr, "feedback candidate marker: %v\n", err)
			return 2
		}
	}
	evidence.Stage = "identify-factory"
	dirtySHA, err := factoryDirtyIdentity(ctx, factoryDir, executor)
	if err != nil {
		writeFailure(stderr, "identify factory working tree: %v\n", err)
		return 1
	}
	evidence.FactoryDirtySHA256 = dirtySHA
	evidence.Stage = "read-factory-head"
	factorySHA, err := factoryHead(ctx, factoryDir, executor)
	if err != nil {
		writeFailure(stderr, "read factory commit: %v\n", err)
		return 1
	}
	evidence.Sources["factory"] = factorySHA
	sourceDirs := make(map[string]string, 4)
	if options.candidate {
		evidence.Mode = "candidate"
		evidence.Stage = "resolve-candidate-sources"
		refs := map[string]string{"warehouse": options.warehouse, "serving": options.serving, "gateway": options.gateway, "frontend": options.frontend}
		for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
			if !isSHA(refs[service]) {
				writeFailure(stderr, "candidate %s ref must be a full lowercase commit SHA\n", service)
				return 2
			}
		}
		for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
			evidence.Sources[service] = refs[service]
			evidence.Images[service] = imageEvidence{Ref: candidateImageRef(service, refs[service], project)}
			sourceDir, err := candidateSource(ctx, factoryDir, service, refs[service], filepath.Join(tempDir, service), executor)
			if err != nil {
				writeFailure(stderr, "resolve candidate %s@%s: %v\n", service, refs[service], err)
				return processCode(err)
			}
			sourceDirs[service] = sourceDir
		}
		evidence.Stage = "warehouse-service-tests"
		if err := runWarehouseServiceEvidence(ctx, factoryDir, sourceDirs["warehouse"], refs["warehouse"], project, evidence.ServiceTests, executor); err != nil {
			writeFailure(stderr, "candidate warehouse service evidence failed: %v\n", err)
			return processCode(err)
		}
		evidence.Stage = "build-candidate-images"
		for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
			if err := buildCandidateImage(ctx, factoryDir, service, refs[service], sourceDirs[service], project, executor); err != nil {
				writeFailure(stderr, "build candidate %s@%s: %v\n", service, refs[service], err)
				return processCode(err)
			}
			imageRef := candidateImageRef(service, refs[service], project)
			image, err := inspectDockerImage(ctx, executor, factoryDir, imageRef)
			if err != nil {
				writeFailure(stderr, "inspect candidate %s@%s: %v\n", service, refs[service], err)
				return 1
			}
			evidence.Images[service] = makeImageEvidence(imageRef, image)
		}
		evidence.Published = false
	} else {
		evidence.Mode = "selected"
		evidence.Stage = "read-selected-pins"
		images, err := checkImages(ctx, factoryDir, executor)
		if err != nil {
			writeFailure(stderr, "read selected image pins: %v\n", err)
			return 1
		}
		for service, imageRef := range images {
			evidence.Sources[service] = strings.TrimPrefix(imageRef, "ghcr.io/justikra/umir-"+service+":sha-")
			evidence.Images[service] = imageEvidence{Ref: imageRef}
		}
		evidence.Stage = "selected-publication-check"
		if err := runCommand(ctx, executor, factoryDir, commandEnv(), "bash", []string{filepath.Join(factoryDir, "scripts", "check-image-sources.sh"), "-f", filepath.Join(factoryDir, "compose.yaml"), "--env-file", filepath.Join(factoryDir, ".env.example")}, stdout, stderr); err != nil {
			writeFailure(stderr, "selected publication/source check failed: %v\n", err)
			return processCode(err)
		}
		composeArgs := composeArgsBase(project, factoryDir)
		evidence.Stage = "pull-selected-images"
		if err := runCommand(ctx, executor, factoryDir, composeEnvironment(nil), "docker", append(composeArgs, "pull"), stdout, stderr); err != nil {
			writeFailure(stderr, "pull selected images failed: %v\n", err)
			return processCode(err)
		}
		evidence.Stage = "selected-image-identity-check"
		if err := runCommand(ctx, executor, factoryDir, commandEnv(), "bash", []string{filepath.Join(factoryDir, "scripts", "check-pulled-images.sh"), "-f", filepath.Join(factoryDir, "compose.yaml"), "--env-file", filepath.Join(factoryDir, ".env.example")}, stdout, stderr); err != nil {
			writeFailure(stderr, "selected image identity check failed: %v\n", err)
			return processCode(err)
		}
		for service, imageRef := range images {
			image, err := inspectDockerImage(ctx, executor, factoryDir, imageRef)
			if err != nil {
				writeFailure(stderr, "inspect selected image %s: %v\n", imageRef, err)
				return 1
			}
			if !hasRepositoryDigest(image.RepoDigests) {
				writeFailure(stderr, "selected image %s has no registry digest after pull\n", imageRef)
				return 1
			}
			evidence.Images[service] = makeImageEvidence(imageRef, image)
		}
		evidence.Published = true
		evidence.Stage = "warehouse-service-tests"
		warehouseSHA := evidence.Sources["warehouse"]
		warehouseDir, err := candidateSource(ctx, factoryDir, "warehouse", warehouseSHA, filepath.Join(tempDir, "warehouse-selected"), executor)
		if err != nil {
			writeFailure(stderr, "resolve selected warehouse test source: %v\n", err)
			return processCode(err)
		}
		if err := runWarehouseServiceEvidence(ctx, factoryDir, warehouseDir, warehouseSHA, project, evidence.ServiceTests, executor); err != nil {
			writeFailure(stderr, "selected warehouse service evidence failed: %v\n", err)
			return processCode(err)
		}
	}

	evidence.Stage = "build-fault-fixture"
	faultImage := faultFixtureImageRef(factorySHA, dirtySHA)
	if err := runCommand(ctx, executor, factoryDir, commandEnv(), "docker", []string{"build", "--file", filepath.Join(testModule, "Dockerfile"), "--tag", faultImage, factoryDir}, stdout, stderr); err != nil {
		writeFailure(stderr, "build test-only fault fixture failed: %v\n", err)
		return processCode(err)
	}
	if err := verifyDockerImage(ctx, executor, factoryDir, faultImage, "", "", false); err != nil {
		writeFailure(stderr, "test-only fault fixture image is invalid: %v\n", err)
		return 1
	}
	faultImageDetails, err := inspectDockerImage(ctx, executor, factoryDir, faultImage)
	if err != nil {
		writeFailure(stderr, "inspect fault fixture image: %v\n", err)
		return 1
	}
	evidence.Images["fault-fixture"] = makeImageEvidence(faultImage, faultImageDetails)
	if options.candidate {
		for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
			if err := verifyDockerImage(ctx, executor, factoryDir, evidence.Images[service].Ref, httpRepositories[service], evidence.Sources[service], true); err != nil {
				writeFailure(stderr, "candidate image identity check for %s failed: %v\n", service, err)
				return 1
			}
		}
	}

	var auth *authAssembly
	if options.auth {
		evidence.Stage = "prepare-operator"
		auth, err = prepareAuthAssembly(ctx, executor, factoryDir, tempDir, evidence.Images["gateway"].Ref, freeLoopbackPort)
		if err != nil {
			writeFailure(stderr, "prepare the disposable operator: %v\n", err)
			return processCode(err)
		}
	}
	if options.feedbackRef != "" {
		evidence.Stage = "prepare-feedback"
		feedback, err := prepareFeedbackAssembly(ctx, executor, factoryDir, tempDir, options.feedbackRef, faultImage)
		if err != nil {
			writeFailure(stderr, "prepare the private feedback assembly: %v\n", err)
			return processCode(err)
		}
		auth.feedback = feedback
		evidence.Sources["feedback"] = options.feedbackRef
		evidence.Sources["telegram-vpn"] = options.feedbackRef
		for name, image := range feedback.images {
			evidence.Images[name] = image
		}
	}
	acceptanceBin := filepath.Join(tempDir, "http-acceptance")
	evidence.Stage = "build-acceptance-harness"
	if err := runCommand(ctx, executor, testModule, commandEnv("GOWORK=off"), "go", []string{"build", "-o", acceptanceBin, "./cmd/acceptance"}, stdout, stderr); err != nil {
		writeFailure(stderr, "build HTTP acceptance harness failed: %v\n", err)
		return processCode(err)
	}
	faultToken, err := randomToken()
	if err != nil {
		writeFailure(stderr, "create fault fixture control token: %v\n", err)
		return 1
	}
	normalProject := project
	faultProject := project + "-fault"
	evidence.Projects["normal"] = normalProject
	evidence.Projects["faults"] = faultProject
	normalOverride := ""
	if options.candidate {
		normalOverride = filepath.Join(tempDir, "compose.normal.json")
		if err := writeHTTPImageOverride(normalOverride, options, normalProject); err != nil {
			writeFailure(stderr, "write candidate image override: %v\n", err)
			return 1
		}
	}
	faultOverride := filepath.Join(tempDir, "compose.faults.json")
	if err := writeHTTPOverride(faultOverride, options, project, faultImage, faultToken); err != nil {
		writeFailure(stderr, "write isolated fault fixture override: %v\n", err)
		return 1
	}
	evidence.Stage = "normal-phase"
	normalResult, err := runHTTPPhase(ctx, factoryDir, normalProject, normalOverride, "normal", acceptanceBin, faultToken, options, auth, &evidence, executor, stdout, stderr)
	evidence.Outcomes = append(evidence.Outcomes, normalResult.Outcomes...)
	evidence.Observations = append(evidence.Observations, normalResult.Observations...)
	if err != nil {
		evidence.FailureStage = evidence.Stage
		evidence.Failure = err.Error()
		writeFailure(stderr, "normal acceptance phase failed: %v\n", err)
		return processCode(err)
	}
	evidence.Stage = "fault-phase"
	faultResult, err := runHTTPPhase(ctx, factoryDir, faultProject, faultOverride, "faults", acceptanceBin, faultToken, options, auth, &evidence, executor, stdout, stderr)
	evidence.Outcomes = append(evidence.Outcomes, faultResult.Outcomes...)
	evidence.Observations = append(evidence.Observations, faultResult.Observations...)
	if err != nil {
		evidence.FailureStage = evidence.Stage
		evidence.Failure = err.Error()
		writeFailure(stderr, "fault acceptance phase failed: %v\n", err)
		return processCode(err)
	}
	evidence.Outcomes = append(evidence.Outcomes, "H25:passed")
	evidence.Stage = "validate-matrix"
	if err := validateHTTPOutcomes(evidence.Outcomes, options.auth, options.feedbackRef != ""); err != nil {
		writeFailure(stderr, "HTTP acceptance matrix incomplete: %v\n", err)
		return 1
	}
	evidence.Cleanup = "complete"
	evidence.Stage = "complete"
	return 0
}

func candidateImageRef(service, sha, _ string) string {
	return fmt.Sprintf("umir-factory/%s:sha-%s", service, sha)
}

func faultFixtureImageRef(factorySHA, dirtySHA string) string {
	sum := sha256.Sum256([]byte(factorySHA + "\x00" + dirtySHA))
	return "umir-factory/http-fault:sha-" + hex.EncodeToString(sum[:])
}

func uniqueComposeProject() (string, error) {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "umir-http-" + hex.EncodeToString(bytes[:]), nil
}

func randomToken() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

// composeEnvironment uses ephemeral loopback ports, or the fixed frontend port and operator settings of
// an auth run.
func composeEnvironment(auth *authAssembly) []string {
	values := []string{"UMIR_BIND=127.0.0.1", "UMIR_WEB_PORT=0", "UMIR_API_PORT=0"}
	if auth != nil {
		values = append(values, auth.composeEnv()...)
	}
	return commandEnv(values...)
}

func composeHTTPArgs(project, factoryDir string, auth *authAssembly, overridePath string) []string {
	args := []string{"compose", "--project-name", project, "--file", filepath.Join(factoryDir, "compose.yaml")}
	if auth != nil {
		args = append(args, "--file", filepath.Join(factoryDir, authOverrideFile))
		if auth.feedback != nil {
			args = append(args, "--file", filepath.Join(factoryDir, feedbackOverrideFile))
		}
	}
	if overridePath != "" {
		args = append(args, "--file", overridePath)
	}
	return append(args, "--env-file", filepath.Join(factoryDir, ".env.example"))
}

func composeArgsBase(project, factoryDir string) []string {
	return []string{"compose", "--project-name", project, "--file", filepath.Join(factoryDir, "compose.yaml"), "--env-file", filepath.Join(factoryDir, ".env.example")}
}

func runCommand(ctx context.Context, executor commandExecutor, dir string, env []string, name string, args []string, stdout, stderr io.Writer) error {
	out, errOut, err := executor.Run(ctx, dir, env, name, args...)
	if writeErr := writeAll(stdout, out); writeErr != nil {
		return writeErr
	}
	if writeErr := writeAll(stderr, errOut); writeErr != nil {
		return writeErr
	}
	return err
}

func captureComposeLogs(ctx context.Context, executor commandExecutor, dir string, env, composeArgs []string, stdout, stderr io.Writer) {
	out, errOut, err := executor.Run(ctx, dir, env, "docker", append(append([]string(nil), composeArgs...), "logs", "--no-color")...)
	if err != nil {
		writeFailure(stderr, "collect owned Compose logs: %v\n", err)
	}
	if writeErr := writeAll(stderr, errOut); writeErr != nil {
		writeFailure(stderr, "write Compose logs: %v\n", writeErr)
	}
	if writeErr := writeAll(stdout, out); writeErr != nil {
		writeFailure(stderr, "write Compose log output: %v\n", writeErr)
	}
}

type httpPhaseResult struct {
	Outcomes     []string          `json:"outcomes"`
	Observations []json.RawMessage `json:"observations"`
}

func runHTTPPhase(ctx context.Context, factoryDir, project, overridePath, phase, acceptanceBin, token string, options cliOptions, auth *authAssembly, evidence *httpRunEvidence, executor commandExecutor, stdout, stderr io.Writer) (result httpPhaseResult, resultErr error) {
	composeArgs := composeHTTPArgs(project, factoryDir, auth, overridePath)
	env := composeEnvironment(auth)
	down := []string{"down", "--remove-orphans"}
	if auth != nil {
		// The account volume holds only this run's disposable operator.
		down = append(down, "--volumes")
	}
	upAttempted := false
	defer func() {
		if !upAttempted {
			evidence.PhaseCleanup[phase] = "not-started"
			return
		}
		if resultErr != nil {
			logCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			captureComposeLogs(logCtx, executor, factoryDir, env, composeArgs, stdout, stderr)
			cancel()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cleanupErr := runCommand(cleanupCtx, executor, factoryDir, env, "docker", append(append([]string(nil), composeArgs...), down...), io.Discard, stderr)
		cancel()
		if cleanupErr != nil {
			evidence.PhaseCleanup[phase] = "failed"
			evidence.Cleanup = phase + ":failed"
			if resultErr == nil {
				resultErr = fmt.Errorf("%s project cleanup failed: %w", phase, cleanupErr)
			}
			writeFailure(stderr, "%s Compose cleanup failed: %v\n", phase, cleanupErr)
		} else {
			evidence.PhaseCleanup[phase] = "complete"
			evidence.Cleanup = phase + ":complete"
		}
	}()
	if err := runCommand(ctx, executor, factoryDir, env, "docker", append(append([]string(nil), composeArgs...), "config", "--quiet"), stdout, stderr); err != nil {
		return result, fmt.Errorf("%s Compose config failed: %w", phase, err)
	}
	upAttempted = true
	upArgs := []string{"up", "--detach", "--wait", "--wait-timeout", "120"}
	if auth != nil && auth.feedback != nil {
		// The VPN may be unhealthy without a usable synthetic subscription; intake must still start.
		upArgs = []string{"up", "--detach"}
	}
	if err := runCommand(ctx, executor, factoryDir, env, "docker", append(append([]string(nil), composeArgs...), upArgs...), stdout, stderr); err != nil {
		return result, fmt.Errorf("start %s assembly failed: %w", phase, err)
	}
	services := []string{"warehouse", "serving", "gateway", "frontend"}
	if auth != nil && auth.feedback != nil {
		services = append(services, "feedback", "telegram-vpn")
	}
	if phase == "faults" {
		services = append(services, "http-fault-serving", "http-fault-warehouse")
	}
	if auth != nil && auth.feedback != nil {
		waitCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		err := waitFeedbackTopology(waitCtx, executor, factoryDir, env, composeArgs, services)
		cancel()
		if err != nil {
			return result, fmt.Errorf("verify %s topology: %w", phase, err)
		}
	} else if err := verifyRunningTopology(ctx, executor, factoryDir, env, composeArgs, services); err != nil {
		return result, fmt.Errorf("verify %s topology: %w", phase, err)
	}
	containerIDs, err := verifyContainerImageIdentities(ctx, executor, factoryDir, env, composeArgs, services, phase, evidence.Images)
	if err != nil {
		return result, fmt.Errorf("verify %s running image identities: %w", phase, err)
	}
	evidence.ContainerIDs[phase] = containerIDs
	addresses := make(map[string]string, 4)
	published := []string{"gateway", "frontend"}
	if auth != nil {
		report, errOut, err := executor.Run(ctx, factoryDir, env, "docker", append(append([]string(nil), composeArgs...), "ps", "--format", "json")...)
		if err != nil {
			return result, fmt.Errorf("docker compose ps: %s: %w", strings.TrimSpace(string(errOut)), err)
		}
		if err := checkPublishedPorts(report, auth.port); err != nil {
			return result, fmt.Errorf("verify the %s one-port boundary: %w", phase, err)
		}
		published = []string{"frontend"}
	}
	for _, service := range published {
		address, err := composePort(ctx, executor, factoryDir, env, composeArgs, service, 8080)
		if err != nil {
			return result, err
		}
		addresses[service] = address
	}
	api := addresses["gateway"]
	if auth != nil {
		if addresses["frontend"] != auth.origin {
			return result, fmt.Errorf("frontend answers at %s, not at the configured origin %s", addresses["frontend"], auth.origin)
		}
		// The browser reaches the API only through frontend.
		api = addresses["frontend"]
	}
	controls := make(map[string]string)
	if phase == "faults" {
		for _, fixture := range []string{"http-fault-serving", "http-fault-warehouse"} {
			published, err := composePort(ctx, executor, factoryDir, env, composeArgs, fixture, 9090)
			if err != nil {
				return result, err
			}
			controls[fixture] = published
		}
	}
	if err := runSmoke(ctx, defaultHTTPClient(), smokeTargets{Web: addresses["frontend"], API: api}); err != nil {
		return result, fmt.Errorf("%s health/smoke failed: %w", phase, err)
	}
	evidence.Smoke = phase + ":passed"
	evidence.RedactedComposeFacts = append(evidence.RedactedComposeFacts, "project="+project, "phase="+phase, "api="+api, "frontend="+addresses["frontend"])
	args := []string{"--phase", phase, "--gateway-url", api}
	args = append(args, "--factory-dir", factoryDir, "--compose-project", project)
	harnessEnv := commandEnv()
	if auth != nil {
		args = append(args, "--auth", "--operator-login", auth.login, "--operator-password-file", auth.passwordFile, "--compose-override", filepath.Join(factoryDir, authOverrideFile))
		if auth.feedback != nil && phase == "normal" {
			args = append(args, "--feedback")
		}
		if auth.feedback != nil {
			args = append(args, "--compose-override", filepath.Join(factoryDir, feedbackOverrideFile))
		}
		// Restarts resolve compose.auth.yaml, which needs the same settings.
		harnessEnv = env
	}
	if overridePath != "" {
		args = append(args, "--compose-override", overridePath)
	}
	if phase == "faults" {
		args = append(args, "--serving-fault-control-url", controls["http-fault-serving"], "--warehouse-fault-control-url", controls["http-fault-warehouse"], "--control-token", token)
	}
	out, errOut, err := executor.Run(ctx, factoryDir, harnessEnv, acceptanceBin, args...)
	if writeErr := writeAll(stdout, out); writeErr != nil {
		return result, writeErr
	}
	if writeErr := writeAll(stderr, errOut); writeErr != nil {
		return result, writeErr
	}
	if len(out) > 0 {
		if decodeErr := json.Unmarshal(out, &result); decodeErr != nil {
			return result, fmt.Errorf("%s harness returned invalid evidence JSON: %w", phase, decodeErr)
		}
	}
	if err != nil {
		return result, fmt.Errorf("%s public-gateway scenarios failed: %w", phase, err)
	}
	if len(result.Outcomes) == 0 {
		return result, fmt.Errorf("%s harness returned no outcomes", phase)
	}
	return result, nil
}

func composePort(ctx context.Context, executor commandExecutor, factoryDir string, env, composeArgs []string, service string, port int) (string, error) {
	args := append(append([]string(nil), composeArgs...), "port", service, strconv.Itoa(port))
	out, errOut, err := executor.Run(ctx, factoryDir, env, "docker", args...)
	if err != nil {
		return "", fmt.Errorf("discover %s:%d: %s: %w", service, port, strings.TrimSpace(string(errOut)), err)
	}
	address, err := publishedURL(strings.TrimSpace(string(out)), service, port)
	if err != nil {
		return "", err
	}
	return address, nil
}

func verifyDockerImage(ctx context.Context, executor commandExecutor, dir, ref, source, revision string, healthcheck bool) error {
	image, err := inspectDockerImage(ctx, executor, dir, ref)
	if err != nil {
		return err
	}
	if source != "" && !strings.EqualFold(strings.TrimRight(image.Config.Labels["org.opencontainers.image.source"], "/"), strings.TrimSuffix(source, ".git")) {
		return fmt.Errorf("image %s has an unexpected OCI source label", ref)
	}
	if revision != "" && image.Config.Labels["org.opencontainers.image.revision"] != revision {
		return fmt.Errorf("image %s revision label does not match %s", ref, revision)
	}
	if healthcheck && (image.Config.Healthcheck == nil || len(image.Config.Healthcheck.Test) < 2 || image.Config.Healthcheck.Test[0] != "CMD" && image.Config.Healthcheck.Test[0] != "CMD-SHELL") {
		return fmt.Errorf("image %s has no supported HEALTHCHECK", ref)
	}
	return nil
}

func inspectDockerImage(ctx context.Context, executor commandExecutor, dir, ref string) (dockerImage, error) {
	out, errOut, err := executor.Run(ctx, dir, commandEnv(), "docker", "image", "inspect", ref)
	if err != nil {
		return dockerImage{}, fmt.Errorf("docker image inspect %s: %s: %w", ref, strings.TrimSpace(string(errOut)), err)
	}
	var images []dockerImage
	if err := json.Unmarshal(out, &images); err != nil || len(images) != 1 {
		return dockerImage{}, fmt.Errorf("invalid image inspection for %s", ref)
	}
	image := images[0]
	if image.ID == "" {
		return dockerImage{}, fmt.Errorf("image %s has no local image ID", ref)
	}
	return image, nil
}

func makeImageEvidence(ref string, image dockerImage) imageEvidence {
	evidence := imageEvidence{Ref: ref, ID: image.ID, Digests: append([]string(nil), image.RepoDigests...), Source: image.Config.Labels["org.opencontainers.image.source"], Revision: image.Config.Labels["org.opencontainers.image.revision"]}
	if image.Config.Healthcheck != nil {
		evidence.Healthcheck = append([]string(nil), image.Config.Healthcheck.Test...)
	}
	return evidence
}

func hasRepositoryDigest(digests []string) bool {
	for _, digest := range digests {
		if strings.Contains(digest, "@sha256:") {
			return true
		}
	}
	return false
}

func writeHTTPOverride(path string, options cliOptions, project, faultImage, token string) error {
	type serviceConfig map[string]any
	services := make(map[string]serviceConfig, 6)
	if options.candidate {
		refs := map[string]string{"warehouse": options.warehouse, "serving": options.serving, "gateway": options.gateway, "frontend": options.frontend}
		for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
			services[service] = serviceConfig{"image": candidateImageRef(service, refs[service], project)}
		}
	}
	serving := services["serving"]
	if serving == nil {
		serving = serviceConfig{}
	}
	serving["environment"] = map[string]string{"WAREHOUSE_INTERNAL_URL": "http://http-fault-warehouse:8082"}
	services["serving"] = serving
	gateway := services["gateway"]
	if gateway == nil {
		gateway = serviceConfig{}
	}
	gateway["environment"] = map[string]string{"SERVING_URL": "http://http-fault-serving:8080"}
	gateway["depends_on"] = map[string]any{"http-fault-serving": map[string]string{"condition": "service_healthy"}}
	services["gateway"] = gateway
	for _, fixture := range []struct{ name, upstream, listen string }{
		{"http-fault-serving", "http://serving:8080", ":8080"},
		{"http-fault-warehouse", "http://warehouse:8082", ":8082"},
	} {
		controlPort := 9090
		dep := map[string]string{"http-fault-serving": "serving", "http-fault-warehouse": "warehouse"}[fixture.name]
		services[fixture.name] = serviceConfig{
			"image":   faultImage,
			"command": []string{"--listen", fixture.listen, "--control-listen", fmt.Sprintf("0.0.0.0:%d", controlPort), "--upstream", fixture.upstream, "--control-token", token},
			"ports":   []string{fmt.Sprintf("127.0.0.1::%d", controlPort)},
			"restart": "no",
			"healthcheck": map[string]any{
				"test":     []string{"CMD", "/fault-proxy", "healthcheck", fmt.Sprintf("http://127.0.0.1:%d/healthz", controlPort)},
				"interval": "5s",
				"timeout":  "2s",
				"retries":  5,
			},
			"depends_on": map[string]any{dep: map[string]string{"condition": "service_healthy"}},
		}
	}
	content, err := json.MarshalIndent(map[string]any{"services": services}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0o600)
}

func writeHTTPImageOverride(path string, options cliOptions, project string) error {
	refs := map[string]string{"warehouse": options.warehouse, "serving": options.serving, "gateway": options.gateway, "frontend": options.frontend}
	services := make(map[string]any, len(refs))
	for service, ref := range refs {
		services[service] = map[string]string{"image": candidateImageRef(service, ref, project)}
	}
	content, err := json.MarshalIndent(map[string]any{"services": services}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0o600)
}

// validateHTTPOutcomes requires H01–H25 except the service-only H16, and A01–A04 in auth acceptance.
func validateHTTPOutcomes(outcomes []string, auth bool, feedback ...bool) error {
	want := make(map[string]bool, 28)
	for number := 1; number <= 25; number++ {
		if number == 16 {
			continue
		}
		want[fmt.Sprintf("H%02d", number)] = false
	}
	if auth {
		for number := 1; number <= 4; number++ {
			want[fmt.Sprintf("A%02d", number)] = false
		}
	}
	if len(feedback) > 0 && feedback[0] {
		for number := 1; number <= 5; number++ {
			want[fmt.Sprintf("F%02d", number)] = false
		}
	}
	for _, outcome := range outcomes {
		parts := strings.SplitN(outcome, ":", 2)
		if len(parts) != 2 || parts[1] != "passed" {
			return fmt.Errorf("invalid scenario outcome %q", outcome)
		}
		if _, exists := want[parts[0]]; !exists {
			return fmt.Errorf("unexpected or service-only scenario outcome %q", parts[0])
		}
		if want[parts[0]] {
			return fmt.Errorf("duplicate scenario outcome %s", parts[0])
		}
		want[parts[0]] = true
	}
	missing := make([]string, 0)
	for name, passed := range want {
		if !passed {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("missing required outcomes: %s", strings.Join(missing, ", "))
	}
	return nil
}

func factoryHead(ctx context.Context, factoryDir string, executor commandExecutor) (string, error) {
	head, errOut, err := executor.Run(ctx, factoryDir, commandEnv(), "git", "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %s: %w", strings.TrimSpace(string(errOut)), err)
	}
	value := strings.TrimSpace(string(head))
	if !isSHA(value) {
		return "", fmt.Errorf("invalid factory HEAD %q", value)
	}
	return value, nil
}

func writeHTTPEvidence(factoryDir string, evidence httpRunEvidence) error {
	root, err := httpEvidenceRoot(factoryDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, evidence.Project+".json"), append(data, '\n'), 0o600)
}

func httpEvidenceRoot(factoryDir string) (string, error) {
	root := os.Getenv("UMIR_HTTP_EVIDENCE_DIR")
	if root == "" {
		root = filepath.Join(filepath.Dir(factoryDir), ".memory-base", "specs", "2026-10-11-http-inmemory", "factory", "runs")
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("HTTP evidence directory must be an absolute path")
	}
	return root, nil
}

func runWarehouseServiceEvidence(ctx context.Context, factoryDir, sourceDir, sourceSHA, project string, target map[string]serviceTestEvidence, executor commandExecutor) error {
	evidence := serviceTestEvidence{
		SourceSHA: sourceSHA,
		Status:    "running",
		TestNames: []string{
			"TestOrderAttemptDuplicateDuringOwnerExecutionReturnsPending",
			"TestOrderAttemptCachesRecheckAndRejectsCrossProposalReuse",
			"TestOrderCommitPendingUsesHTTP202AndTaggedPayloadOnly",
		},
	}
	root, err := httpEvidenceRoot(factoryDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	evidence.RawJSONFile = filepath.Join(root, project+"-warehouse-service-tests.jsonl")
	evidence.StderrFile = filepath.Join(root, project+"-warehouse-service-tests.stderr.log")
	target["warehouse"] = evidence
	out, stderr, runErr := executor.Run(ctx, sourceDir, commandEnv("GOWORK=off"), "go", "test", "-race", "-count=1", "-json", "./internal/application", "./internal/adapters/httpapi")
	writeErr := os.WriteFile(evidence.RawJSONFile, out, 0o600)
	if writeErr != nil {
		evidence.Status = "failed"
		evidence.Failure = "write raw service test JSON: " + writeErr.Error()
		target["warehouse"] = evidence
		return writeErr
	}
	if len(stderr) != 0 {
		if err := os.WriteFile(evidence.StderrFile, stderr, 0o600); err != nil {
			evidence.Status = "failed"
			evidence.Failure = "write service test stderr: " + err.Error()
			target["warehouse"] = evidence
			return err
		}
	} else {
		evidence.StderrFile = ""
	}
	parseErr := validateWarehouseServiceTestJSON(out)
	if runErr != nil || parseErr != nil {
		evidence.Status = "failed"
		evidence.Failure = errors.Join(runErr, parseErr).Error()
		target["warehouse"] = evidence
		return fmt.Errorf("warehouse service tests failed: %w", errors.Join(runErr, parseErr))
	}
	evidence.Status = "passed"
	target["warehouse"] = evidence
	return nil
}

func validateWarehouseServiceTestJSON(data []byte) error {
	want := map[string]string{
		"TestOrderAttemptDuplicateDuringOwnerExecutionReturnsPending": "github.com/JustIkra/umir-warehouse/internal/application",
		"TestOrderAttemptCachesRecheckAndRejectsCrossProposalReuse":   "github.com/JustIkra/umir-warehouse/internal/application",
		"TestOrderCommitPendingUsesHTTP202AndTaggedPayloadOnly":       "github.com/JustIkra/umir-warehouse/internal/adapters/httpapi",
	}
	seenRun := make(map[string]bool, len(want))
	seenPass := make(map[string]bool, len(want))
	seenSkip := make(map[string]bool, len(want))
	seenFail := make(map[string]bool, len(want))
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		var event struct {
			Action  string `json:"Action"`
			Package string `json:"Package"`
			Test    string `json:"Test"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("decode go test JSON event: %w", err)
		}
		if event.Test == "" {
			continue
		}
		packagePath, required := want[event.Test]
		if !required {
			continue
		}
		if event.Package != packagePath {
			return fmt.Errorf("required test %s ran in unexpected package %s", event.Test, event.Package)
		}
		switch event.Action {
		case "run":
			seenRun[event.Test] = true
		case "pass":
			seenPass[event.Test] = true
		case "skip":
			seenSkip[event.Test] = true
		case "fail":
			seenFail[event.Test] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read go test JSON: %w", err)
	}
	missing := make([]string, 0)
	for name := range want {
		if !seenRun[name] || !seenPass[name] || seenSkip[name] || seenFail[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		return fmt.Errorf("required tests did not each run and pass: %s", strings.Join(missing, ", "))
	}
	return nil
}

func candidateSource(ctx context.Context, factoryDir, service, ref, destination string, executor commandExecutor) (string, error) {
	if !isSHA(ref) {
		return "", errors.New("ref must be exactly 40 lowercase hexadecimal characters")
	}
	sibling := filepath.Join(filepath.Dir(factoryDir), "umir-"+service)
	if verifyCandidateCheckout(ctx, sibling, service, ref, executor) == nil {
		return sibling, nil
	}
	if _, _, err := executor.Run(ctx, filepath.Dir(destination), commandEnv(), "git", "clone", "--no-checkout", "--", httpRepositories[service], destination); err != nil {
		return "", fmt.Errorf("clone known remote: %w", err)
	}
	if _, _, err := executor.Run(ctx, destination, commandEnv(), "git", "fetch", "--depth=1", "origin", ref); err != nil {
		return "", fmt.Errorf("fetch exact commit: %w", err)
	}
	if _, _, err := executor.Run(ctx, destination, commandEnv(), "git", "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return "", fmt.Errorf("checkout exact commit: %w", err)
	}
	if err := verifyCandidateCheckout(ctx, destination, service, ref, executor); err != nil {
		return "", err
	}
	return destination, nil
}

func verifyRunningTopology(ctx context.Context, executor commandExecutor, factoryDir string, env, composeArgs []string, expectedServices []string, unhealthyAllowed ...string) error {
	out, errOut, err := executor.Run(ctx, factoryDir, env, "docker", append(append([]string(nil), composeArgs...), "ps", "--format", "json")...)
	if err != nil {
		return fmt.Errorf("docker compose ps: %s: %w", strings.TrimSpace(string(errOut)), err)
	}
	containers, err := decodeComposeContainers(out)
	if err != nil {
		return fmt.Errorf("decode Compose container report: %w", err)
	}
	want := make(map[string]bool, len(expectedServices))
	allowed := make(map[string]bool, len(unhealthyAllowed))
	for _, service := range unhealthyAllowed {
		allowed[service] = true
	}
	for _, service := range expectedServices {
		want[service] = false
	}
	if len(containers) != len(want) {
		return fmt.Errorf("compose started %d containers, want exactly %d including two test fixtures", len(containers), len(want))
	}
	for _, container := range containers {
		if _, ok := want[container.Service]; !ok || want[container.Service] {
			return fmt.Errorf("unexpected or duplicate Compose service %q", container.Service)
		}
		want[container.Service] = true
		if container.State != "running" || container.Health != "healthy" && !allowed[container.Service] {
			return fmt.Errorf("service %s state=%q health=%q, want running/healthy", container.Service, container.State, container.Health)
		}
	}
	for service, present := range want {
		if !present {
			return fmt.Errorf("compose omitted service %s", service)
		}
	}
	return nil
}

func waitFeedbackTopology(ctx context.Context, executor commandExecutor, factoryDir string, env, composeArgs, expectedServices []string) error {
	var lastErr error
	for {
		lastErr = verifyRunningTopology(ctx, executor, factoryDir, env, composeArgs, expectedServices, "telegram-vpn")
		if lastErr == nil {
			return nil
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("feedback application services did not become ready: %w (last check: %w)", ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
}

func decodeComposeContainers(data []byte) ([]composeContainerReport, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("empty container report")
	}
	var containers []composeContainerReport
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &containers); err != nil {
			return nil, err
		}
	} else {
		scanner := bufio.NewScanner(bytes.NewReader(trimmed))
		scanner.Buffer(make([]byte, 4096), 4*1024*1024)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				return nil, errors.New("empty line in Compose JSONL report")
			}
			var container composeContainerReport
			if err := json.Unmarshal(line, &container); err != nil {
				return nil, fmt.Errorf("decode Compose JSONL record: %w", err)
			}
			containers = append(containers, container)
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read Compose JSONL report: %w", err)
		}
	}
	if len(containers) == 0 {
		return nil, errors.New("compose report contains no containers")
	}
	for i, container := range containers {
		if strings.TrimSpace(container.Service) == "" || strings.TrimSpace(container.State) == "" || strings.TrimSpace(container.Health) == "" {
			return nil, fmt.Errorf("compose container record %d omits service, state, or health", i)
		}
	}
	return containers, nil
}

func verifyContainerImageIdentities(ctx context.Context, executor commandExecutor, factoryDir string, env, composeArgs []string, services []string, phase string, images map[string]imageEvidence) (map[string]string, error) {
	ids := make(map[string]string, len(services))
	for _, service := range services {
		psArgs := append(append([]string(nil), composeArgs...), "ps", "-q", service)
		out, errOut, err := executor.Run(ctx, factoryDir, env, "docker", psArgs...)
		if err != nil {
			return nil, fmt.Errorf("discover %s container: %s: %w", service, strings.TrimSpace(string(errOut)), err)
		}
		containerID := strings.TrimSpace(string(out))
		if containerID == "" || strings.Contains(containerID, "\n") {
			return nil, fmt.Errorf("expected one running %s container", service)
		}
		inspect, inspectErr, err := executor.Run(ctx, factoryDir, commandEnv(), "docker", "inspect", containerID)
		if err != nil {
			return nil, fmt.Errorf("inspect %s container: %s: %w", service, strings.TrimSpace(string(inspectErr)), err)
		}
		var containers []struct {
			ID     string `json:"Id"`
			Image  string `json:"Image"`
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"Config"`
		}
		if err := json.Unmarshal(inspect, &containers); err != nil || len(containers) != 1 {
			return nil, fmt.Errorf("invalid docker inspect output for %s", service)
		}
		container := containers[0]
		imageKey := service
		if strings.HasPrefix(service, "http-fault-") {
			imageKey = "fault-fixture"
		}
		expected := images[imageKey]
		if expected.ID == "" || container.Image != expected.ID {
			return nil, fmt.Errorf("running %s uses image ID %s, expected %s", service, container.Image, expected.ID)
		}
		if container.Config.Labels["com.docker.compose.service"] != service {
			return nil, fmt.Errorf("container %s has the wrong Compose service identity", service)
		}
		ids[service] = container.ID
	}
	if phase == "normal" {
		want := 4
		if images["feedback"].ID != "" || images["telegram-vpn"].ID != "" {
			want = 6
		}
		if len(ids) != want {
			return nil, fmt.Errorf("normal phase has %d service containers, want %d", len(ids), want)
		}
	}
	return ids, nil
}

func factoryDirtyIdentity(ctx context.Context, factoryDir string, executor commandExecutor) (string, error) {
	diff, _, err := executor.Run(ctx, factoryDir, commandEnv(), "git", "diff", "--binary", "HEAD", "--")
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	files, _, err := executor.Run(ctx, factoryDir, commandEnv(), "git", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", fmt.Errorf("git ls-files: %w", err)
	}
	paths := strings.Split(string(files), "\x00")
	sort.Strings(paths)
	hash := sha256.New()
	if err := writeAll(hash, diff); err != nil {
		return "", err
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(factoryDir, filepath.Clean(path)))
		if err != nil {
			return "", fmt.Errorf("read untracked file %s: %w", path, err)
		}
		if err := writeAll(hash, []byte(path+"\x00")); err != nil {
			return "", err
		}
		if err := writeAll(hash, content); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyCandidateCheckout(ctx context.Context, dir, service, expected string, executor commandExecutor) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return err
	}
	if _, err := verifyHead(ctx, executor, dir, expected); err != nil {
		return err
	}
	origin, _, err := executor.Run(ctx, dir, commandEnv(), "git", "remote", "get-url", "origin")
	if err != nil || !knownOrigin(service, strings.TrimSpace(string(origin))) {
		return fmt.Errorf("candidate checkout has an unknown origin")
	}
	status, _, err := executor.Run(ctx, dir, commandEnv(), "git", "status", "--porcelain", "--untracked-files=all")
	if err != nil || len(bytesTrimSpace(status)) != 0 {
		return fmt.Errorf("candidate checkout is not clean")
	}
	if service != "frontend" {
		module, _, err := executor.Run(ctx, dir, commandEnv(), "git", "show", "HEAD:go.mod")
		if err != nil || !strings.Contains(string(module), "module github.com/JustIkra/umir-"+service+"\n") {
			return fmt.Errorf("candidate module path does not match %s", service)
		}
	} else if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return fmt.Errorf("frontend package manifest is missing: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil {
		return fmt.Errorf("candidate Dockerfile is missing: %w", err)
	}
	return nil
}

func knownOrigin(service, value string) bool {
	value = strings.TrimSuffix(strings.TrimSuffix(value, ".git"), "/")
	return strings.EqualFold(value, strings.TrimSuffix(strings.TrimSuffix(httpRepositories[service], ".git"), "/")) || strings.EqualFold(value, "git@github.com:JustIkra/umir-"+service)
}

func bytesTrimSpace(value []byte) []byte { return []byte(strings.TrimSpace(string(value))) }

func buildCandidateImage(ctx context.Context, factoryDir, service, ref, sourceDir, project string, executor commandExecutor) error {
	tag := candidateImageRef(service, ref, project)
	source := strings.TrimSuffix(httpRepositories[service], ".git")
	args := []string{"build", "--label", "org.opencontainers.image.source=" + source, "--label", "org.opencontainers.image.revision=" + ref, "--tag", tag, sourceDir}
	_, errOut, err := executor.Run(ctx, factoryDir, commandEnv(), "docker", args...)
	if err != nil {
		return fmt.Errorf("docker build: %s: %w", strings.TrimSpace(string(errOut)), err)
	}
	return nil
}

func init() { _ = json.Valid }
