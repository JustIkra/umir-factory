package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sync"

	"strings"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

const backendContract = "sha256:733e102ff0b4f52bb49d435716f9f1720642c8fad3a8a5d0685847071950e9e6"

var backendOwners = []string{"auth", "feedback", "warehouse", "serving", "gateway"}
var backendTargets = []string{"auth", "feedback", "warehouse", "serving", "gateway", "smtp", "notification"}

type fixtureClock struct {
	Owner             string   `json:"owner"`
	InstanceID        string   `json:"instanceID"`
	Source            string   `json:"source"`
	FixtureID         string   `json:"fixtureID"`
	Generation        string   `json:"generation"`
	Instant           string   `json:"instant"`
	InjectedConsumers []string `json:"injectedConsumers"`
	Healthy           bool     `json:"healthy"`
}
type fixtureRequest struct {
	Version   int            `json:"version"`
	Project   string         `json:"project"`
	RequestID string         `json:"requestID"`
	Operation string         `json:"operation"`
	Data      map[string]any `json:"data"`
}
type fixtureOwner struct {
	Profile               string                    `json:"profile"`
	ConfigSHA256          string                    `json:"configSHA256"`
	ServerBinarySHA256    string                    `json:"serverBinarySHA256"`
	ControlBinarySHA256   string                    `json:"controlBinarySHA256"`
	ConfigPath            string                    `json:"configPath"`
	ControlSocket         string                    `json:"controlSocket"`
	ControlCredentialFile faultproxy.FixtureFileRef `json:"controlCredentialFile"`
	Capabilities          []string                  `json:"capabilities"`
}
type fixtureDescriptor struct {
	Protocol               string                  `json:"protocol"`
	BindingsSHA256         string                  `json:"bindingsSHA256"`
	FixtureID              string                  `json:"fixtureID"`
	InitialInstant         string                  `json:"initialInstant"`
	InitialGeneration      string                  `json:"initialGeneration"`
	HarnessPrivateRoot     string                  `json:"harnessPrivateRoot"`
	Owners                 map[string]fixtureOwner `json:"owners"`
	BootstrapBindingSHA256 string                  `json:"bootstrapBindingSHA256"`
}
type backendImage struct {
	Ref         string   `json:"ref"`
	ID          string   `json:"id"`
	Digests     []string `json:"digests,omitempty"`
	Source      string   `json:"source,omitempty"`
	Revision    string   `json:"revision,omitempty"`
	Healthcheck []string `json:"healthcheck,omitempty"`
}
type backendRunDescriptor struct {
	Version            int                                  `json:"version"`
	ContractIdentity   string                               `json:"contractIdentity"`
	Project            string                               `json:"project"`
	ComposeFile        string                               `json:"composeFile"`
	PrivateServiceURLs map[string]string                    `json:"privateServiceURLs"`
	GatewayURL         string                               `json:"gatewayURL"`
	Origin             string                               `json:"origin"`
	SinkURLs           map[string]string                    `json:"sinkURLs"`
	CredentialFiles    map[string]faultproxy.FixtureFileRef `json:"credentialFiles"`
	Fixtures           fixtureDescriptor                    `json:"fixtures"`
	Requirements       []string                             `json:"requirements"`
	EvidenceDirectory  string                               `json:"evidenceDirectory"`
	Sources            map[string]json.RawMessage           `json:"sources"`
	Images             map[string]backendImage              `json:"images"`
}
type backendClient struct {
	privateOnce         sync.Once
	privateGate         chan struct{}
	publicMu            sync.Mutex
	publicActive        int
	publicIdle          chan struct{}
	containers          map[string]string
	requalifying        map[string]bool
	unqualified         map[string]bool
	privateObservations []backendPrivateObservation
	run                 backendRunDescriptor
	execute             func(context.Context, []byte, ...string) ([]byte, error)
	instances           map[string]string
	capabilities        map[string][]string
	observations        []backendControlObservation
	restarts            []backendRestartObservation
	feedbackEvidence    []backendFeedbackEvidence
}

func newBackendClient(run backendRunDescriptor) *backendClient {
	return &backendClient{run: run, execute: backendExecute, instances: map[string]string{}, capabilities: map[string][]string{}}
}
func readBackendRun(path string) (backendRunDescriptor, error) {
	raw, e := faultproxy.ReadPrivateFile(path, 16<<20)
	if e != nil {
		return backendRunDescriptor{}, faultproxy.ErrProtocol
	}
	if _, e := faultproxy.StrictJSON(raw, 32); e != nil {
		return backendRunDescriptor{}, e
	}
	schema, schemaErr := backendPrivateWire.ReadFile("backend-run.schema.json")
	if schemaErr != nil || faultproxy.ValidateSchema(schema, "#", raw, 32) != nil {
		return backendRunDescriptor{}, faultproxy.ErrProtocol
	}
	var d backendRunDescriptor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil {
		return d, faultproxy.ErrProtocol
	}
	if d.Version != 2 || d.ContractIdentity != backendContract || !regexp.MustCompile(`^umir-backend-[0-9a-f]{24}$`).MatchString(d.Project) || d.Fixtures.Protocol != faultproxy.FixtureProtocol || d.Fixtures.BindingsSHA256 != faultproxy.FixtureBindings || !faultproxy.UUID(d.Fixtures.FixtureID) {
		return d, faultproxy.ErrProtocol
	}
	if !filepath.IsAbs(d.ComposeFile) || !filepath.IsAbs(d.EvidenceDirectory) || !filepath.IsAbs(d.Fixtures.HarnessPrivateRoot) || filepath.Dir(d.ComposeFile) != filepath.Dir(filepath.Dir(d.Fixtures.HarnessPrivateRoot)) {
		return d, faultproxy.ErrProtocol
	}
	if validateURL(d.GatewayURL, true) != nil || d.GatewayURL != d.Origin || !strings.HasPrefix(d.GatewayURL, "http://127.0.0.1:") {
		return d, faultproxy.ErrProtocol
	}
	if _, e := faultproxy.ReadPrivateFile(d.ComposeFile, 1<<20); e != nil {
		return d, e
	}
	if len(d.Fixtures.Owners) != 5 || len(d.Images) != 7 || len(d.Sources) != 5 || len(d.SinkURLs) != 2 || len(d.Requirements) == 0 {
		return d, faultproxy.ErrProtocol
	}
	for _, target := range backendTargets {
		image, ok := d.Images[target]
		if !ok || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image.ID) {
			return d, faultproxy.ErrProtocol
		}
		if slices.Contains(backendOwners, target) {
			o, ok := d.Fixtures.Owners[target]
			if !ok || o.Profile != target+"-real-v2" || o.ControlSocket != "/run/umir-fixtures/control.sock" || o.ConfigPath != "/run/umir-fixtures/config/config.json" || !isDigest(o.ServerBinarySHA256) || !isDigest(o.ControlBinarySHA256) || !isDigest(o.ConfigSHA256) {
				return d, faultproxy.ErrProtocol
			}
		} else if validateURL(d.SinkURLs[target], true) != nil {
			return d, faultproxy.ErrProtocol
		}
	}
	if validatePrivateURLs(d) != nil {
		return d, faultproxy.ErrProtocol
	}
	seen := map[string]bool{}
	for _, id := range d.Requirements {
		if !regexp.MustCompile(`^BE-E[1-5]-[0-9]{2}$`).MatchString(id) || seen[id] {
			return d, faultproxy.ErrProtocol
		}
		seen[id] = true
	}
	for name, ref := range d.CredentialFiles {
		if ref.Target != "harness" || ref.Path != "/run/umir-fixtures/secrets/"+name || !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(name) {
			return d, faultproxy.ErrProtocol
		}
	}
	return d, nil
}
func isDigest(s string) bool { return regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s) }

type boundedBackendOutput struct {
	bytes.Buffer
	limit int
}

type backendControlTransportError struct {
	cause error
}

func (e *backendControlTransportError) Error() string { return "owner control transport unavailable" }
func (e *backendControlTransportError) Unwrap() error { return e.cause }

func (b *boundedBackendOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > b.limit {
		return 0, faultproxy.ErrProtocol
	}
	return b.Buffer.Write(data)
}
func backendExecute(parent context.Context, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	limit := 262144
	if len(args) > 0 && args[0] == "cp" {
		limit = 8 << 20
	}
	out := &boundedBackendOutput{limit: limit}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return nil, errors.New("owned Docker operation failed")
	}
	return out.Bytes(), nil
}
func (c *backendClient) compose(args ...string) []string {
	return append([]string{"compose", "--project-name", c.run.Project, "--file", c.run.ComposeFile}, args...)
}
func (c *backendClient) ownership(ctx context.Context, target string) (string, error) {
	return c.ownershipState(ctx, target, true)
}
func (c *backendClient) ownershipState(ctx context.Context, target string, running bool) (string, error) {
	if !slices.Contains(backendTargets, target) {
		return "", faultproxy.ErrProtocol
	}
	out, e := c.readOwned(ctx, c.compose("ps", "--all", "--quiet", target)...)
	id := strings.TrimSpace(string(out))
	if e != nil || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(id) {
		return "", errors.New("OwnershipMismatch")
	}
	out, e = c.readOwned(ctx, "container", "inspect", "--format", `{"id":{{json .Id}},"image":{{json .Image}},"running":{{json .State.Running}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"service":{{json (index .Config.Labels "com.docker.compose.service")}},"run":{{json (index .Config.Labels "io.umir.backend.run")}}}`, id)
	if e != nil {
		return "", errors.New("OwnershipMismatch")
	}
	var info struct {
		ID, Image, Project, Service, Run string
		Running                          bool
	}
	if json.Unmarshal(out, &info) != nil || info.ID != id || info.Running != running || info.Image != c.run.Images[target].ID || info.Project != c.run.Project || info.Service != target || info.Run != c.run.Project {
		return "", errors.New("OwnershipMismatch")
	}
	if c.containers != nil && !c.requalifying[target] && c.containers[target] != id {
		return "", errors.New("OwnershipMismatch")
	}
	return id, nil
}
func (c *backendClient) control(ctx context.Context, target, op string, data map[string]any) (map[string]any, error) {
	id, err := faultproxy.NewUUID()
	if err != nil {
		return nil, faultproxy.ErrProtocol
	}
	request := fixtureRequest{1, c.run.Project, id, op, data}
	result, _, err := c.controlRequest(ctx, target, request)
	return result, err
}
func (c *backendClient) controlRequest(ctx context.Context, target string, request fixtureRequest) (result map[string]any, response []byte, callErr error) {
	op := request.Operation
	replyValidated := false
	safeTarget := target
	if !slices.Contains(backendTargets, target) {
		safeTarget = "invalid"
	}
	safeOperation := op
	if !faultproxy.OperationalOperationKnown(op) {
		safeOperation = "invalid"
	}
	observation := backendControlObservation{Target: safeTarget, Operation: safeOperation}
	defer func() {
		var f *faultproxy.FixtureError
		if errors.As(callErr, &f) {
			observation.ErrorCode = f.Code
			observation.Effect = f.Effect
			observation.ResponseValid = replyValidated
		} else if callErr == nil {
			observation.ResponseValid = true
		} else {
			observation.ErrorCode = "Unavailable"
		}
		c.observations = append(c.observations, observation)
	}()
	if request.Project != c.run.Project || request.Version != 1 {
		return nil, nil, faultproxy.ErrProtocol
	}
	rawCheck, err := json.Marshal(request)
	if err != nil || len(rawCheck) > 16384 || faultproxy.ValidateOperational("#/$defs/"+strings.ReplaceAll(op, ".", "_")+"_request", rawCheck) != nil {
		return nil, nil, faultproxy.ErrProtocol
	}
	if op != "fixture.ready" && !slices.Contains(c.capabilities[target], op) {
		return nil, nil, &faultproxy.FixtureError{Code: "UnsupportedOperation", Effect: "none", Retryable: false}
	}
	id, e := c.ownership(ctx, target)
	if e != nil {
		return nil, nil, e
	}
	observation.RequestID = request.RequestID
	raw, e := json.Marshal(request)
	if e != nil || len(raw) > 16384 || faultproxy.ValidateOperational("#/$defs/"+strings.ReplaceAll(op, ".", "_")+"_request", raw) != nil {
		return nil, nil, faultproxy.ErrProtocol
	}
	if slices.Contains(backendOwners, target) {
		archive, e := c.execute(ctx, nil, "cp", id+":/fixture-control", "-")
		if e != nil {
			return nil, nil, e
		}
		binary, e := readBackendTar(archive, "fixture-control", 8<<20, false)
		if e != nil || faultproxy.SHA256(binary) != c.run.Fixtures.Owners[target].ControlBinarySHA256 {
			return nil, nil, errors.New("OwnershipMismatch")
		}
		response, e = c.execute(ctx, raw, c.compose("exec", "-T", target, "/fixture-control", "request")...)
		if e != nil {
			return nil, nil, &backendControlTransportError{cause: e}
		}
	} else {
		if e := c.qualifyProbe(ctx, target); e != nil {
			return nil, nil, e
		}
		secret, e := c.credential("fixture-" + target)
		if e != nil {
			return nil, nil, e
		}
		reply, e := backendPinnedHTTP(ctx, c.run.SinkURLs[target], http.MethodPost, "/fixture/v1/control", raw, http.Header{"Authorization": []string{"Bearer " + string(secret)}})
		clear(secret)
		if e != nil || reply.Status != 200 {
			return nil, nil, faultproxy.ErrProtocol
		}
		response = reply.Body
	}
	result, e = validateControlResponse(request, response)
	var typedReplyError *faultproxy.FixtureError
	replyValidated = e == nil || errors.As(e, &typedReplyError)
	if e != nil {
		return nil, nil, e
	}
	if op == "fixture.ready" {
		if result["target"] != target || result["ready"] != true || result["protocol"] != faultproxy.FixtureProtocol {
			return nil, nil, errors.New("NotReady")
		}
		instance, ok := result["instanceID"].(string)
		if !ok || !faultproxy.UUID(instance) {
			return nil, nil, faultproxy.ErrProtocol
		}
		if slices.Contains(backendOwners, target) {
			o := c.run.Fixtures.Owners[target]
			if result["binarySHA256"] != o.ServerBinarySHA256 || result["configSHA256"] != o.ConfigSHA256 {
				return nil, nil, errors.New("OwnershipMismatch")
			}
		}
		values, ok := result["capabilities"].([]any)
		if !ok {
			return nil, nil, faultproxy.ErrProtocol
		}
		caps := []string{}
		for _, value := range values {
			cap, ok := value.(string)
			if !ok || !faultproxy.OperationalOperationKnown(cap) || slices.Contains(caps, cap) {
				return nil, nil, faultproxy.ErrProtocol
			}
			caps = append(caps, cap)
		}
		for _, cap := range c.run.Fixtures.Owners[target].Capabilities {
			if !slices.Contains(caps, cap) {
				return nil, nil, errors.New("NotReady")
			}
		}
		if c.capabilities == nil {
			c.capabilities = map[string][]string{}
		}
		c.capabilities[target] = caps
		c.instances[target] = instance
	}
	return result, slices.Clone(response), nil
}
func (c *backendClient) credential(name string) ([]byte, error) {
	ref, ok := c.run.CredentialFiles[name]
	if !ok || ref.Target != "harness" || ref.Path != "/run/umir-fixtures/secrets/"+name {
		return nil, faultproxy.ErrProtocol
	}
	path := filepath.Join(c.run.Fixtures.HarnessPrivateRoot, "secrets", name)
	return faultproxy.ReadPrivateFile(path, 16384)
}
func validateControlResponse(request fixtureRequest, raw []byte) (map[string]any, error) {
	if len(raw) > 262144 {
		return nil, faultproxy.ErrProtocol
	}
	value, e := faultproxy.StrictJSON(raw, 16)
	response, ok := value.(map[string]any)
	if e != nil || !ok || response["project"] != request.Project || response["requestID"] != request.RequestID || response["operation"] != request.Operation {
		return nil, faultproxy.ErrProtocol
	}
	if errorValue, ok := response["error"]; ok {
		if faultproxy.ValidateOperational("#/$defs/Error", raw) != nil {
			return nil, faultproxy.ErrProtocol
		}
		f, ok := errorValue.(map[string]any)
		if !ok {
			return nil, faultproxy.ErrProtocol
		}
		code, ok := f["code"].(string)
		if !ok {
			return nil, faultproxy.ErrProtocol
		}
		effect, ok := f["effect"].(string)
		if !ok {
			return nil, faultproxy.ErrProtocol
		}
		retryable, ok := f["retryable"].(bool)
		if !ok {
			return nil, faultproxy.ErrProtocol
		}
		readOnly := slices.Contains([]string{"fixture.ready", "clock.read", "clock.checkpoint", "bootstrap.read", "owner.jobs", "owner.jobs.discover", "feedback.observe", "sink.capture", "owner.fault.read", "sink.fault.read"}, request.Operation)
		if retryable && (!readOnly || effect != "none" || !slices.Contains([]string{"NotReady", "WaitExpired"}, code)) {
			return nil, faultproxy.ErrProtocol
		}
		return nil, &faultproxy.FixtureError{Code: code, Effect: effect, Retryable: retryable}
	}
	if faultproxy.ValidateOperational("#/$defs/"+strings.ReplaceAll(request.Operation, ".", "_")+"_response", raw) != nil {
		return nil, faultproxy.ErrProtocol
	}
	data, ok := response["data"].(map[string]any)
	if !ok {
		return nil, faultproxy.ErrProtocol
	}
	return data, nil
}
func readBackendTar(archive []byte, name string, limit int, private bool) ([]byte, error) {
	r := tar.NewReader(bytes.NewReader(archive))
	var result []byte
	found := false
	for {
		h, e := r.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil || h.Name != name || h.Typeflag != tar.TypeReg || h.Size < 1 || h.Size > int64(limit) || found {
			return nil, faultproxy.ErrProtocol
		}
		if private && (h.Mode != 0o600 || h.Uid != 65532 || h.Gid != 65532) {
			return nil, faultproxy.ErrProtocol
		}
		result, e = io.ReadAll(io.LimitReader(r, int64(limit+1)))
		if e != nil || len(result) > limit {
			return nil, faultproxy.ErrProtocol
		}
		found = true
	}
	if !found {
		return nil, faultproxy.ErrProtocol
	}
	return result, nil
}
func (c *backendClient) clock(ctx context.Context, target string) (fixtureClock, error) {
	data, e := c.control(ctx, target, "clock.read", map[string]any{})
	if e != nil {
		return fixtureClock{}, e
	}
	raw, e := json.Marshal(data)
	if e != nil {
		return fixtureClock{}, faultproxy.ErrProtocol
	}
	var clock fixtureClock
	if json.Unmarshal(raw, &clock) != nil || clock.Owner != target || clock.InstanceID != c.instances[target] || clock.FixtureID != c.run.Fixtures.FixtureID || clock.Source != "owner-injected" || !clock.Healthy || len(clock.InjectedConsumers) == 0 {
		return clock, errors.New("ClockUnavailable")
	}
	return clock, nil
}
func (c *backendClient) advance(ctx context.Context, instant string) error {
	before := map[string]fixtureClock{}
	for _, owner := range backendOwners {
		clock, e := c.clock(ctx, owner)
		if e != nil {
			return e
		}
		before[owner] = clock
	}
	for _, owner := range backendOwners {
		clock := before[owner]
		if _, e := c.control(ctx, owner, "clock.set", map[string]any{"expectedInstanceID": clock.InstanceID, "expectedGeneration": clock.Generation, "instant": instant}); e != nil {
			return errors.New("partial authoritative clock advance")
		}
	}
	for _, owner := range backendOwners {
		clock, e := c.clock(ctx, owner)
		if e != nil {
			return e
		}
		requested, e := time.Parse(time.RFC3339Nano, instant)
		actual, parseErr := time.Parse(time.RFC3339Nano, clock.Instant)
		if e != nil || parseErr != nil || !actual.Equal(requested) || clock.InstanceID != before[owner].InstanceID {
			return errors.New("clock readback mismatch")
		}
		old, e := faultproxy.Decimal(before[owner].Generation)
		newGen, genErr := faultproxy.Decimal(clock.Generation)
		if e != nil || genErr != nil || newGen != old+1 {
			return errors.New("clock generation mismatch")
		}
	}
	return nil
}
func validateDiscoveredJobs(data, selector map[string]any, clock fixtureClock, expiry string) (map[string]any, error) {
	req := fixtureRequest{1, "umir-backend-0123456789abcdef01234567", "11111111-1111-4111-8111-111111111111", "owner.jobs.discover", data}
	raw, e := json.Marshal(req)
	if e != nil || faultproxy.ValidateOperational("#/$defs/owner_jobs_discover_response", raw) != nil {
		return nil, faultproxy.ErrProtocol
	}
	if data["instanceID"] != clock.InstanceID || data["clockGeneration"] != clock.Generation || data["complete"] != true || !reflect.DeepEqual(data["selector"], selector) {
		return nil, errors.New("CorrelationConflict")
	}
	aggregate, ok := data["aggregateRevision"].(string)
	if !ok {
		return nil, faultproxy.ErrProtocol
	}
	revision, e := faultproxy.Decimal(aggregate)
	if e != nil {
		return nil, e
	}
	command, ok := data["commandAggregateRevision"].(string)
	if selector["kind"] == "command" {
		if !ok {
			return nil, faultproxy.ErrProtocol
		}
		cr, e := faultproxy.Decimal(command)
		if e != nil || cr > revision {
			return nil, errors.New("CorrelationConflict")
		}
	} else if data["commandAggregateRevision"] != nil {
		return nil, errors.New("CorrelationConflict")
	}
	jobs, ok := data["jobs"].([]any)
	if !ok || len(jobs) == 0 || len(jobs) > 64 || selector["kind"] == "command" && len(jobs) != 1 {
		return nil, errors.New("CorrelationConflict")
	}
	last := ""
	for _, v := range jobs {
		job, ok := v.(map[string]any)
		if !ok {
			return nil, faultproxy.ErrProtocol
		}
		id, ok := job["jobID"].(string)
		if !ok || id <= last || job["challengeID"] != selector["challengeID"] || job["channel"] != "smtp" || job["expiresAt"] != expiry {
			return nil, errors.New("CorrelationConflict")
		}
		last = id
	}
	return data, nil
}
func (c *backendClient) discoverJob(ctx context.Context, selector map[string]any, expiry string) (map[string]any, error) {
	clock, e := c.clock(ctx, "auth")
	if e != nil {
		return nil, e
	}
	data, e := c.control(ctx, "auth", "owner.jobs.discover", map[string]any{"expectedInstanceID": clock.InstanceID, "expectedClockGeneration": clock.Generation, "selector": selector, "waitMs": 5000})
	if e != nil {
		return nil, e
	}
	return validateDiscoveredJobs(data, selector, clock, expiry)
}
func (c *backendClient) otp(ctx context.Context, job map[string]any, clock fixtureClock) (string, error) {
	correlation := map[string]any{"kind": "auth-otp", "jobID": job["jobID"], "challengeID": job["challengeID"]}
	if _, e := c.control(ctx, "smtp", "sink.correlation.bind", map[string]any{"correlation": correlation, "ownerInstanceID": clock.InstanceID, "ownerClockGeneration": clock.Generation, "authoritativeExpiresAt": job["expiresAt"]}); e != nil {
		return "", e
	}
	data, e := c.control(ctx, "smtp", "sink.capture", map[string]any{"selector": correlation, "afterSequence": "1", "limit": 64, "waitMs": 5000})
	if e != nil || data["dropped"] != json.Number("0") || data["complete"] != true {
		return "", errors.New("capture unavailable")
	}
	caps, ok := data["captures"].([]any)
	if !ok || len(caps) == 0 {
		return "", errors.New("capture unavailable")
	}
	var cap map[string]any
	for _, v := range caps {
		m, ok := v.(map[string]any)
		if ok && m["accepted"] == true && reflect.DeepEqual(m["correlation"], correlation) {
			cap = m
			break
		}
	}
	if cap == nil {
		return "", errors.New("capture unavailable")
	}
	id, e := faultproxy.NewUUID()
	if e != nil {
		return "", e
	}
	name := "otp-" + id
	logical := "/run/umir-fixtures/private/" + name
	result, e := c.control(ctx, "smtp", "sink.otp.write", map[string]any{"captureID": cap["captureID"], "correlation": correlation, "output": map[string]any{"target": "smtp", "path": logical}})
	if e != nil || result["expiresAt"] != job["expiresAt"] || result["captureID"] != cap["captureID"] || !reflect.DeepEqual(result["correlation"], correlation) {
		return "", errors.New("OTP unavailable")
	}
	ref, ok := result["secretFile"].(map[string]any)
	if !ok || ref["target"] != "smtp" || ref["path"] != logical {
		return "", faultproxy.ErrProtocol
	}
	container, e := c.ownership(ctx, "smtp")
	if e != nil {
		return "", e
	}
	archive, e := c.execute(ctx, nil, "cp", container+":"+logical, "-")
	if e != nil {
		return "", e
	}
	code, e := readBackendTar(archive, name, 6, true)
	if e != nil || !regexp.MustCompile(`^[0-9]{6}$`).Match(code) {
		return "", errors.New("OTP unavailable")
	}
	if _, e := c.ownership(ctx, "smtp"); e != nil {
		return "", e
	}
	if _, e := c.execute(ctx, nil, c.compose("exec", "-T", "smtp", "rm", "--", logical)...); e != nil {
		return "", e
	}
	file := filepath.Join(c.run.Fixtures.HarnessPrivateRoot, "private", name)
	if e := os.MkdirAll(filepath.Dir(file), 0o700); e != nil {
		return "", faultproxy.ErrProtocol
	}
	f, e := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if e != nil {
		return "", faultproxy.ErrProtocol
	}
	_, e = f.Write(code)
	e = errors.Join(e, f.Close())
	if e != nil {
		removeErr := os.Remove(file)
		return "", errors.Join(faultproxy.ErrProtocol, removeErr)
	}
	read, e := faultproxy.ReadPrivateFile(file, 6)
	removeErr := os.Remove(file)
	if e != nil || removeErr != nil {
		return "", faultproxy.ErrProtocol
	}
	return string(read), nil
}
func validateCheckpoint(data map[string]any, owner, instance, fixture string) (fixtureClock, error) {
	clock, ok := data["clock"].(map[string]any)
	if !ok {
		return fixtureClock{}, faultproxy.ErrProtocol
	}
	raw, e := json.Marshal(clock)
	if e != nil || faultproxy.ValidateOperational("#/$defs/Clock", raw) != nil || data["checkpointSHA256"] != faultproxy.SHA256(raw) {
		return fixtureClock{}, faultproxy.ErrProtocol
	}
	var result fixtureClock
	if json.Unmarshal(raw, &result) != nil || result.Owner != owner || result.InstanceID != instance || result.FixtureID != fixture || result.Source != "owner-injected" || !result.Healthy {
		return fixtureClock{}, faultproxy.ErrProtocol
	}
	return result, nil
}

type backendControlObservation struct {
	Target        string `json:"target"`
	Operation     string `json:"operation"`
	RequestID     string `json:"requestID,omitempty"`
	ResponseValid bool   `json:"responseValid"`
	ErrorCode     string `json:"errorCode,omitempty"`
	Effect        string `json:"effect,omitempty"`
}
