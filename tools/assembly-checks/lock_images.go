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
	"sort"
	"strings"
)

const (
	lockRelativePath  = "scripts/completion/images.lock.json"
	sourcesRelative   = "scripts/completion/sources.json"
	lockPlatform      = "linux/amd64"
	lockImageRegistry = "ghcr.io/justikra/umir-"
	revisionLabel     = "org.opencontainers.image.revision"
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// Auxiliary images the completion profile starts besides the five services.
	defaultAuxiliary = map[string]string{
		"alpine":  "alpine:3.23",
		"nginx":   "nginxinc/nginx-unprivileged:1.30.5-alpine",
		"node":    "node:24.21.0-alpine",
		"mailpit": "axllent/mailpit:v1.31.5",
	}
)

// imageLock is scripts/completion/images.lock.json: the exact registry identity of
// every image the completion profile runs.
type imageLock struct {
	Profile   string                   `json:"profile"`
	Platform  string                   `json:"platform"`
	Services  map[string]lockedService `json:"services"`
	Auxiliary map[string]lockedAux     `json:"auxiliary"`
	// Unpublished lists optional services (source pin sources.json) whose image the
	// registry does not serve. They are built from the exact source and run by tag.
	Unpublished map[string]string `json:"unpublished,omitempty"`
}

type lockedService struct {
	SourceSHA      string `json:"sourceSHA"`
	IndexDigest    string `json:"indexDigest"`
	PlatformDigest string `json:"platformDigest"`
	ConfigDigest   string `json:"configDigest"`
	Revision       string `json:"revision"`
}

type lockedAux struct {
	Image          string `json:"image"`
	Ref            string `json:"ref"`
	IndexDigest    string `json:"indexDigest"`
	PlatformDigest string `json:"platformDigest"`
}

type sourcesManifest struct {
	Profile          string            `json:"profile"`
	Services         map[string]string `json:"services"`
	OptionalServices map[string]string `json:"optionalServices"`
}

func (m sourcesManifest) pins() map[string]string {
	pins := make(map[string]string, len(m.Services)+len(m.OptionalServices))
	for service, sha := range m.Services {
		pins[service] = sha
	}
	for service, sha := range m.OptionalServices {
		pins[service] = sha
	}
	return pins
}

type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Platform  *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform"`
}

type ociDocument struct {
	Manifests []ociDescriptor `json:"manifests"`
	Config    *ociDescriptor  `json:"config"`
}

type ociImageConfig struct {
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"config"`
}

// registryReader reads image identities through the Docker CLI, so tests replace it
// with a command double.
type registryReader struct {
	executor   commandExecutor
	factoryDir string
}

// errImageUnavailable marks an image the registry does not serve at all (no such
// package or tag, or anonymous access denied), as opposed to a failed request.
var errImageUnavailable = errors.New("image is not served by the registry")

var unavailableText = regexp.MustCompile(`(?i)\b(403|404)\b|not found|denied|unauthorized|manifest unknown|name unknown`)

func (r registryReader) inspect(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"buildx", "imagetools", "inspect"}, args...)
	out, errOut, err := r.executor.Run(ctx, r.factoryDir, commandEnv(), "docker", full...)
	if err != nil {
		wrapped := fmt.Errorf("docker %s: %s: %w", strings.Join(full, " "), strings.TrimSpace(string(errOut)), err)
		if unavailableText.Match(errOut) {
			return nil, errors.Join(errImageUnavailable, wrapped)
		}
		return nil, wrapped
	}
	return out, nil
}

// index reads the OCI index behind a reference. Its digest is the SHA-256 of the raw
// bytes the registry serves.
func (r registryReader) index(ctx context.Context, ref string) (string, ociDocument, error) {
	raw, err := r.inspect(ctx, "--raw", ref)
	if err != nil {
		return "", ociDocument{}, err
	}
	var document ociDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", ociDocument{}, fmt.Errorf("parse index of %s: %w", ref, err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), document, nil
}

func platformDigest(document ociDocument) (string, error) {
	want := strings.SplitN(lockPlatform, "/", 2)
	for _, entry := range document.Manifests {
		if entry.Platform != nil && entry.Platform.OS == want[0] && entry.Platform.Architecture == want[1] {
			return entry.Digest, nil
		}
	}
	return "", fmt.Errorf("the index has no %s manifest", lockPlatform)
}

func indexContains(document ociDocument, digest string) bool {
	for _, entry := range document.Manifests {
		if entry.Digest == digest {
			return true
		}
	}
	return false
}

func (r registryReader) configDigest(ctx context.Context, repository, platform string) (string, error) {
	raw, err := r.inspect(ctx, "--raw", repository+"@"+platform)
	if err != nil {
		return "", err
	}
	var document ociDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", fmt.Errorf("parse manifest %s: %w", platform, err)
	}
	if document.Config == nil || !digestPattern.MatchString(document.Config.Digest) {
		return "", fmt.Errorf("manifest %s has no config digest", platform)
	}
	return document.Config.Digest, nil
}

// revision reads the OCI revision label of the platform image.
func (r registryReader) revision(ctx context.Context, repository, platform string) (string, error) {
	out, err := r.inspect(ctx, repository+"@"+platform, "--format", "{{json .Image}}")
	if err != nil {
		return "", err
	}
	var direct ociImageConfig
	if err := json.Unmarshal(out, &direct); err == nil && direct.Config.Labels != nil {
		return direct.Config.Labels[revisionLabel], nil
	}
	var byPlatform map[string]ociImageConfig
	if err := json.Unmarshal(out, &byPlatform); err != nil {
		return "", fmt.Errorf("parse image config of %s: %w", platform, err)
	}
	return byPlatform[lockPlatform].Config.Labels[revisionLabel], nil
}

func readSources(factoryDir string) (sourcesManifest, error) {
	var manifest sourcesManifest
	data, err := os.ReadFile(filepath.Join(factoryDir, sourcesRelative))
	if err != nil {
		return manifest, fmt.Errorf("read sources: %w", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("parse sources: %w", err)
	}
	for service, sha := range manifest.pins() {
		if !isSHA(sha) {
			return manifest, fmt.Errorf("source pin for %s is not a full commit SHA", service)
		}
	}
	return manifest, nil
}

func readLock(factoryDir string) (imageLock, error) {
	var lock imageLock
	data, err := os.ReadFile(filepath.Join(factoryDir, lockRelativePath))
	if err != nil {
		return lock, fmt.Errorf("read image lock: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lock); err != nil {
		return lock, fmt.Errorf("parse image lock: %w", err)
	}
	return lock, nil
}

func marshalLock(lock imageLock) ([]byte, error) {
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func serviceRepository(service string) string { return lockImageRegistry + service }

// auxiliaryRef is the reference Compose runs for an auxiliary image: its tag, kept for
// reading, and the digest of the multi-architecture index.
func auxiliaryRef(image, indexDigest string) string { return image + "@" + indexDigest }

func lockService(ctx context.Context, reader registryReader, service, sourceSHA string) (lockedService, error) {
	repository := serviceRepository(service)
	indexDigest, document, err := reader.index(ctx, repository+":sha-"+sourceSHA)
	if err != nil {
		return lockedService{}, err
	}
	platform, err := platformDigest(document)
	if err != nil {
		return lockedService{}, fmt.Errorf("%s: %w", service, err)
	}
	config, err := reader.configDigest(ctx, repository, platform)
	if err != nil {
		return lockedService{}, err
	}
	revision, err := reader.revision(ctx, repository, platform)
	if err != nil {
		return lockedService{}, err
	}
	if revision != sourceSHA {
		return lockedService{}, fmt.Errorf("%s: image label %s is %q, the source is %s", service, revisionLabel, revision, sourceSHA)
	}
	return lockedService{SourceSHA: sourceSHA, IndexDigest: indexDigest, PlatformDigest: platform, ConfigDigest: config, Revision: revision}, nil
}

func lockAuxiliary(ctx context.Context, reader registryReader, image string) (lockedAux, error) {
	indexDigest, document, err := reader.index(ctx, image)
	if err != nil {
		return lockedAux{}, err
	}
	platform, err := platformDigest(document)
	if err != nil {
		return lockedAux{}, fmt.Errorf("%s: %w", image, err)
	}
	return lockedAux{Image: image, Ref: auxiliaryRef(image, indexDigest), IndexDigest: indexDigest, PlatformDigest: platform}, nil
}

// buildLock resolves every pin in sources.json (and every auxiliary image) in the
// registry. Auxiliary image names are taken from the existing lock when there is one.
func buildLock(ctx context.Context, reader registryReader, manifest sourcesManifest, previous *imageLock) (imageLock, error) {
	lock := imageLock{Profile: manifest.Profile, Platform: lockPlatform, Services: map[string]lockedService{}, Auxiliary: map[string]lockedAux{}}
	for service, sha := range manifest.pins() {
		locked, err := lockService(ctx, reader, service, sha)
		if _, optional := manifest.OptionalServices[service]; optional && errors.Is(err, errImageUnavailable) {
			if lock.Unpublished == nil {
				lock.Unpublished = map[string]string{}
			}
			lock.Unpublished[service] = sha
			continue
		}
		if err != nil {
			return lock, err
		}
		lock.Services[service] = locked
	}
	images := make(map[string]string, len(defaultAuxiliary))
	for name, image := range defaultAuxiliary {
		images[name] = image
	}
	if previous != nil {
		for name, aux := range previous.Auxiliary {
			images[name] = aux.Image
		}
	}
	for name, image := range images {
		locked, err := lockAuxiliary(ctx, reader, image)
		if err != nil {
			return lock, err
		}
		lock.Auxiliary[name] = locked
	}
	return lock, nil
}

// verifyLock checks the lock against sources.json and the registry. It reads by
// digest only, so a moved tag does not fail a lock that is still correct.
func verifyLock(ctx context.Context, reader registryReader, manifest sourcesManifest, lock imageLock) error {
	var problems []error
	if lock.Platform != lockPlatform {
		problems = append(problems, fmt.Errorf("lock platform is %q, expected %s", lock.Platform, lockPlatform))
	}
	if lock.Profile != manifest.Profile {
		problems = append(problems, fmt.Errorf("lock profile is %q, sources say %q", lock.Profile, manifest.Profile))
	}
	pins := manifest.pins()
	for _, service := range sortedKeys(pins) {
		locked, exists := lock.Services[service]
		if sha, unpublished := lock.Unpublished[service]; !exists && unpublished {
			if sha != pins[service] {
				problems = append(problems, fmt.Errorf("%s: lock records source %s as unpublished, sources.json has %s", service, sha, pins[service]))
			} else if err := verifyStillUnpublished(ctx, reader, service, sha); err != nil {
				problems = append(problems, err)
			}
			continue
		}
		if !exists {
			problems = append(problems, fmt.Errorf("%s: no lock entry for source %s", service, pins[service]))
			continue
		}
		if locked.SourceSHA != pins[service] {
			problems = append(problems, fmt.Errorf("%s: lock source %s differs from sources.json %s", service, locked.SourceSHA, pins[service]))
			continue
		}
		if err := verifyService(ctx, reader, service, locked); err != nil {
			problems = append(problems, err)
		}
	}
	for _, service := range sortedKeys(lock.Unpublished) {
		if _, optional := manifest.OptionalServices[service]; !optional {
			problems = append(problems, fmt.Errorf("%s: only optional services can be unpublished", service))
		}
	}
	for _, service := range sortedKeys(lock.Services) {
		if _, exists := pins[service]; !exists {
			problems = append(problems, fmt.Errorf("%s: lock entry without a source pin", service))
		}
	}
	for _, name := range sortedKeys(lock.Auxiliary) {
		if err := verifyAuxiliary(ctx, reader, name, lock.Auxiliary[name]); err != nil {
			problems = append(problems, err)
		}
	}
	for name := range defaultAuxiliary {
		if _, exists := lock.Auxiliary[name]; !exists {
			problems = append(problems, fmt.Errorf("auxiliary image %s is not in the lock", name))
		}
	}
	return errors.Join(problems...)
}

func verifyStillUnpublished(ctx context.Context, reader registryReader, service, sha string) error {
	_, _, err := reader.index(ctx, serviceRepository(service)+":sha-"+sha)
	switch {
	case err == nil:
		return fmt.Errorf("%s: the image is published now; run lock-images --write to pin it by digest", service)
	case errors.Is(err, errImageUnavailable):
		return nil
	default:
		return fmt.Errorf("%s: %w", service, err)
	}
}

func verifyService(ctx context.Context, reader registryReader, service string, locked lockedService) error {
	for field, value := range map[string]string{"indexDigest": locked.IndexDigest, "platformDigest": locked.PlatformDigest, "configDigest": locked.ConfigDigest} {
		if !digestPattern.MatchString(value) {
			return fmt.Errorf("%s: %s is not a sha256 digest", service, field)
		}
	}
	repository := serviceRepository(service)
	indexDigest, document, err := reader.index(ctx, repository+"@"+locked.IndexDigest)
	if err != nil {
		return fmt.Errorf("%s: %w", service, err)
	}
	if indexDigest != locked.IndexDigest {
		return fmt.Errorf("%s: the registry serves index %s, the lock has %s", service, indexDigest, locked.IndexDigest)
	}
	if !indexContains(document, locked.PlatformDigest) {
		return fmt.Errorf("%s: platform digest %s is not in the index", service, locked.PlatformDigest)
	}
	config, err := reader.configDigest(ctx, repository, locked.PlatformDigest)
	if err != nil {
		return fmt.Errorf("%s: %w", service, err)
	}
	if config != locked.ConfigDigest {
		return fmt.Errorf("%s: config digest %s differs from the lock %s", service, config, locked.ConfigDigest)
	}
	revision, err := reader.revision(ctx, repository, locked.PlatformDigest)
	if err != nil {
		return fmt.Errorf("%s: %w", service, err)
	}
	if revision != locked.SourceSHA || locked.Revision != locked.SourceSHA {
		return fmt.Errorf("%s: image label %s is %q, lock source is %s", service, revisionLabel, revision, locked.SourceSHA)
	}
	return nil
}

func verifyAuxiliary(ctx context.Context, reader registryReader, name string, locked lockedAux) error {
	if !digestPattern.MatchString(locked.IndexDigest) || !digestPattern.MatchString(locked.PlatformDigest) {
		return fmt.Errorf("auxiliary %s: digests are not sha256 digests", name)
	}
	if locked.Ref != auxiliaryRef(locked.Image, locked.IndexDigest) {
		return fmt.Errorf("auxiliary %s: ref %q does not match image and index digest", name, locked.Ref)
	}
	indexDigest, document, err := reader.index(ctx, imageRepository(locked.Image)+"@"+locked.IndexDigest)
	if err != nil {
		return fmt.Errorf("auxiliary %s: %w", name, err)
	}
	if indexDigest != locked.IndexDigest {
		return fmt.Errorf("auxiliary %s: the registry serves index %s, the lock has %s", name, indexDigest, locked.IndexDigest)
	}
	if !indexContains(document, locked.PlatformDigest) {
		return fmt.Errorf("auxiliary %s: platform digest %s is not in the index", name, locked.PlatformDigest)
	}
	return nil
}

// imageRepository drops the tag of a reference such as "node:24.21.0-alpine".
func imageRepository(image string) string {
	slash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > slash {
		return image[:colon]
	}
	return image
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// unpublishedImage is the tag reference of a service the registry does not serve.
func unpublishedImage(service, sha string) string {
	return serviceRepository(service) + ":sha-" + sha
}

// composeImage is the reference prepare.mjs writes into compose.env for a service.
func composeImage(service string, locked lockedService) string {
	return serviceRepository(service) + ":sha-" + locked.SourceSHA + "@" + locked.PlatformDigest
}

func runLockImages(ctx context.Context, factoryDir string, options cliOptions, executor commandExecutor, stdout, stderr io.Writer) int {
	manifest, err := readSources(factoryDir)
	if err != nil {
		writeFailure(stderr, "%v\n", err)
		return 1
	}
	reader := registryReader{executor: executor, factoryDir: factoryDir}
	if options.verify {
		lock, err := readLock(factoryDir)
		if err != nil {
			writeFailure(stderr, "%v\n", err)
			return 1
		}
		if err := verifyLock(ctx, reader, manifest, lock); err != nil {
			writeFailure(stderr, "image lock check failed:\n%v\n", err)
			return 1
		}
		if err := writeMessage(stdout, "Image lock matches sources.json and the registry\n"); err != nil {
			return 1
		}
		return 0
	}
	var previous *imageLock
	if existing, err := readLock(factoryDir); err == nil {
		previous = &existing
	}
	lock, err := buildLock(ctx, reader, manifest, previous)
	if err != nil {
		writeFailure(stderr, "%v\n", err)
		return 1
	}
	data, err := marshalLock(lock)
	if err != nil {
		writeFailure(stderr, "%v\n", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(factoryDir, lockRelativePath), data, 0o644); err != nil {
		writeFailure(stderr, "write image lock: %v\n", err)
		return 1
	}
	if err := writeMessage(stdout, "Wrote %s\n", lockRelativePath); err != nil {
		return 1
	}
	return 0
}

// Compose files whose images must follow the lock.
var lockedComposeFiles = []string{"compose.completion.yaml", "tests/examples/completion/compose.mail.yaml"}

type completionStandRecord struct {
	Images map[string]struct {
		SourceSHA      string `json:"sourceSHA"`
		IndexDigest    string `json:"indexDigest"`
		PlatformDigest string `json:"platformDigest"`
		ConfigDigest   string `json:"configDigest"`
	} `json:"images"`
}

// lockEnvironment is the synthetic environment that lets Compose resolve the
// completion profile: the image variables come from the lock, the rest are placeholders.
func lockEnvironment(lock imageLock, scratch string) []string {
	env := []string{
		"COMPOSE_PROJECT_NAME=umir-completion-lockcheck",
		"COMPLETION_PRIVATE_DIR=" + scratch,
		"COMPLETION_TEST_MAIL_DIR=" + scratch,
		"COMPLETION_ORIGIN=http://127.0.0.1:8080",
		"COMPLETION_COOKIE_MODE=loopback-development",
	}
	for _, service := range sortedKeys(lock.Services) {
		env = append(env, fmt.Sprintf("COMPLETION_%s_IMAGE=%s", strings.ToUpper(service), composeImage(service, lock.Services[service])))
	}
	for _, service := range sortedKeys(lock.Unpublished) {
		env = append(env, fmt.Sprintf("COMPLETION_%s_IMAGE=%s", strings.ToUpper(service), unpublishedImage(service, lock.Unpublished[service])))
	}
	return env
}

// checkImagesLock requires every image of the completion profile (and of its test
// mail overlay) to carry a digest that the lock vouches for.
func checkImagesLock(ctx context.Context, factoryDir string, executor commandExecutor) (result error) {
	lock, err := readLock(factoryDir)
	if err != nil {
		return err
	}
	// Compose reads the env files of the services, so an empty set stands in for the
	// private bundle; nothing from it is used.
	scratch, err := os.MkdirTemp("", "umir-lock-check-")
	if err != nil {
		return fmt.Errorf("create scratch directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(scratch); removeErr != nil {
			result = errors.Join(result, fmt.Errorf("remove scratch directory: %w", removeErr))
		}
	}()
	if err := os.Mkdir(filepath.Join(scratch, "env"), 0o700); err != nil {
		return fmt.Errorf("create scratch directory: %w", err)
	}
	for _, name := range []string{"warehouse", "serving", "gateway", "feedback"} {
		if err := os.WriteFile(filepath.Join(scratch, "env", name+".env"), nil, 0o600); err != nil {
			return fmt.Errorf("create scratch directory: %w", err)
		}
	}
	args := []string{"compose"}
	_, locked := lock.Services["feedback"]
	if _, unpublished := lock.Unpublished["feedback"]; locked || unpublished {
		args = append(args, "--profile", "feedback")
	}
	for _, file := range lockedComposeFiles {
		args = append(args, "-f", file)
	}
	args = append(args, "config", "--format", "json")
	out, errOut, err := executor.Run(ctx, factoryDir, commandEnv(lockEnvironment(lock, scratch)...), "docker", args...)
	if err != nil {
		return fmt.Errorf("docker compose config failed: %s: %w", strings.TrimSpace(string(errOut)), err)
	}
	var config composeConfig
	if err := json.Unmarshal(out, &config); err != nil {
		return fmt.Errorf("parse Compose JSON: %w", err)
	}
	allowed := map[string]string{}
	for service, locked := range lock.Services {
		allowed[composeImage(service, locked)] = "service " + service
	}
	for name, aux := range lock.Auxiliary {
		allowed[aux.Ref] = "auxiliary " + name
	}
	// An unpublished optional image has no digest to pin; its exact source is the pin.
	tagged := map[string]bool{}
	for service, sha := range lock.Unpublished {
		tagged[unpublishedImage(service, sha)] = true
	}
	var problems []error
	used := map[string]bool{}
	for _, name := range sortedKeys(config.Services) {
		image := config.Services[name].Image
		if tagged[image] {
			continue
		}
		if !strings.Contains(image, "@sha256:") {
			problems = append(problems, fmt.Errorf("%s: image %q has no digest", name, image))
			continue
		}
		if _, ok := allowed[image]; !ok {
			problems = append(problems, fmt.Errorf("%s: image %q is not in the lock", name, image))
			continue
		}
		used[image] = true
	}
	for ref, what := range allowed {
		if !used[ref] && strings.HasPrefix(what, "auxiliary") {
			problems = append(problems, fmt.Errorf("%s (%s) is in the lock but no profile service runs it", what, ref))
		}
	}
	if err := checkStandRecord(factoryDir, lock); err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

// checkStandRecord compares docs/completion-stand.json with the lock for every
// service built from the same source.
func checkStandRecord(factoryDir string, lock imageLock) error {
	data, err := os.ReadFile(filepath.Join(factoryDir, "docs", "completion-stand.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read completion stand record: %w", err)
	}
	var record completionStandRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return fmt.Errorf("parse completion stand record: %w", err)
	}
	var problems []error
	for _, service := range sortedKeys(record.Images) {
		recorded := record.Images[service]
		locked, exists := lock.Services[service]
		if !exists || locked.SourceSHA != recorded.SourceSHA {
			continue
		}
		if locked.IndexDigest != recorded.IndexDigest || locked.PlatformDigest != recorded.PlatformDigest || locked.ConfigDigest != recorded.ConfigDigest {
			problems = append(problems, fmt.Errorf("docs/completion-stand.json differs from the lock for %s at the same source %s", service, recorded.SourceSHA))
		}
	}
	return errors.Join(problems...)
}
