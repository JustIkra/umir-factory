package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type backendEvidence struct {
	Mode               string                           `json:"mode"`
	Project            string                           `json:"project"`
	Published          bool                             `json:"published"`
	ContractIdentity   string                           `json:"contractIdentity"`
	InputsSHA256       string                           `json:"inputsSHA256"`
	HarnessSHA256      string                           `json:"harnessSHA256"`
	RequestedRefs      map[string]string                `json:"requestedRefs"`
	FactoryRef         string                           `json:"factoryRef"`
	FactoryDirtySHA256 string                           `json:"factoryDirtySHA256"`
	RunnerBinarySHA256 string                           `json:"runnerBinarySHA256"`
	Sources            map[string]backendSourceEvidence `json:"sources"`
	Images             map[string]imageEvidence         `json:"images"`
	BuildRefs          map[string]string                `json:"buildRefs"`
	Containers         []backendContainer               `json:"containers,omitempty"`
	RedactedTopology   map[string]any                   `json:"redactedTopology,omitempty"`
	Required           []string                         `json:"required,omitempty"`
	Passed             []string                         `json:"passed,omitempty"`
	Stage              string                           `json:"stage"`
	Stages             []string                         `json:"stages"`
	Cleanup            string                           `json:"cleanup"`
	ImageCleanup       string                           `json:"imageCleanup"`
	WorkspaceCleanup   string                           `json:"workspaceCleanup"`
	ExitCode           int                              `json:"exitCode"`
	Failure            string                           `json:"failure,omitempty"`
	FixtureOwners      map[string]backendRunOwner       `json:"fixtureOwners,omitempty"`
	FixtureProtocol    string                           `json:"fixtureProtocol,omitempty"`
	BindingsSHA256     string                           `json:"bindingsSHA256,omitempty"`
	FixtureInstances   map[string]string                `json:"fixtureInstances,omitempty"`
	PrivateServiceURLs map[string]string                `json:"privateServiceURLs,omitempty"`
}
type backendRun struct {
	Version            int                              `json:"version"`
	ContractIdentity   string                           `json:"contractIdentity"`
	Project            string                           `json:"project"`
	ComposeFile        string                           `json:"composeFile"`
	GatewayURL         string                           `json:"gatewayURL"`
	Origin             string                           `json:"origin"`
	SinkURLs           map[string]string                `json:"sinkURLs"`
	PrivateServiceURLs map[string]string                `json:"privateServiceURLs,omitempty"`
	Secrets            map[string]string                `json:"secrets,omitempty"`
	CredentialFiles    map[string]backendFileRef        `json:"credentialFiles,omitempty"`
	Fixtures           *backendRunFixtures              `json:"fixtures,omitempty"`
	Requirements       []string                         `json:"requirements"`
	EvidenceDirectory  string                           `json:"evidenceDirectory"`
	Sources            map[string]backendSourceEvidence `json:"sources"`
	Images             map[string]imageEvidence         `json:"images"`
}
type backendResult struct {
	Status           string   `json:"status"`
	ContractIdentity string   `json:"contractIdentity"`
	Passed           []string `json:"passed"`
}

func runBackendAcceptance(ctx context.Context, factoryDir string, o cliOptions, executor commandExecutor, stdout, stderr io.Writer) (code int) {
	if err := validateBackendOptions(o); err != nil {
		writeFailure(stderr, "%v\n", err)
		return 2
	}
	if err := os.MkdirAll(o.backendEvidenceDir, 0o700); err != nil {
		writeFailure(stderr, "create backend evidence directory: %v\n", err)
		return 1
	}
	token, err := randomToken()
	if err != nil {
		writeFailure(stderr, "generate backend run identity\n")
		return 1
	}
	project := "umir-backend-" + token[:24]
	evidenceDir := filepath.Join(o.backendEvidenceDir, project)
	if err := os.Mkdir(evidenceDir, 0o700); err != nil {
		writeFailure(stderr, "create unique backend evidence directory: %v\n", err)
		return 1
	}
	evidencePath := filepath.Join(evidenceDir, "evidence.json")
	ev := backendEvidence{Mode: "backend-candidate", Project: project, ContractIdentity: backendContractIdentity, InputsSHA256: o.backendInputsSHA, HarnessSHA256: o.backendHarnessSHA, RequestedRefs: backendRefs(o), Sources: map[string]backendSourceEvidence{}, Images: map[string]imageEvidence{}, BuildRefs: map[string]string{}, Cleanup: "not-started", ImageCleanup: "not-started", WorkspaceCleanup: "not-created", Stage: "inputs"}
	if err := writeBackendJSON(evidencePath, ev); err != nil {
		writeFailure(stderr, "write initial backend evidence: %v\n", err)
		return 1
	}
	var temp, compose string
	var input backendInputs
	started := false
	createdImages := []string{}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if started {
			containers, err := backendContainers(cleanupCtx, factoryDir, project, compose, input, ev.Images, executor, false)
			if err == nil {
				ev.Containers = containers
			}
			if err := cleanupBackendProject(cleanupCtx, factoryDir, project, compose, input, executor); err != nil {
				ev.Cleanup = "failed"
				if code == 0 {
					code = 1
				}
				writeFailure(stderr, "backend project cleanup failed; recorded project %s\n", project)
			} else {
				ev.Cleanup = "removed"
			}
		}
		ev.ImageCleanup = "removed"
		for _, ref := range createdImages {
			out, _, err := executor.Run(cleanupCtx, factoryDir, commandEnv(), "docker", "image", "ls", "--quiet", ref)
			if err != nil {
				ev.ImageCleanup = "failed"
				if code == 0 {
					code = 1
				}
				continue
			}
			if len(bytes.TrimSpace(out)) == 0 {
				continue
			}
			image, err := inspectDockerImage(cleanupCtx, executor, factoryDir, ref)
			if err != nil || image.Config.Labels["io.umir.backend.run"] != project {
				ev.ImageCleanup = "failed"
				if code == 0 {
					code = 1
				}
				continue
			}
			if _, _, err := executor.Run(cleanupCtx, factoryDir, commandEnv(), "docker", "image", "rm", ref); err != nil {
				ev.ImageCleanup = "failed"
				if code == 0 {
					code = 1
				}
			}
			out, _, err = executor.Run(cleanupCtx, factoryDir, commandEnv(), "docker", "image", "ls", "--quiet", ref)
			if err != nil || len(bytes.TrimSpace(out)) > 0 {
				ev.ImageCleanup = "failed"
				if code == 0 {
					code = 1
				}
			}
		}
		if temp != "" {
			if err := os.RemoveAll(temp); err != nil {
				ev.WorkspaceCleanup = "failed"
				if code == 0 {
					code = 1
				}
			} else {
				ev.WorkspaceCleanup = "removed"
			}
		}
		ev.ExitCode = code
		if code != 0 {
			ev.Failure = fmt.Sprintf("backend acceptance exited %d at %s", code, ev.Stage)
		}
		if err := writeBackendJSON(evidencePath, ev); err != nil {
			if code == 0 {
				code = 1
			}
			writeFailure(stderr, "write final backend evidence failed\n")
		}
	}()
	stage := func(name string) error {
		ev.Stage = name
		ev.Stages = append(ev.Stages, name)
		return writeBackendJSON(evidencePath, ev)
	}
	input, err = readBackendInputs(o)
	if err != nil {
		writeFailure(stderr, "backend inputs: %v\n", err)
		return 2
	}
	ev.Required = slices.Clone(input.Requirements)
	if err := stage("factory-identity"); err != nil {
		return 1
	}
	factoryDir, err = absoluteFactoryDir(factoryDir)
	if err != nil {
		return 1
	}
	ev.FactoryRef, err = factoryHead(ctx, factoryDir, executor)
	if err != nil {
		return 1
	}
	ev.FactoryDirtySHA256, err = factoryDirtyIdentity(ctx, factoryDir, executor)
	if err != nil {
		return 1
	}
	executable, err := os.Executable()
	if err != nil {
		return 1
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return 1
	}
	ev.RunnerBinarySHA256 = backendDigest(binary)
	if err := stage("workspace"); err != nil {
		return 1
	}
	temp, err = os.MkdirTemp("", project+"-")
	if err != nil {
		return 1
	}
	ev.WorkspaceCleanup = "pending"
	if err := stage("harness-identity"); err != nil {
		return 1
	}
	harnessData, err := os.ReadFile(o.backendHarness)
	if err != nil || backendDigest(harnessData) != o.backendHarnessSHA {
		writeFailure(stderr, "backend harness identity differs from reviewed binary\n")
		return 2
	}
	harness := filepath.Join(temp, "harness")
	if err := os.WriteFile(harness, harnessData, 0o700); err != nil {
		return 1
	}
	contexts := map[string]string{}
	for _, name := range backendServices {
		if err := stage("source-" + name); err != nil {
			return 1
		}
		destination := filepath.Join(temp, "contexts", name)
		identity, err := snapshotBackendSource(ctx, name, input.Services[name], destination, executor)
		if err != nil {
			writeFailure(stderr, "%s source: %v\n", name, err)
			return 2
		}
		ev.Sources[name] = identity
		contexts[name] = destination
	}
	if input.Fixtures != nil && input.Fixtures.SinkSource != nil {
		if err := stage("source-sinks"); err != nil {
			return 1
		}
		source := input.Fixtures.SinkSource
		destination := filepath.Join(temp, "contexts", "sinks")
		identity, err := snapshotBackendSource(ctx, "factory", backendSource{Path: source.Path, Ref: source.Ref, SourceMode: source.SourceMode, Files: source.Files, Dockerfile: source.Dockerfile}, destination, executor)
		if err != nil {
			writeFailure(stderr, "sink source: %v\n", err)
			return 2
		}
		ev.Sources["sinks"], contexts["sinks"] = identity, destination
	}
	for _, name := range backendServices {
		ref := "umir-backend/" + project + "-" + name + ":candidate"
		ev.BuildRefs[name] = ref
		if err := stage("build-" + name); err != nil {
			return 1
		}
		source := input.Services[name]
		identity := ev.Sources[name]
		args := []string{"build", "--tag", ref, "--file", filepath.Join(contexts[name], source.Dockerfile), "--label", "io.umir.backend.run=" + project, "--label", "io.umir.backend.context-sha256=" + identity.ContextSHA256, "--label", "org.opencontainers.image.source=https://github.com/JustIkra/umir-" + name, "--label", "org.opencontainers.image.revision=" + source.Ref, contexts[name]}
		createdImages = append(createdImages, ref)
		if _, _, err := executor.Run(ctx, factoryDir, commandEnv(), "docker", args...); err != nil {
			writeFailure(stderr, "backend build %s failed\n", name)
			return processCode(err)
		}
		image, err := inspectDockerImage(ctx, executor, factoryDir, ref)
		if err != nil || image.ID == "" || image.Config.Labels["io.umir.backend.run"] != project || image.Config.Labels["io.umir.backend.context-sha256"] != identity.ContextSHA256 || image.Config.Labels["org.opencontainers.image.revision"] != source.Ref {
			writeFailure(stderr, "backend build identity %s unavailable or mismatched\n", name)
			return 1
		}
		ev.Images[name] = makeImageEvidence(ref, image)
	}
	if input.Fixtures != nil && input.Fixtures.SinkSource != nil {
		for _, name := range []string{"smtp", "notification"} {
			ref := "umir-backend/" + project + "-" + name + ":candidate"
			ev.BuildRefs[name] = ref
			if err := stage("build-" + name); err != nil {
				return 1
			}
			source, identity := input.Fixtures.SinkSource, ev.Sources["sinks"]
			args := []string{"build", "--tag", ref, "--file", filepath.Join(contexts["sinks"], source.Dockerfile), "--build-arg", "SINK=" + name, "--label", "io.umir.backend.run=" + project, "--label", "io.umir.backend.context-sha256=" + identity.ContextSHA256, "--label", "org.opencontainers.image.source=https://github.com/JustIkra/umir-factory", "--label", "org.opencontainers.image.revision=" + source.Ref, contexts["sinks"]}
			createdImages = append(createdImages, ref)
			if _, _, err := executor.Run(ctx, factoryDir, commandEnv(), "docker", args...); err != nil {
				return processCode(err)
			}
			image, err := inspectDockerImage(ctx, executor, factoryDir, ref)
			if err != nil || !strings.HasPrefix(image.ID, "sha256:") || !isSHA256(strings.TrimPrefix(image.ID, "sha256:")) || image.Config.Labels["io.umir.backend.run"] != project || image.Config.Labels["io.umir.backend.context-sha256"] != identity.ContextSHA256 || image.Config.Labels["org.opencontainers.image.revision"] != source.Ref {
				return 1
			}
			ev.Images[name] = makeImageEvidence(ref, image)
		}
	}
	if err := stage("topology"); err != nil {
		return 1
	}
	secrets, err := backendSecrets()
	if err != nil {
		return 1
	}
	port, err := freeLoopbackPort()
	if err != nil {
		return 1
	}
	origin := "http://127.0.0.1:" + strconv.Itoa(port)
	var fixtures *backendRunFixtures
	var credentialFiles map[string]backendFileRef
	if input.Version == 2 {
		fixtures, credentialFiles, err = prepareBackendFixtures(temp, project, input, secrets, origin)
		if err != nil {
			return 1
		}
		ev.FixtureProtocol, ev.BindingsSHA256 = fixtures.Protocol, fixtures.BindingsSHA256
	}
	compose, ev.RedactedTopology, err = writeBackendTopology(temp, project, input, ev.Images, secrets, origin, port)
	if err != nil {
		return 1
	}
	if err := stage("compose-config"); err != nil {
		return 1
	}
	args := backendComposeArgs(project, compose)
	if _, _, err := executor.Run(ctx, factoryDir, commandEnv(), "docker", append(slices.Clone(args), "config", "--quiet")...); err != nil {
		return processCode(err)
	}
	if err := stage("start"); err != nil {
		return 1
	}
	started = true
	ev.Cleanup = "pending"
	if err := writeBackendJSON(evidencePath, ev); err != nil {
		return 1
	}
	if _, _, err := executor.Run(ctx, factoryDir, commandEnv(), "docker", append(slices.Clone(args), "up", "--detach", "--wait", "--wait-timeout", "60")...); err != nil {
		return processCode(err)
	}
	if err := stage("containers"); err != nil {
		return 1
	}
	for _, name := range []string{"smtp", "notification"} {
		if _, built := ev.Images[name]; built {
			continue
		}
		ref := input.Sinks[name].Image
		image, err := inspectDockerImage(ctx, executor, factoryDir, ref)
		if err != nil || image.ID == "" {
			return 1
		}
		ev.Images[name] = makeImageEvidence(ref, image)
	}
	ev.Containers, err = backendContainers(ctx, factoryDir, project, compose, input, ev.Images, executor, true)
	if err != nil {
		writeFailure(stderr, "backend topology: %v\n", err)
		return 1
	}
	if input.Version == 2 {
		if err := stage("fixture-bindings"); err != nil {
			return 1
		}
		ev.FixtureInstances = map[string]string{}
		if err := probeBackendOwners(ctx, factoryDir, temp, project, compose, input, ev.Images, fixtures, ev.FixtureInstances, executor); err != nil {
			writeFailure(stderr, "backend fixture bindings unavailable or mismatched\n")
			return 1
		}
		ev.FixtureOwners = fixtures.Owners
	}
	endpoint, err := backendEndpoint(ctx, factoryDir, project, compose, "gateway", input.Services["gateway"].PublicPort, executor)
	if err != nil || endpoint != origin {
		writeFailure(stderr, "backend Gateway endpoint differs from captured Origin\n")
		return 1
	}
	sinkURLs := map[string]string{}
	for _, name := range []string{"smtp", "notification"} {
		endpoint, err := backendEndpoint(ctx, factoryDir, project, compose, name, input.Sinks[name].ControlPort, executor)
		if err != nil {
			return 1
		}
		sinkURLs[name] = endpoint
	}
	runFile := filepath.Join(temp, "backend-run.json")
	resultFile := filepath.Join(temp, "backend-result.json")
	descriptor := backendRun{Version: 1, ContractIdentity: input.ContractIdentity, Project: project, ComposeFile: compose, GatewayURL: origin, Origin: origin, SinkURLs: sinkURLs, Secrets: secrets, Requirements: input.Requirements, EvidenceDirectory: evidenceDir, Sources: ev.Sources, Images: ev.Images}
	if input.Version == 2 {
		descriptor.Version, descriptor.Secrets, descriptor.CredentialFiles, descriptor.Fixtures = 2, nil, credentialFiles, fixtures
		if err := stage("private-probe-discovery"); err != nil {
			return 1
		}
		descriptor.PrivateServiceURLs, err = discoverBackendPrivateServiceURLs(ctx, factoryDir, descriptor, input, ev.Containers, executor)
		if err != nil {
			writeFailure(stderr, "backend private probe mapping unavailable or mismatched\n")
			return 1
		}
		ev.PrivateServiceURLs = descriptor.PrivateServiceURLs
	}
	if err := writeBackendJSON(runFile, descriptor); err != nil {
		return 1
	}
	if err := stage("harness"); err != nil {
		return 1
	}
	if _, _, err := executor.Run(ctx, factoryDir, commandEnv(), harness, "--backend-run", runFile, "--backend-result", resultFile); err != nil {
		return processCode(err)
	}
	if err := stage("result"); err != nil {
		return 1
	}
	resultData, err := os.ReadFile(resultFile)
	if err != nil {
		return 1
	}
	var result backendResult
	if err := decodeBackendJSON(resultData, &result); err != nil || result.Status != "passed" || result.ContractIdentity != input.ContractIdentity {
		return 1
	}
	required := slices.Clone(input.Requirements)
	passed := slices.Clone(result.Passed)
	slices.Sort(required)
	slices.Sort(passed)
	if !slices.Equal(required, passed) {
		writeFailure(stderr, "backend harness omitted or duplicated required assertions\n")
		return 1
	}
	ev.Passed = result.Passed
	if err := stage("passed"); err != nil {
		return 1
	}
	if err := writeMessage(stdout, "Backend evidence: %s\n", evidencePath); err != nil {
		return 1
	}
	return 0
}

type backendPrivateExecutor interface {
	RunPrivate(context.Context, string, []string, []byte, string, ...string) ([]byte, error)
}
type backendBoundedOutput struct {
	data  bytes.Buffer
	limit int
}

func (b *backendBoundedOutput) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.data.Len() {
		return 0, errors.New("private command output exceeds bound")
	}
	return b.data.Write(data)
}
func (*osExecutor) RunPrivate(ctx context.Context, dir string, env []string, stdin []byte, name string, args ...string) ([]byte, error) {
	if len(stdin) > 16384 {
		return nil, errors.New("private command input exceeds bound")
	}
	command := exec.CommandContext(ctx, name, args...)
	command.Dir, command.Env, command.Stdin = dir, env, bytes.NewReader(stdin)
	limit := 262144
	if len(args) > 0 && args[0] == "cp" && (strings.HasSuffix(args[1], ":/fixture-server") || strings.HasSuffix(args[1], ":/fixture-control")) {
		limit = 64 << 20
	}
	output := backendBoundedOutput{limit: limit}
	command.Stdout, command.Stderr = &output, io.Discard
	if err := command.Run(); err != nil {
		return nil, errors.New("private command failed")
	}
	return output.data.Bytes(), nil
}
func backendRequestID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	data[6] = (data[6] & 15) | 64
	data[8] = (data[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", data[:4], data[4:6], data[6:8], data[8:10], data[10:]), nil
}

type backendReady struct {
	Version   int    `json:"version"`
	Project   string `json:"project"`
	RequestID string `json:"requestID"`
	Operation string `json:"operation"`
	Data      struct {
		Target       string   `json:"target"`
		InstanceID   string   `json:"instanceID"`
		Protocol     string   `json:"protocol"`
		BinarySHA256 string   `json:"binarySHA256"`
		ConfigSHA256 string   `json:"configSHA256"`
		Ready        bool     `json:"ready"`
		Capabilities []string `json:"capabilities"`
	} `json:"data"`
}

func probeBackendOwners(ctx context.Context, dir, temp, project, compose string, input backendInputs, images map[string]imageEvidence, fixtures *backendRunFixtures, instances map[string]string, executor commandExecutor) error {
	if input.Fixtures == nil || input.Fixtures.Protocol != backendFixtureProtocol || input.Fixtures.BindingsSHA256 != backendBindingsSHA256 || fixtures == nil || fixtures.Protocol != backendFixtureProtocol || fixtures.BindingsSHA256 != backendBindingsSHA256 {
		return errors.New("fixture readiness protocol or reviewed binding differs")
	}
	private, ok := executor.(backendPrivateExecutor)
	if !ok {
		return errors.New("private IPC executor unavailable")
	}
	owned := func() ([]backendContainer, error) {
		return backendContainers(ctx, dir, project, compose, input, images, executor, true)
	}
	copyPrivate := func(container, path string) ([]byte, error) {
		if _, err := owned(); err != nil {
			return nil, err
		}
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return private.RunPrivate(deadline, dir, commandEnv(), nil, "docker", "cp", container+":"+path, "-")
	}
	for _, name := range backendServices {
		containers, err := owned()
		if err != nil {
			return err
		}
		var container string
		for _, c := range containers {
			if c.Service == name {
				container = c.ID
			}
		}
		owner := fixtures.Owners[name]
		for _, binary := range []string{"fixture-server", "fixture-control"} {
			data, err := copyPrivate(container, "/"+binary)
			if err != nil {
				return err
			}
			files, err := readBackendPrivateArchive(data, 64<<20, false, binary)
			if err != nil || len(files) != 1 {
				return errors.New("fixture binary archive unavailable")
			}
			content, ok := files[binary]
			if !ok || len(content) == 0 {
				return errors.New("fixture binary unavailable")
			}
			if binary == "fixture-server" {
				owner.ServerBinarySHA256 = backendDigest(content)
			} else {
				owner.ControlBinarySHA256 = backendDigest(content)
			}
		}
		for _, kind := range []string{"secrets", "config"} {
			data, err := copyPrivate(container, "/run/umir-fixtures/"+kind)
			if err != nil {
				return err
			}
			files, err := readBackendPrivateArchive(data, 262144, true, kind)
			if err != nil {
				return err
			}
			host := filepath.Join(temp, "fixtures", name, kind)
			entries, err := os.ReadDir(host)
			if err != nil || len(entries) != len(files) {
				return errors.New("fixture mount inventory differs")
			}
			for _, entry := range entries {
				expected, err := os.ReadFile(filepath.Join(host, entry.Name()))
				if err != nil || !bytes.Equal(expected, files[entry.Name()]) {
					return errors.New("fixture mount content differs")
				}
			}
		}
		requestID, err := backendRequestID()
		if err != nil {
			return err
		}
		request, err := json.Marshal(map[string]any{"version": 1, "project": project, "requestID": requestID, "operation": "fixture.ready", "data": map[string]any{}})
		if err != nil {
			return err
		}
		if _, err := owned(); err != nil {
			return err
		}
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		out, err := private.RunPrivate(deadline, dir, commandEnv(), request, "docker", append(backendComposeArgs(project, compose), "exec", "-T", name, "/fixture-control", "request")...)
		cancel()
		if err != nil || len(out) > 262144 {
			return errors.New("fixture readiness IPC unavailable")
		}
		var ready backendReady
		if err := decodeBackendJSON(out, &ready); err != nil {
			return err
		}
		if ready.Version != 1 || ready.Project != project || ready.RequestID != requestID || ready.Operation != "fixture.ready" || ready.Data.Target != name || !backendUUID(ready.Data.InstanceID) || ready.Data.Protocol != backendFixtureProtocol || !ready.Data.Ready || ready.Data.BinarySHA256 != owner.ServerBinarySHA256 || ready.Data.ConfigSHA256 != owner.ConfigSHA256 {
			return errors.New("fixture readiness identity differs")
		}
		caps := ready.Data.Capabilities
		seen := map[string]bool{}
		for _, capability := range caps {
			if capability == "" || len(capability) > 128 || seen[capability] || !backendOwnerCapability(name, capability) {
				return errors.New("invalid fixture capability set")
			}
			seen[capability] = true
		}
		if len(caps) == 0 || len(caps) > 32 {
			return errors.New("invalid fixture capability bound")
		}
		for _, required := range []string{"fixture.ready", "clock.read", "clock.set", "clock.checkpoint"} {
			if !seen[required] {
				return errors.New("required fixture capability absent")
			}
		}
		if name == "auth" && !seen["owner.jobs.discover"] {
			return errors.New("auth discovery capability absent")
		}
		for _, instance := range instances {
			if instance == ready.Data.InstanceID {
				return errors.New("fixture process instance reused across owners")
			}
		}
		instances[name] = ready.Data.InstanceID
		owner.Capabilities = slices.Clone(caps)
		fixtures.Owners[name] = owner
	}
	return nil
}
func backendOwnerCapability(owner, capability string) bool {
	switch capability {
	case "fixture.ready", "clock.read", "clock.set", "clock.checkpoint":
		return slices.Contains(backendServices, owner)
	case "bootstrap.read", "owner.jobs", "owner.jobs.discover":
		return owner == "auth"
	case "feedback.observe":
		return owner == "feedback"
	case "scheduler.tick":
		return owner == "gateway"
	case "owner.fault.arm", "owner.fault.read", "owner.fault.clear":
		return owner == "auth" || owner == "feedback" || owner == "warehouse"
	default:
		return false
	}
}
func readBackendPrivateArchive(data []byte, limit int, private bool, root string) (map[string][]byte, error) {
	if len(data) > limit {
		return nil, errors.New("private archive exceeds bound")
	}
	files := map[string][]byte{}
	rootSeen := false
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("invalid private archive")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !safeBackendFixturePath(name) {
			return nil, errors.New("unsafe private archive path")
		}
		if header.Typeflag == tar.TypeDir {
			if !private || name != root || rootSeen || header.Uid != 65532 || header.Gid != 65532 || header.Mode != 0o700 {
				return nil, errors.New("private directory path or ownership differs")
			}
			rootSeen = true
			continue
		}
		if private {
			name = strings.TrimPrefix(name, root+"/")
		}
		if strings.ContainsRune(name, '/') || !private && name != root {
			return nil, errors.New("private archive path differs from requested root")
		}
		if header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > int64(limit) || files[name] != nil || private && (header.Uid != 65532 || header.Gid != 65532 || header.Mode != 0o600 || header.Size > 16384) {
			return nil, errors.New("private file bounds or ownership differ")
		}
		content, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
		if err != nil || int64(len(content)) != header.Size {
			return nil, errors.New("private file content unavailable")
		}
		files[name] = content
		if len(files) > 32 {
			return nil, errors.New("private file inventory exceeds bound")
		}
	}
	return files, nil
}
