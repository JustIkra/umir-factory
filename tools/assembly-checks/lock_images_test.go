package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRegistry answers `docker buildx imagetools inspect` from in-memory images.
type fakeRegistry struct {
	// raw documents by reference as asked ("repo:tag" or "repo@digest").
	raw    map[string][]byte
	labels map[string]string // reference -> revision label
	calls  []string
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (f *fakeRegistry) Run(_ context.Context, _ string, _ []string, name string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name != "docker" || len(args) < 4 || args[0] != "buildx" || args[1] != "imagetools" || args[2] != "inspect" {
		return nil, []byte("unexpected command"), fmt.Errorf("unexpected command %s", name)
	}
	rest := args[3:]
	if rest[0] == "--raw" {
		if data, ok := f.raw[rest[1]]; ok {
			return data, nil, nil
		}
		return nil, []byte("not found: " + rest[1]), fmt.Errorf("exit status 1")
	}
	if len(rest) == 3 && rest[1] == "--format" {
		label, ok := f.labels[rest[0]]
		if !ok {
			return nil, []byte("not found: " + rest[0]), fmt.Errorf("exit status 1")
		}
		body, err := json.Marshal(map[string]any{"config": map[string]any{"Labels": map[string]string{revisionLabel: label}}})
		if err != nil {
			return nil, nil, err
		}
		return body, nil, nil
	}
	return nil, []byte("unexpected arguments"), fmt.Errorf("unexpected arguments")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type fakeImage struct {
	index, platform, config string
}

// addImage registers an index with an amd64 and an arm64 manifest under a reference.
func (f *fakeRegistry) addImage(t *testing.T, repository, tag, seed, revision string) fakeImage {
	t.Helper()
	configRaw := []byte("config-" + seed)
	config := digestOf(configRaw)
	manifest := mustJSON(t, map[string]any{"schemaVersion": 2, "config": map[string]any{"digest": config}})
	platform := digestOf(manifest)
	other := digestOf([]byte("arm-" + seed))
	index := mustJSON(t, map[string]any{"schemaVersion": 2, "manifests": []any{
		map[string]any{"digest": platform, "platform": map[string]string{"os": "linux", "architecture": "amd64"}},
		map[string]any{"digest": other, "platform": map[string]string{"os": "linux", "architecture": "arm64"}},
	}})
	indexDigest := digestOf(index)
	if f.raw == nil {
		f.raw = map[string][]byte{}
		f.labels = map[string]string{}
	}
	if tag != "" {
		f.raw[repository+":"+tag] = index
	}
	f.raw[repository+"@"+indexDigest] = index
	f.raw[repository+"@"+platform] = manifest
	f.labels[repository+"@"+platform] = revision
	return fakeImage{indexDigest, platform, config}
}

func sha40(c string) string { return strings.Repeat(c, 40) }

// lockFixture writes a factory checkout with sources.json and a registry holding
// matching images; it returns the registry, factory dir and the images by name.
func lockFixture(t *testing.T) (*fakeRegistry, string, map[string]fakeImage) {
	t.Helper()
	factory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(factory, "scripts", "completion"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(factory, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sources := map[string]any{
		"profile":          "REG1",
		"services":         map[string]string{"auth": sha40("a"), "warehouse": sha40("b")},
		"optionalServices": map[string]string{"feedback": sha40("c")},
	}
	if err := os.WriteFile(filepath.Join(factory, sourcesRelative), mustJSON(t, sources), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := &fakeRegistry{}
	images := map[string]fakeImage{}
	for service, sha := range map[string]string{"auth": sha40("a"), "warehouse": sha40("b"), "feedback": sha40("c")} {
		images[service] = registry.addImage(t, serviceRepository(service), "sha-"+sha, service, sha)
	}
	for name, image := range defaultAuxiliary {
		repository := strings.SplitN(image, ":", 2)[0]
		tag := strings.SplitN(image, ":", 2)[1]
		images[name] = registry.addImage(t, repository, tag, name, "")
	}
	return registry, factory, images
}

func runLock(t *testing.T, registry *fakeRegistry, factory string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := runWithExecutor(context.Background(), append([]string{"lock-images", "--factory-dir", factory}, args...), &stdout, &stderr, registry)
	return code, stdout.String(), stderr.String()
}

func TestLockImagesWriteThenVerify(t *testing.T) {
	registry, factory, images := lockFixture(t)
	if code, _, stderr := runLock(t, registry, factory, "--write"); code != 0 {
		t.Fatalf("write failed: %s", stderr)
	}
	lock, err := readLock(factory)
	if err != nil {
		t.Fatal(err)
	}
	warehouse := lock.Services["warehouse"]
	want := images["warehouse"]
	if warehouse.SourceSHA != sha40("b") || warehouse.Revision != sha40("b") || warehouse.IndexDigest != want.index || warehouse.PlatformDigest != want.platform || warehouse.ConfigDigest != want.config {
		t.Fatalf("warehouse lock = %#v, registry = %#v", warehouse, want)
	}
	if _, ok := lock.Services["feedback"]; !ok {
		t.Fatal("optional feedback service is not locked")
	}
	alpine := lock.Auxiliary["alpine"]
	if alpine.Image != "alpine:3.23" || alpine.Ref != "alpine:3.23@"+images["alpine"].index || alpine.PlatformDigest != images["alpine"].platform {
		t.Fatalf("alpine lock = %#v", alpine)
	}
	if lock.Platform != "linux/amd64" || lock.Profile != "REG1" {
		t.Fatalf("lock header = %q %q", lock.Profile, lock.Platform)
	}
	if code, _, stderr := runLock(t, registry, factory, "--verify"); code != 0 {
		t.Fatalf("verify failed: %s", stderr)
	}
}

func TestLockImagesRejectsRevisionMismatch(t *testing.T) {
	registry, factory, _ := lockFixture(t)
	repository := serviceRepository("auth")
	for ref := range registry.labels {
		if strings.HasPrefix(ref, repository+"@") {
			registry.labels[ref] = sha40("f")
		}
	}
	code, _, stderr := runLock(t, registry, factory, "--write")
	if code == 0 || !strings.Contains(stderr, revisionLabel) {
		t.Fatalf("revision mismatch accepted: code %d %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(factory, lockRelativePath)); err == nil {
		t.Fatal("a lock was written despite the mismatch")
	}
}

func TestLockImagesVerifyDetectsDrift(t *testing.T) {
	cases := map[string]func(*testing.T, *fakeRegistry, string, *imageLock){
		"source pin moved in sources.json": func(t *testing.T, _ *fakeRegistry, factory string, _ *imageLock) {
			sources := map[string]any{"profile": "REG1", "services": map[string]string{"auth": sha40("d"), "warehouse": sha40("b")}, "optionalServices": map[string]string{"feedback": sha40("c")}}
			if err := os.WriteFile(filepath.Join(factory, sourcesRelative), mustJSON(t, sources), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"service missing from the lock": func(_ *testing.T, _ *fakeRegistry, _ string, lock *imageLock) {
			delete(lock.Services, "warehouse")
		},
		"platform digest outside the index": func(_ *testing.T, _ *fakeRegistry, _ string, lock *imageLock) {
			entry := lock.Services["auth"]
			entry.PlatformDigest = digestOf([]byte("elsewhere"))
			lock.Services["auth"] = entry
		},
		"config digest differs": func(_ *testing.T, _ *fakeRegistry, _ string, lock *imageLock) {
			entry := lock.Services["auth"]
			entry.ConfigDigest = digestOf([]byte("other config"))
			lock.Services["auth"] = entry
		},
		"revision label differs in the registry": func(_ *testing.T, registry *fakeRegistry, _ string, _ *imageLock) {
			for ref := range registry.labels {
				if strings.HasPrefix(ref, serviceRepository("warehouse")+"@") {
					registry.labels[ref] = sha40("0")
				}
			}
		},
		"auxiliary platform digest outside the index": func(_ *testing.T, _ *fakeRegistry, _ string, lock *imageLock) {
			entry := lock.Auxiliary["node"]
			entry.PlatformDigest = digestOf([]byte("elsewhere"))
			lock.Auxiliary["node"] = entry
		},
		"auxiliary ref without the digest": func(_ *testing.T, _ *fakeRegistry, _ string, lock *imageLock) {
			entry := lock.Auxiliary["node"]
			entry.Ref = entry.Image
			lock.Auxiliary["node"] = entry
		},
		"auxiliary missing": func(_ *testing.T, _ *fakeRegistry, _ string, lock *imageLock) {
			delete(lock.Auxiliary, "mailpit")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			registry, factory, _ := lockFixture(t)
			if code, _, stderr := runLock(t, registry, factory, "--write"); code != 0 {
				t.Fatalf("write failed: %s", stderr)
			}
			lock, err := readLock(factory)
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, registry, factory, &lock)
			data, err := marshalLock(lock)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(factory, lockRelativePath), data, 0o644); err != nil {
				t.Fatal(err)
			}
			if code, _, stderr := runLock(t, registry, factory, "--verify"); code == 0 {
				t.Fatalf("drift accepted: %s", stderr)
			}
		})
	}
}

func TestLockImagesVerifyIgnoresMovedTags(t *testing.T) {
	registry, factory, _ := lockFixture(t)
	if code, _, stderr := runLock(t, registry, factory, "--write"); code != 0 {
		t.Fatalf("write failed: %s", stderr)
	}
	// The mutable tag now points elsewhere; the locked digest still exists.
	registry.addImage(t, "alpine", "3.23", "alpine-new", "")
	if code, _, stderr := runLock(t, registry, factory, "--verify"); code != 0 {
		t.Fatalf("a moved tag broke a correct lock: %s", stderr)
	}
}

func TestLockImagesNeedsExactlyOneMode(t *testing.T) {
	registry, factory, _ := lockFixture(t)
	for _, args := range [][]string{nil, {"--verify", "--write"}} {
		if code, _, _ := runLock(t, registry, factory, args...); code != 2 {
			t.Fatalf("args %v: code %d, want 2", args, code)
		}
	}
	if len(registry.calls) != 0 {
		t.Fatalf("registry was called for invalid options: %v", registry.calls)
	}
}

// ---- check-images --lock -----------------------------------------------------

type composeDouble struct {
	output []byte
	args   []string
	env    []string
}

func (c *composeDouble) Run(_ context.Context, _ string, env []string, name string, args ...string) ([]byte, []byte, error) {
	c.args = append([]string{name}, args...)
	c.env = env
	return c.output, nil, nil
}

func writeLock(t *testing.T, factory string, lock imageLock) {
	t.Helper()
	data, err := marshalLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(factory, "scripts", "completion"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factory, lockRelativePath), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sampleLock() imageLock {
	lock := imageLock{Profile: "REG1", Platform: lockPlatform, Services: map[string]lockedService{}, Auxiliary: map[string]lockedAux{}}
	for _, service := range []string{"auth", "warehouse"} {
		sha := sha40(service[:1])
		lock.Services[service] = lockedService{SourceSHA: sha, IndexDigest: digestOf([]byte("i" + service)), PlatformDigest: digestOf([]byte("p" + service)), ConfigDigest: digestOf([]byte("c" + service)), Revision: sha}
	}
	for name, image := range defaultAuxiliary {
		index := digestOf([]byte("aux" + name))
		lock.Auxiliary[name] = lockedAux{Image: image, Ref: auxiliaryRef(image, index), IndexDigest: index, PlatformDigest: digestOf([]byte("p" + name))}
	}
	return lock
}

func composeModel(t *testing.T, lock imageLock, override map[string]string) []byte {
	t.Helper()
	services := map[string]composeService{
		"private-init": {Image: lock.Auxiliary["alpine"].Ref},
		"transport":    {Image: lock.Auxiliary["nginx"].Ref},
		"auth-ready":   {Image: lock.Auxiliary["node"].Ref},
		"demo-smtp":    {Image: lock.Auxiliary["mailpit"].Ref},
		"auth":         {Image: composeImage("auth", lock.Services["auth"])},
		"warehouse":    {Image: composeImage("warehouse", lock.Services["warehouse"])},
	}
	for name, image := range override {
		services[name] = composeService{Image: image}
	}
	return mustJSON(t, map[string]any{"services": services})
}

func TestCheckImagesLockAcceptsLockedProfile(t *testing.T) {
	factory := t.TempDir()
	lock := sampleLock()
	writeLock(t, factory, lock)
	double := &composeDouble{output: composeModel(t, lock, nil)}
	if err := checkImagesLock(context.Background(), factory, double); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(double.args, " ")
	for _, want := range []string{"compose", "-f compose.completion.yaml", "-f tests/examples/completion/compose.mail.yaml", "config --format json"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("command %q lacks %q", joined, want)
		}
	}
	environment := strings.Join(double.env, "\n")
	if !strings.Contains(environment, "COMPLETION_AUTH_IMAGE="+composeImage("auth", lock.Services["auth"])) {
		t.Fatal("service image variable is not taken from the lock")
	}
}

func TestCheckImagesLockRejects(t *testing.T) {
	lock := sampleLock()
	cases := map[string]map[string]string{
		"tag without a digest":       {"transport": "nginxinc/nginx-unprivileged:1.30.5-alpine"},
		"digest not in the lock":     {"transport": "nginxinc/nginx-unprivileged:1.30.5-alpine@" + digestOf([]byte("other"))},
		"service image off the lock": {"auth": serviceRepository("auth") + ":sha-" + sha40("a") + "@" + digestOf([]byte("other"))},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			factory := t.TempDir()
			writeLock(t, factory, lock)
			double := &composeDouble{output: composeModel(t, lock, override)}
			if err := checkImagesLock(context.Background(), factory, double); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	t.Run("lock entry nothing runs", func(t *testing.T) {
		factory := t.TempDir()
		writeLock(t, factory, lock)
		model := composeModel(t, lock, nil)
		var parsed map[string]map[string]any
		if err := json.Unmarshal(model, &parsed); err != nil {
			t.Fatal(err)
		}
		delete(parsed["services"], "demo-smtp")
		double := &composeDouble{output: mustJSON(t, parsed)}
		if err := checkImagesLock(context.Background(), factory, double); err == nil {
			t.Fatal("unused auxiliary image accepted")
		}
	})
}

func TestCheckImagesLockComparesStandRecord(t *testing.T) {
	lock := sampleLock()
	factory := t.TempDir()
	writeLock(t, factory, lock)
	auth := lock.Services["auth"]
	record := func(platform string) {
		body := mustJSON(t, map[string]any{"images": map[string]any{"auth": map[string]string{
			"sourceSHA": auth.SourceSHA, "indexDigest": auth.IndexDigest, "platformDigest": platform, "configDigest": auth.ConfigDigest,
		}}})
		if err := os.MkdirAll(filepath.Join(factory, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(factory, "docs", "completion-stand.json"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	double := &composeDouble{output: composeModel(t, lock, nil)}
	record(auth.PlatformDigest)
	if err := checkImagesLock(context.Background(), factory, double); err != nil {
		t.Fatal(err)
	}
	record(digestOf([]byte("another")))
	if err := checkImagesLock(context.Background(), factory, double); err == nil {
		t.Fatal("a stand record that differs from the lock at the same source was accepted")
	}
}

func TestLockImagesRecordsUnpublishedOptionalService(t *testing.T) {
	registry, factory, _ := lockFixture(t)
	repository := serviceRepository("feedback")
	for ref := range registry.raw {
		if strings.HasPrefix(ref, repository) {
			delete(registry.raw, ref)
		}
	}
	if code, _, stderr := runLock(t, registry, factory, "--write"); code != 0 {
		t.Fatalf("write failed: %s", stderr)
	}
	lock, err := readLock(factory)
	if err != nil {
		t.Fatal(err)
	}
	if _, locked := lock.Services["feedback"]; locked || lock.Unpublished["feedback"] != sha40("c") {
		t.Fatalf("feedback should be recorded as unpublished: %#v", lock)
	}
	if code, _, stderr := runLock(t, registry, factory, "--verify"); code != 0 {
		t.Fatalf("verify failed: %s", stderr)
	}
	// Once the registry serves it, the lock must be regenerated.
	registry.addImage(t, repository, "sha-"+sha40("c"), "feedback", sha40("c"))
	if code, _, _ := runLock(t, registry, factory, "--verify"); code == 0 {
		t.Fatal("a published image kept the unpublished record")
	}
}

func TestLockImagesRequiredServiceMustBePublished(t *testing.T) {
	registry, factory, _ := lockFixture(t)
	for ref := range registry.raw {
		if strings.HasPrefix(ref, serviceRepository("auth")) {
			delete(registry.raw, ref)
		}
	}
	if code, _, _ := runLock(t, registry, factory, "--write"); code == 0 {
		t.Fatal("an unpublished required service was written into the lock")
	}
}

func TestCheckImagesLockAllowsUnpublishedByTag(t *testing.T) {
	lock := sampleLock()
	lock.Unpublished = map[string]string{"feedback": sha40("c")}
	factory := t.TempDir()
	writeLock(t, factory, lock)
	double := &composeDouble{output: composeModel(t, lock, map[string]string{"feedback": unpublishedImage("feedback", sha40("c"))})}
	if err := checkImagesLock(context.Background(), factory, double); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(double.args, " "), "--profile feedback") {
		t.Fatal("the feedback profile is not checked")
	}
	other := &composeDouble{output: composeModel(t, lock, map[string]string{"feedback": unpublishedImage("feedback", sha40("d"))})}
	if err := checkImagesLock(context.Background(), factory, other); err == nil {
		t.Fatal("a feedback image from another source was accepted")
	}
}
