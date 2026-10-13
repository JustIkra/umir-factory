package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const backendContractIdentity = "sha256:733e102ff0b4f52bb49d435716f9f1720642c8fad3a8a5d0685847071950e9e6"

const backendFixtureProtocol = "umir-operational-fixtures/1.1.0"
const backendBindingsSHA256 = "af215b378140b8e690523fc537007679b8c2599ed3039f7f2a0c651a64c59410"
const backendFixtureConfigPath = "/run/umir-fixtures/config/config.json"
const backendControlSocket = "/run/umir-fixtures/control.sock"

type backendFileRef struct {
	Target string `json:"target"`
	Path   string `json:"path"`
}
type backendBootstrapUser struct {
	UserID            string `json:"userID"`
	Login             string `json:"login"`
	DisplayName       string `json:"displayName"`
	CredentialVersion string `json:"credentialVersion"`
}
type backendBootstrap struct {
	User         backendBootstrapUser `json:"user"`
	PasswordFile backendFileRef       `json:"passwordFile"`
	StateFile    string               `json:"stateFile"`
	Mode         string               `json:"mode"`
}
type backendSinkSource struct {
	Path       string        `json:"path"`
	Ref        string        `json:"ref"`
	SourceMode string        `json:"sourceMode"`
	Files      []backendFile `json:"files"`
	Dockerfile string        `json:"dockerfile"`
}
type backendFixtureInput struct {
	Protocol       string             `json:"protocol"`
	BindingsSHA256 string             `json:"bindingsSHA256"`
	FixtureID      string             `json:"fixtureID"`
	InitialInstant string             `json:"initialInstant"`
	Bootstrap      backendBootstrap   `json:"bootstrap"`
	Owners         map[string]string  `json:"owners"`
	SinkSource     *backendSinkSource `json:"sinkSource"`
}

var backendServices = []string{"auth", "gateway", "warehouse", "serving", "feedback"}

type backendFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}
type backendSource struct {
	Path        string            `json:"path"`
	Ref         string            `json:"ref"`
	SourceMode  string            `json:"sourceMode"`
	Files       []backendFile     `json:"files"`
	Dockerfile  string            `json:"dockerfile"`
	Port        int               `json:"port"`
	PublicPort  int               `json:"publicPort,omitempty"`
	StatePath   string            `json:"statePath,omitempty"`
	Environment map[string]string `json:"environment"`
	Command     []string          `json:"command"`
	Healthcheck []string          `json:"healthcheck"`
}
type backendSink struct {
	Image       string   `json:"image,omitempty"`
	Port        int      `json:"port"`
	ControlPort int      `json:"controlPort"`
	Command     []string `json:"command,omitempty"`
}
type backendInputs struct {
	Version          int                      `json:"version"`
	ContractIdentity string                   `json:"contractIdentity"`
	Services         map[string]backendSource `json:"services"`
	Sinks            map[string]backendSink   `json:"sinks"`
	Requirements     []string                 `json:"requirements"`
	Fixtures         *backendFixtureInput     `json:"fixtures,omitempty"`
}
type backendSourceEvidence struct {
	Ref             string        `json:"ref"`
	SourceMode      string        `json:"sourceMode"`
	ContextSHA256   string        `json:"contextSHA256"`
	Files           []backendFile `json:"files"`
	ExcludedIgnored []string      `json:"excludedIgnored"`
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}
func validateBackendOptions(o cliOptions) error {
	if !o.candidate || o.selected || o.auth || o.realtime || o.frontend != "" {
		return errors.New("--backend-only requires --candidate and excludes --selected, --auth, --realtime and frontend refs")
	}
	for _, ref := range backendRefs(o) {
		if !isSHA(ref) {
			return errors.New("backend candidate requires five exact lowercase full commit SHAs")
		}
	}
	for _, path := range []string{o.backendInputs, o.backendHarness, o.backendEvidenceDir} {
		if !filepath.IsAbs(path) {
			return errors.New("backend inputs, harness and evidence directory must be explicit absolute paths")
		}
	}
	if !isSHA256(o.backendInputsSHA) || !isSHA256(o.backendHarnessSHA) {
		return errors.New("backend inputs and harness require reviewed lowercase SHA-256 identities")
	}
	return nil
}
func backendRefs(o cliOptions) map[string]string {
	return map[string]string{"auth": o.authRef, "gateway": o.gateway, "warehouse": o.warehouse, "serving": o.serving, "feedback": o.feedbackRef}
}
func backendDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func decodeBackendJSON(data []byte, target any) error {
	if len(data) > 16<<20 || !utf8.Valid(data) {
		return errors.New("backend JSON exceeds its size limit")
	}
	if err := validateBackendJSONKeys(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid backend JSON descriptor")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("backend JSON must contain exactly one object")
	}
	return nil
}

func validateBackendJSONKeys(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("backend JSON nesting exceeds its limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid backend JSON")
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return errors.New("invalid backend JSON key")
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate backend JSON field")
			}
			seen[name] = true
		}
		if err := validateBackendJSONKeys(decoder, depth+1); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return errors.New("invalid backend JSON closing delimiter")
	}
	return nil
}
func readBackendInputs(o cliOptions) (backendInputs, error) {
	var input backendInputs
	data, err := os.ReadFile(o.backendInputs)
	if err != nil {
		return input, fmt.Errorf("read backend inputs: %w", err)
	}
	if backendDigest(data) != o.backendInputsSHA {
		return input, errors.New("backend input identity differs from reviewed digest")
	}
	if err := decodeBackendJSON(data, &input); err != nil {
		return input, err
	}
	if (input.Version != 1 && input.Version != 2) || input.ContractIdentity != backendContractIdentity {
		return input, errors.New("unsupported backend descriptor or unfrozen contract")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return input, errors.New("invalid backend input")
	}
	if input.Version == 1 {
		if _, present := fields["fixtures"]; present {
			return input, errors.New("version1 excludes fixtures")
		}
	} else if err := validateBackendFixtures(fields["fixtures"], input); err != nil {
		return input, err
	}
	if input.Version == 2 && input.Fixtures.SinkSource != nil {
		var sinks map[string]map[string]json.RawMessage
		if err := json.Unmarshal(fields["sinks"], &sinks); err != nil {
			return input, errors.New("invalid sink source mode")
		}
		for _, sink := range sinks {
			if _, present := sink["image"]; present {
				return input, errors.New("local source sinks must omit image fields")
			}
		}
	}
	if len(input.Services) != 5 || len(input.Sinks) != 2 {
		return input, errors.New("backend descriptor requires exactly five services and two synthetic delivery sinks")
	}
	refs := backendRefs(o)
	dirty := false
	for _, name := range backendServices {
		s, ok := input.Services[name]
		if !ok || s.Ref != refs[name] {
			return input, fmt.Errorf("%s descriptor source differs from supplied ref", name)
		}
		if s.SourceMode != "clean" && s.SourceMode != "reviewed-dirty" {
			return input, errors.New("sourceMode must be clean or reviewed-dirty")
		}
		dirty = dirty || s.SourceMode == "reviewed-dirty"
		if !filepath.IsAbs(s.Path) || !safeBackendPath(s.Dockerfile) || len(s.Files) == 0 || input.Version == 2 && len(s.Files) > 10000 {
			return input, errors.New("explicit source directory, Dockerfile and complete file manifest required")
		}
		if s.Port < 1 || s.Port > 65535 || len(s.Healthcheck) < 2 || s.Healthcheck[0] != "CMD" {
			return input, errors.New("service requires a private port and exec-form readiness check")
		}
		if name == "gateway" {
			if s.PublicPort < 1 || s.PublicPort > 65535 {
				return input, errors.New("gateway public container port required")
			}
		} else if s.PublicPort != 0 {
			return input, errors.New("only Gateway may publish a business listener")
		}
		if name == "auth" || name == "feedback" {
			if !strings.HasPrefix(s.StatePath, "/var/lib/") || filepath.Clean(s.StatePath) != s.StatePath {
				return input, errors.New("Auth/Feedback require an isolated /var/lib state directory")
			}
		} else if s.StatePath != "" {
			return input, errors.New("business services must remain in memory")
		}
		if err := validateBackendEnvironment(s.Environment, name); err != nil {
			return input, err
		}
	}
	if input.Fixtures != nil && input.Fixtures.SinkSource != nil {
		dirty = dirty || input.Fixtures.SinkSource.SourceMode == "reviewed-dirty"
	}
	if dirty != o.reviewedDirty {
		return input, errors.New("reviewed dirty inputs require their separate explicit mode; clean input excludes it")
	}
	for _, name := range []string{"smtp", "notification"} {
		sink, ok := input.Sinks[name]
		parts := strings.Split(sink.Image, "@sha256:")
		local := input.Fixtures != nil && input.Fixtures.SinkSource != nil
		if !ok || !local && (len(parts) != 2 || parts[0] == "" || !isSHA256(parts[1])) || local && sink.Image != "" || sink.Port < 1 || sink.Port > 65535 || sink.ControlPort < 1 || sink.ControlPort > 65535 {
			return input, errors.New("synthetic sink requires a digest-pinned image and bounded delivery/control ports")
		}
	}
	seen := map[string]bool{}
	if len(input.Requirements) == 0 {
		return input, errors.New("explicit required acceptance IDs are mandatory")
	}
	for _, id := range input.Requirements {
		if id == "" || seen[id] {
			return input, errors.New("acceptance IDs must be nonempty and unique")
		}
		seen[id] = true
	}
	return input, nil
}
func safeBackendPath(path string) bool {
	return path != "" && path != "." && !filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.HasPrefix(path, "../") && path != ".." && path != ".git" && !strings.HasPrefix(path, ".git/")
}

func safeBackendFixturePath(path string) bool {
	return safeBackendPath(path) && len(path) <= 1024 && !strings.ContainsAny(path, "\x00\r\n")
}

func backendUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' || !strings.ContainsRune("89ab", rune(value[19])) {
		return false
	}
	raw := strings.ReplaceAll(value, "-", "")
	_, err := hex.DecodeString(raw)
	return err == nil && len(raw) == 32 && strings.ToLower(value) == value
}

var backendInstantPattern = regexp.MustCompile(`^20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func backendInstant(value string) bool {
	if !backendInstantPattern.MatchString(value) {
		return false
	}
	instant, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && instant.Year() >= 2000 && instant.Year() <= 2099
}
func validateBackendFixtures(data []byte, input backendInputs) error {
	f := input.Fixtures
	if f == nil || f.Protocol != backendFixtureProtocol || f.BindingsSHA256 != backendBindingsSHA256 || !backendUUID(f.FixtureID) || !backendInstant(f.InitialInstant) {
		return errors.New("fixture protocol, reviewed bindings, UUID and explicit instant required")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return errors.New("invalid fixture fields")
	}
	for _, key := range []string{"protocol", "bindingsSHA256", "fixtureID", "initialInstant", "bootstrap", "owners", "sinkSource"} {
		if _, present := fields[key]; !present {
			return errors.New("missing required fixture field")
		}
	}
	expected := backendBootstrap{
		User:         backendBootstrapUser{"11111111-1111-4111-8111-111111111111", "e2e.initial", "E2E Initial Operator", "1"},
		PasswordFile: backendFileRef{"auth", "/run/umir-fixtures/secrets/bootstrap-password"},
		StateFile:    "/var/lib/umir-auth/auth.json", Mode: "initialize-if-absent-validate-if-present",
	}
	if f.Bootstrap != expected || len(f.Owners) != 5 {
		return errors.New("fixture bootstrap or owner profile differs from reviewed binding")
	}
	for _, name := range backendServices {
		s := input.Services[name]
		for _, file := range s.Files {
			if !safeBackendFixturePath(file.Path) {
				return errors.New("invalid fixture source manifest path")
			}
		}
		port, state := 8080, ""
		if name == "gateway" {
			port = 8082
		}
		if name == "auth" || name == "feedback" {
			state = "/var/lib/umir-" + name
		}
		if f.Owners[name] != name+"-real-v2" || s.Dockerfile != "tests/fixtures/Dockerfile" || s.Port != port || s.StatePath != state || name == "gateway" && s.PublicPort != 8080 || !slices.Equal(s.Command, []string{"serve"}) || !slices.Equal(s.Healthcheck, []string{"CMD", "/fixture-server", "healthcheck"}) {
			return errors.New("fixture wrapper differs from reviewed launch binding")
		}
		for key, value := range s.Environment {
			if (name != "auth" && name != "feedback") || key != "SSL_CERT_FILE" || value != "/run/umir-fixtures/secrets/synthetic-ca.pem" {
				return errors.New("version2 credentials and configuration require private files")
			}
		}
	}
	for _, name := range []string{"smtp", "notification"} {
		sink := input.Sinks[name]
		port, control := 2525, 8025
		if name == "notification" {
			port, control = 3128, 8026
		}
		if sink.Port != port || sink.ControlPort != control || len(sink.Command) > 0 && !slices.Equal(sink.Command, []string{"serve"}) {
			return errors.New("fixture sink differs from reviewed launch binding")
		}
	}
	if source := f.SinkSource; source != nil {
		if !filepath.IsAbs(source.Path) || !isSHA(source.Ref) || (source.SourceMode != "clean" && source.SourceMode != "reviewed-dirty") || source.Dockerfile != "tests/http/fixtures/sinks/Dockerfile" || len(source.Files) == 0 || len(source.Files) > 10000 {
			return errors.New("local sink source requires a complete reviewed Factory manifest")
		}
		for _, file := range source.Files {
			if !safeBackendFixturePath(file.Path) {
				return errors.New("invalid fixture sink manifest path")
			}
		}
	}
	return nil
}
func snapshotBackendSource(ctx context.Context, name string, s backendSource, destination string, executor commandExecutor) (backendSourceEvidence, error) {
	evidence := backendSourceEvidence{Ref: s.Ref, SourceMode: s.SourceMode, Files: slices.Clone(s.Files)}
	origin, _, err := executor.Run(ctx, s.Path, commandEnv(), "git", "remote", "get-url", "origin")
	remote := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(string(origin)), ".git"), "/")
	if err != nil || remote != "https://github.com/JustIkra/umir-"+name && remote != "git@github.com:JustIkra/umir-"+name {
		return evidence, errors.New("candidate has an unknown service origin")
	}
	head, _, err := executor.Run(ctx, s.Path, commandEnv(), "git", "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != s.Ref {
		return evidence, errors.New("candidate HEAD differs from exact source ref")
	}
	status, _, err := executor.Run(ctx, s.Path, commandEnv(), "git", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return evidence, errors.New("candidate status unavailable")
	}
	if s.SourceMode == "clean" && len(bytes.TrimSpace(status)) > 0 {
		return evidence, errors.New("clean candidate has unreviewed changes")
	}
	tracked, _, err := executor.Run(ctx, s.Path, commandEnv(), "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return evidence, errors.New("candidate file inventory unavailable")
	}
	inventory := []string{}
	for _, path := range strings.Split(strings.TrimSuffix(string(tracked), "\x00"), "\x00") {
		if !safeBackendPath(path) {
			return evidence, errors.New("invalid candidate inventory path")
		}
		if _, err := os.Lstat(filepath.Join(s.Path, path)); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return evidence, fmt.Errorf("inspect candidate inventory: %w", err)
		}
		inventory = append(inventory, path)
	}
	sort.Strings(inventory)
	sort.Slice(evidence.Files, func(i, j int) bool { return evidence.Files[i].Path < evidence.Files[j].Path })
	if len(inventory) != len(evidence.Files) {
		return evidence, errors.New("candidate contains omitted or added build context files")
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return evidence, err
	}
	dockerfileFound := false
	for i, file := range evidence.Files {
		if !safeBackendPath(file.Path) || file.Path != inventory[i] || !isSHA256(file.SHA256) || (file.Mode != 0o644 && file.Mode != 0o755) {
			return evidence, errors.New("invalid or incomplete reviewed build context manifest")
		}
		path := filepath.Join(s.Path, file.Path)
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return evidence, err
		}
		base, err := filepath.EvalSymlinks(s.Path)
		if err != nil {
			return evidence, err
		}
		canonical := filepath.Join(base, file.Path)
		if resolved != canonical {
			return evidence, errors.New("source symlinks are forbidden in reviewed snapshots")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return evidence, errors.New("source manifest entry is not a regular file")
		}
		if uint32(info.Mode().Perm()) != file.Mode {
			return evidence, errors.New("source file mode differs from reviewed build context")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return evidence, err
		}
		if backendDigest(data) != file.SHA256 {
			return evidence, errors.New("source content differs from reviewed manifest")
		}
		target := filepath.Join(destination, file.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return evidence, err
		}
		if err := os.WriteFile(target, data, os.FileMode(file.Mode)); err != nil {
			return evidence, err
		}
		dockerfileFound = dockerfileFound || file.Path == s.Dockerfile
	}
	if !dockerfileFound {
		return evidence, errors.New("dockerfile is absent from reviewed context")
	}
	modulePath, moduleName := "go.mod", name
	if name == "factory" {
		modulePath, moduleName = "tests/http/go.mod", "factory/tests/http"
	}
	module, err := os.ReadFile(filepath.Join(destination, modulePath))
	if err != nil || !strings.Contains(string(module), "module github.com/JustIkra/umir-"+moduleName+"\n") {
		return evidence, errors.New("candidate snapshot has the wrong service module")
	}
	excluded, _, err := executor.Run(ctx, s.Path, commandEnv(), "git", "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return evidence, errors.New("excluded input inventory unavailable")
	}
	evidence.ExcludedIgnored = strings.Split(strings.TrimSuffix(string(excluded), "\x00"), "\x00")
	if len(excluded) == 0 {
		evidence.ExcludedIgnored = []string{}
	}
	sort.Strings(evidence.ExcludedIgnored)
	encoded, err := json.Marshal(evidence.Files)
	if err != nil {
		return evidence, err
	}
	evidence.ContextSHA256 = backendDigest(encoded)
	return evidence, nil
}
