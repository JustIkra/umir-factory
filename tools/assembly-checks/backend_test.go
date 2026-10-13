package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBackendArgumentsRequireFiveExactSourcesAndExplicitInputs(t *testing.T) {
	args := backendTestArgs(t.TempDir(), "/tmp/inputs.json", "/tmp/harness", t.TempDir())
	if _, err := parseOptions("http-acceptance", args); err != nil {
		t.Fatalf("backend-only should accept five backend refs without frontend: %v", err)
	}
	for _, extra := range [][]string{{"--selected"}, {"--auth"}, {"--realtime"}, {"--frontend-ref", strings.Repeat("a", 40)}} {
		if _, err := parseOptions("http-acceptance", append(slices.Clone(args), extra...)); err == nil {
			t.Fatalf("accepted incompatible mode %v", extra)
		}
	}
	for _, flag := range []string{"--auth-ref", "--feedback-ref", "--backend-inputs", "--backend-inputs-sha256", "--backend-harness", "--backend-harness-sha256", "--backend-evidence-dir"} {
		altered := slices.Clone(args)
		i := slices.Index(altered, flag)
		altered = append(altered[:i], altered[i+2:]...)
		if _, err := parseOptions("http-acceptance", altered); err == nil {
			t.Fatalf("accepted missing %s", flag)
		}
	}
	if _, err := parseOptions("http-acceptance", []string{"--factory-dir", "/tmp/factory", "--selected", "--auth-ref", strings.Repeat("a", 40)}); err == nil {
		t.Fatal("backend source escaped into selected mode")
	}
}
func backendTestArgs(factory, input, harness, evidence string) []string {
	args := []string{"--factory-dir", factory, "--candidate", "--backend-only", "--backend-inputs", input, "--backend-inputs-sha256", strings.Repeat("a", 64), "--backend-harness", harness, "--backend-harness-sha256", strings.Repeat("b", 64), "--backend-evidence-dir", evidence}
	for _, service := range []string{"auth", "gateway", "warehouse", "serving", "feedback"} {
		args = append(args, "--"+service+"-ref", strings.Repeat("a", 40))
	}
	return args
}

func TestBackendReviewedDirtySnapshotAcceptsTrackedDeletions(t *testing.T) {
	for _, staged := range []bool{false, true} {
		t.Run(fmt.Sprintf("staged=%t", staged), func(t *testing.T) {
			fixture := newBackendFixture(t)
			service := fixture.service("auth")
			dir := backendValue[string](t, service["path"])
			obsolete := filepath.Join(dir, "obsolete.go")
			if err := os.WriteFile(obsolete, []byte("package obsolete\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			backendGit(t, dir, "add", "obsolete.go")
			backendGit(t, dir, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-qm", "test: obsolete source")
			ref := strings.TrimSpace(backendGit(t, dir, "rev-parse", "HEAD"))
			service["ref"] = ref
			fixture.refs["auth"] = ref
			if err := os.Remove(obsolete); err != nil {
				t.Fatal(err)
			}
			if staged {
				backendGit(t, dir, "add", "-u")
			}
			service["sourceMode"] = "reviewed-dirty"
			fixture.refresh(t)
			fixture.args = append(fixture.args, "--reviewed-dirty")
			options, err := parseOptions("http-acceptance", fixture.args)
			if err != nil {
				t.Fatal(err)
			}
			input, err := readBackendInputs(options)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(t.TempDir(), "snapshot")
			if _, err := snapshotBackendSource(context.Background(), "auth", input.Services["auth"], destination, &osExecutor{}); err != nil {
				t.Fatalf("reviewed working-tree deletion rejected: %v", err)
			}
			entries, err := os.ReadDir(destination)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 || entries[0].Name() != "Dockerfile" || entries[1].Name() != "go.mod" {
				t.Fatalf("snapshot contains unreviewed files: %v", entries)
			}
			if err := os.WriteFile(filepath.Join(dir, "undeclared.go"), []byte("package undeclared\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := snapshotBackendSource(context.Background(), "auth", input.Services["auth"], filepath.Join(t.TempDir(), "snapshot"), &osExecutor{}); err == nil {
				t.Fatal("deletion allowed an undeclared added file")
			}
		})
	}
}

type backendFixture struct {
	t                                 *testing.T
	factory, input, harness, evidence string
	document                          map[string]any
	args                              []string
	refs                              map[string]string
}

func newBackendFixture(t *testing.T) *backendFixture {
	t.Helper()
	root := t.TempDir()
	f := &backendFixture{t: t, factory: filepath.Join(root, "factory"), input: filepath.Join(root, "input.json"), harness: filepath.Join(root, "harness"), evidence: filepath.Join(root, "evidence"), refs: map[string]string{}}
	if err := os.MkdirAll(filepath.Join(f.factory, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.harness, []byte("synthetic orchestration harness"), 0o700); err != nil {
		t.Fatal(err)
	}
	backendGit(t, f.factory, "init", "-q")
	if err := os.WriteFile(filepath.Join(f.factory, ".fixture"), []byte("factory test source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	backendGit(t, f.factory, "add", ".")
	backendGit(t, f.factory, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-qm", "test: factory source fixture")
	services := map[string]any{}
	for _, name := range []string{"auth", "gateway", "warehouse", "serving", "feedback"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		files := []map[string]any{}
		for _, file := range []struct{ path, data string }{{"Dockerfile", "FROM scratch\n"}, {"go.mod", "module github.com/JustIkra/umir-" + name + "\n\ngo 1.26.9\n"}} {
			if err := os.WriteFile(filepath.Join(dir, file.path), []byte(file.data), 0o644); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(file.data))
			files = append(files, map[string]any{"path": file.path, "sha256": hex.EncodeToString(sum[:]), "mode": 420})
		}
		backendGit(t, dir, "init", "-q")
		backendGit(t, dir, "remote", "add", "origin", "https://github.com/JustIkra/umir-"+name+".git")
		backendGit(t, dir, "add", ".")
		backendGit(t, dir, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-qm", "test: source fixture")
		ref := strings.TrimSpace(backendGit(t, dir, "rev-parse", "HEAD"))
		f.refs[name] = ref
		config := map[string]any{"path": dir, "ref": ref, "sourceMode": "clean", "files": files, "dockerfile": "Dockerfile", "port": 8080, "healthcheck": []string{"CMD", "/service", "health"}, "environment": map[string]string{}, "command": []string{}}
		if name == "auth" || name == "feedback" {
			config["statePath"] = "/var/lib/umir"
		}
		if name == "gateway" {
			config["publicPort"] = 8080
			config["environment"] = map[string]string{"ORIGIN": "{{origin}}", "AUTH_URL": "{{url:auth}}", "AUTH_KEY": "{{secret:gateway-auth}}"}
		}
		services[name] = config
	}
	f.document = map[string]any{"version": 1, "contractIdentity": "sha256:733e102ff0b4f52bb49d435716f9f1720642c8fad3a8a5d0685847071950e9e6", "services": services, "sinks": map[string]any{"smtp": map[string]any{"image": "example.invalid/smtp@sha256:" + strings.Repeat("d", 64), "port": 1025, "controlPort": 8025}, "notification": map[string]any{"image": "example.invalid/notification@sha256:" + strings.Repeat("e", 64), "port": 8080, "controlPort": 8080}}, "requirements": []string{"F2-orchestration-unit"}}
	f.refresh(t)
	return f
}
func backendGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, out, err)
	}
	return string(out)
}
func (f *backendFixture) refresh(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.document)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	hs, err := os.ReadFile(f.harness)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(hs)
	f.args = backendTestArgs(f.factory, f.input, f.harness, f.evidence)
	for _, p := range []struct{ flag, value string }{{"--backend-inputs-sha256", hex.EncodeToString(sum[:])}, {"--backend-harness-sha256", hex.EncodeToString(h[:])}} {
		f.args[slices.Index(f.args, p.flag)+1] = p.value
	}
	for name, ref := range f.refs {
		f.args[slices.Index(f.args, "--"+name+"-ref")+1] = ref
	}
}
func (f *backendFixture) service(name string) map[string]any {
	services, ok := f.document["services"].(map[string]any)
	if !ok {
		f.t.Fatal("invalid test services")
	}
	service, ok := services[name].(map[string]any)
	if !ok {
		f.t.Fatal("invalid test service")
	}
	return service
}
func (f *backendFixture) run(t *testing.T, ctx context.Context, e *backendExecutor) (int, map[string]any) {
	t.Helper()
	e.t = t
	e.fixture = f
	var out, errOut bytes.Buffer
	code := runWithExecutor(ctx, append([]string{"http-acceptance"}, f.args...), &out, &errOut, e)
	t.Logf("runner exit %d: %s", code, errOut.String())
	entries, err := os.ReadDir(f.evidence)
	if err != nil {
		t.Fatalf("evidence must precede failures: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want one owned run directory, got %d", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(f.evidence, entries[0].Name(), "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence map[string]any
	if err = json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if root := os.Getenv("UMIR_BACKEND_TEST_EVIDENCE"); root != "" {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		file, err := os.CreateTemp(root, strings.ReplaceAll(t.Name(), "/", "_")+"-*.json")
		if err != nil {
			t.Fatal(err)
		}
		writeErr := writeAll(file, data)
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			t.Fatal(err)
		}
	}
	return code, evidence
}

func TestBackendRejectsUnreviewedSourceBeforeBuildAndWritesEvidence(t *testing.T) {
	for _, kind := range []string{"dirty", "hash", "symlink", "extra", "wrong-ref", "input-hash", "harness-hash", "external-url", "real-email", "foreign-state", "extra-service", "secret-literal"} {
		t.Run(kind, func(t *testing.T) {
			f := newBackendFixture(t)
			s := f.service("auth")
			dir, ok := s["path"].(string)
			if !ok {
				t.Fatal("invalid test source path")
			}
			switch kind {
			case "dirty":
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("changed"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "hash":
				files, ok := s["files"].([]map[string]any)
				if !ok {
					t.Fatal("invalid test manifest")
				}
				files[0]["sha256"] = strings.Repeat("f", 64)
			case "symlink":
				if err := os.Remove(filepath.Join(dir, "Dockerfile")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.harness, filepath.Join(dir, "Dockerfile")); err != nil {
					t.Fatal(err)
				}
			case "extra":
				if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("unreviewed"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "wrong-ref":
				s["ref"] = strings.Repeat("f", 40)
			case "external-url":
				s["environment"] = map[string]string{"SMTP_URL": "smtp://production.example:25"}
			case "real-email":
				s["environment"] = map[string]string{"MAIL_TO": "real@example.com"}
			case "foreign-state":
				s["statePath"] = "/etc/production/auth.json"
			case "extra-service":
				backendValue[map[string]any](t, f.document["services"])["frontend"] = s
			case "secret-literal":
				s["environment"] = map[string]string{"SERVICE_KEY": "real-secret"}
			}
			f.refresh(t)
			if kind == "input-hash" || kind == "harness-hash" {
				flag := "--backend-inputs-sha256"
				if kind == "harness-hash" {
					flag = "--backend-harness-sha256"
				}
				f.args[slices.Index(f.args, flag)+1] = strings.Repeat("f", 64)
			}
			e := &backendExecutor{}
			code, ev := f.run(t, context.Background(), e)
			if code == 0 || e.builds != 0 || e.starts != 0 {
				t.Fatalf("unreviewed %s reached build/start: code=%d builds=%d starts=%d", kind, code, e.builds, e.starts)
			}
			if ev["stage"] == "passed" {
				t.Fatal("failure evidence claimed pass")
			}
		})
	}
}
func TestBackendReviewedDirtyCopiesExactContextWithoutPublishedSHAClaim(t *testing.T) {
	f := newBackendFixture(t)
	s := f.service("auth")
	data := []byte("module github.com/JustIkra/umir-auth\n\ngo 1.26.9\n\ntoolchain go1.26.9\n")
	if err := os.WriteFile(filepath.Join(backendValue[string](t, s["path"]), "go.mod"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	backendValue[[]map[string]any](t, s["files"])[1]["sha256"] = hex.EncodeToString(sum[:])
	s["sourceMode"] = "reviewed-dirty"
	f.refresh(t)
	e := &backendExecutor{}
	if code, _ := f.run(t, context.Background(), e); code == 0 || e.builds != 0 {
		t.Fatal("dirty source accepted without reviewed-dirty flag")
	}
	f.evidence = t.TempDir()
	f.refresh(t)
	f.args = append(f.args, "--reviewed-dirty")
	e = &backendExecutor{}
	code, ev := f.run(t, context.Background(), e)
	if code != 0 {
		t.Fatalf("reviewed dirty source rejected: %v", ev)
	}
	if ev["published"] != false {
		t.Fatal("dirty build claimed published SHA")
	}
	if backendValue[map[string]any](t, backendValue[map[string]any](t, ev["sources"])["auth"])["sourceMode"] != "reviewed-dirty" {
		t.Fatal("source mode lost")
	}
}
func TestBackendCleanupAndEvidenceOnSuccessFailureAndCancellation(t *testing.T) {
	for _, failure := range []string{"", "build", "up", "harness", "cancel", "cleanup", "leftover", "foreign-container", "incomplete-result"} {
		t.Run(failure, func(t *testing.T) {
			f := newBackendFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e := &backendExecutor{failure: failure, cancel: cancel}
			code, ev := f.run(t, ctx, e)
			if failure == "" && code != 0 {
				t.Fatalf("success exited %d: %v", code, ev)
			}
			if failure != "" && code == 0 {
				t.Fatalf("%s incorrectly passed", failure)
			}
			if e.starts > 0 && !e.down && failure != "foreign-container" {
				t.Fatal("partial/successful start was not cleaned")
			}
			if failure == "build" && e.down {
				t.Fatal("cleanup targeted Compose before any start")
			}
			if e.foreignCleanup {
				t.Fatal("cleanup escaped recorded project")
			}
			if e.starts > 0 && failure != "cleanup" && failure != "leftover" && failure != "foreign-container" && ev["cleanup"] != "removed" {
				t.Fatalf("cleanup not verified: %v", ev)
			}
			if e.starts > 0 {
				assertBackendTopology(t, e.topology)
			}
			if ev["published"] != false {
				t.Fatal("published identity incorrectly claimed")
			}
		})
	}
}
func assertBackendTopology(t *testing.T, topology map[string]any) {
	t.Helper()
	services := backendValue[map[string]any](t, topology["services"])
	if len(services) != 7 {
		t.Fatalf("want five services and two sinks, got %d", len(services))
	}
	if _, ok := services["frontend"]; ok {
		t.Fatal("frontend present")
	}
	for _, name := range []string{"auth", "warehouse", "serving", "feedback"} {
		s := backendValue[map[string]any](t, services[name])
		if ports, ok := s["ports"].([]any); ok && len(ports) > 0 {
			t.Fatalf("private %s published", name)
		}
	}
	for _, name := range []string{"gateway", "smtp", "notification"} {
		ports := backendValue[[]any](t, backendValue[map[string]any](t, services[name])["ports"])
		if len(ports) != 1 {
			t.Fatal("expected one endpoint")
		}
		p := backendValue[map[string]any](t, ports[0])
		if p["host_ip"] != "127.0.0.1" || name != "gateway" && p["published"] != "0" {
			t.Fatalf("non-ephemeral endpoint: %v", p)
		}
	}
	a := backendValue[map[string]any](t, backendValue[[]any](t, backendValue[map[string]any](t, services["auth"])["volumes"])[0])
	f := backendValue[map[string]any](t, backendValue[[]any](t, backendValue[map[string]any](t, services["feedback"])["volumes"])[0])
	if a["type"] != "volume" || f["type"] != "volume" || a["source"] == f["source"] {
		t.Fatal("state not separately isolated")
	}
	for _, name := range []string{"warehouse", "serving"} {
		if _, ok := backendValue[map[string]any](t, services[name])["volumes"]; ok {
			t.Fatal("business memory mounted durable")
		}
	}
	for _, network := range backendValue[map[string]any](t, topology["networks"]) {
		if backendValue[map[string]any](t, network)["internal"] != true {
			t.Fatal("delivery has external egress")
		}
	}
}

type backendExecutor struct {
	t                    *testing.T
	fixture              *backendFixture
	failure              string
	cancel               context.CancelFunc
	builds, starts       int
	down, foreignCleanup bool
	project              string
	topology             map[string]any
	images               map[string]map[string]string
}

func (e *backendExecutor) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
	if name == "git" {
		return (&osExecutor{}).Run(ctx, dir, env, name, args...)
	}
	if filepath.Base(name) == "harness" {
		if e.failure == "cancel" {
			e.cancel()
			return nil, nil, context.Canceled
		}
		if e.failure == "harness" || e.failure == "harness-and-cleanup" {
			return nil, nil, testExitError{code: 7}
		}
		data, err := os.ReadFile(valueAfter(args, "--backend-run"))
		if err != nil {
			return nil, nil, err
		}
		var descriptor map[string]any
		if err = json.Unmarshal(data, &descriptor); err != nil {
			return nil, nil, err
		}
		if descriptor["gatewayURL"] != descriptor["origin"] {
			e.t.Fatalf("harness endpoint wrong: %v", descriptor)
		}
		result := map[string]any{"status": "passed", "contractIdentity": e.fixture.document["contractIdentity"], "passed": []string{"F2-orchestration-unit"}}
		if e.failure == "incomplete-result" {
			result["passed"] = []string{}
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, os.WriteFile(valueAfter(args, "--backend-result"), encoded, 0o600)
	}
	if name != "docker" {
		return nil, nil, fmt.Errorf("unexpected command %s", name)
	}
	if args[0] == "build" {
		e.builds++
		if e.failure == "check-prebuild-evidence" {
			entries, err := os.ReadDir(e.fixture.evidence)
			if err != nil || len(entries) != 1 {
				return nil, nil, errors.New("missing prebuild evidence")
			}
			data, err := os.ReadFile(filepath.Join(e.fixture.evidence, entries[0].Name(), "evidence.json"))
			if err != nil {
				return nil, nil, err
			}
			var ev map[string]any
			if err = json.Unmarshal(data, &ev); err != nil {
				return nil, nil, err
			}
			refs, ok := ev["buildRefs"].(map[string]any)
			if !ok || refs[strings.TrimSuffix(filepath.Base(args[len(args)-1]), "-id")] != valueAfter(args, "--tag") || ev["factoryRef"] == nil || ev["factoryDirtySHA256"] == nil {
				return nil, nil, errors.New("source/build identity not recorded")
			}
		}
		if e.failure == "build" {
			return nil, nil, errors.New("build failed")
		}
		if e.images == nil {
			e.images = map[string]map[string]string{}
		}
		labels := map[string]string{}
		for i, a := range args {
			if a == "--label" {
				p := strings.SplitN(args[i+1], "=", 2)
				labels[p[0]] = p[1]
			}
		}
		e.images[valueAfter(args, "--tag")] = labels
		if e.failure == "build-lost-reply" {
			return nil, nil, testExitError{code: 9}
		}
		return nil, nil, nil
	}
	if args[0] == "image" && slices.Contains(args, "inspect") {
		labels := e.images[args[len(args)-1]]
		data, err := json.Marshal([]any{map[string]any{"Id": "sha256:" + strings.Repeat("f", 64), "RepoDigests": []string{}, "Config": map[string]any{"Labels": labels, "Healthcheck": map[string]any{"Test": []string{"CMD", "health"}}}}})
		return data, nil, err
	}
	if args[0] == "image" && args[1] == "ls" {
		if _, ok := e.images[args[len(args)-1]]; ok {
			return []byte("fixture-image-id\n"), nil, nil
		}
		return nil, nil, nil
	}
	if args[0] == "image" && args[1] == "rm" {
		if _, ok := e.images[args[len(args)-1]]; !ok {
			e.foreignCleanup = true
		}
		delete(e.images, args[len(args)-1])
		return nil, nil, nil
	}
	if args[0] == "inspect" {
		service := strings.TrimSuffix(args[len(args)-1], "-id")
		labels := map[string]string{"com.docker.compose.project": e.project, "com.docker.compose.service": service, "io.umir.backend.run": e.project}
		if e.failure == "foreign-container" {
			labels["com.docker.compose.project"] = "unrelated"
		}
		image := "sha256:" + strings.Repeat("f", 64)
		if e.failure == "image-drift" {
			image = "sha256:" + strings.Repeat("0", 64)
		}
		data, err := json.Marshal(map[string]any{"Id": args[len(args)-1], "Config": map[string]any{"Labels": labels}, "Image": image})
		return data, nil, err
	}
	if args[0] == "compose" {
		project := valueAfter(args, "--project-name")
		if !strings.HasPrefix(project, "umir-backend-") {
			e.foreignCleanup = true
		}
		if e.project == "" {
			e.project = project
		}
		if project != e.project {
			e.foreignCleanup = true
		}
		data, err := os.ReadFile(valueAfter(args, "--file"))
		if err != nil {
			return nil, nil, err
		}
		if err = json.Unmarshal(data, &e.topology); err != nil {
			return nil, nil, err
		}
		if slices.Contains(args, "config") {
			return nil, nil, nil
		}
		if slices.Contains(args, "up") {
			e.starts++
			if e.failure == "up" {
				return nil, nil, testExitError{code: 3}
			}
			return nil, nil, nil
		}
		if slices.Contains(args, "down") {
			e.down = true
			if ctx.Err() != nil {
				e.t.Fatal("cleanup used cancelled context")
			}
			if e.failure == "cleanup" || e.failure == "harness-and-cleanup" {
				return nil, nil, errors.New("cleanup failed")
			}
			return nil, nil, nil
		}
		if slices.Contains(args, "port") {
			service := args[len(args)-2]
			for _, p := range e.mappings(service) {
				if strconv.Itoa(p.TargetPort) == args[len(args)-1] {
					return []byte("127.0.0.1:" + strconv.Itoa(p.PublishedPort) + "\n"), nil, nil
				}
			}
			return nil, nil, errors.New("requested listener is not published")
		}
		if slices.Contains(args, "ps") {
			if e.failure == "malformed-inventory" {
				return []byte("}"), nil, nil
			}
			list := []any{}
			for _, service := range []string{"auth", "gateway", "warehouse", "serving", "feedback", "smtp", "notification"} {
				list = append(list, map[string]any{"ID": service + "-id", "Service": service, "State": "running", "Health": "healthy", "Publishers": e.mappings(service)})
			}
			out, err := json.Marshal(list)
			return out, nil, err
		}
	}
	if slices.Contains(args, "ls") || args[0] == "ps" {
		for _, arg := range args {
			if strings.HasPrefix(arg, "label=com.docker.compose.project=") && strings.TrimPrefix(arg, "label=com.docker.compose.project=") != e.project {
				e.foreignCleanup = true
			}
		}
		if e.failure == "leftover" {
			return []byte("leftover-resource\n"), nil, nil
		}
		return nil, nil, nil
	}
	return nil, nil, fmt.Errorf("unexpected docker arguments: %v", args)
}
func (e *backendExecutor) mappings(service string) []backendPublisher {
	names := append(slices.Clone(backendServices), "smtp", "notification")
	s := backendValue[map[string]any](e.t, backendValue[map[string]any](e.t, e.topology["services"])[service])
	rawPorts, present := s["ports"]
	if !present {
		return nil
	}
	ports := backendValue[[]any](e.t, rawPorts)
	result := []backendPublisher{}
	for i, raw := range ports {
		p := backendValue[map[string]any](e.t, raw)
		published, err := strconv.Atoi(backendValue[string](e.t, p["published"]))
		if err != nil {
			e.t.Fatal(err)
		}
		if published == 0 {
			published = 49200 + slices.Index(names, service)*10 + i
		}
		result = append(result, backendPublisher{URL: backendValue[string](e.t, p["host_ip"]), TargetPort: int(backendValue[float64](e.t, p["target"])), PublishedPort: published, Protocol: backendValue[string](e.t, p["protocol"])})
	}
	return result
}
func TestBackendRejectsUnknownOriginAndRecordsPartialStartIdentity(t *testing.T) {
	t.Run("unknown-origin", func(t *testing.T) {
		f := newBackendFixture(t)
		backendGit(t, backendValue[string](t, f.service("auth")["path"]), "remote", "set-url", "origin", "https://example.invalid/not-auth.git")
		e := &backendExecutor{}
		code, _ := f.run(t, context.Background(), e)
		if code == 0 || e.builds != 0 {
			t.Fatal("unrelated source was labelled as Auth")
		}
	})
	t.Run("partial-start", func(t *testing.T) {
		f := newBackendFixture(t)
		e := &backendExecutor{failure: "up"}
		code, ev := f.run(t, context.Background(), e)
		if code != 3 {
			t.Fatalf("start status was lost: %d", code)
		}
		containers, ok := ev["containers"].([]any)
		if !ok || len(containers) == 0 {
			t.Fatal("partial-start container identities were omitted")
		}
	})
}
func TestBackendRejectsRunningImageDrift(t *testing.T) {
	f := newBackendFixture(t)
	e := &backendExecutor{failure: "image-drift"}
	code, _ := f.run(t, context.Background(), e)
	if code == 0 {
		t.Fatal("container with wrong image identity passed")
	}
}
func TestBackendDescriptorRejectsDuplicateFields(t *testing.T) {
	f := newBackendFixture(t)
	data, err := os.ReadFile(f.input)
	if err != nil {
		t.Fatal(err)
	}
	data = append([]byte(`{"version":999,`), data[1:]...)
	if err = os.WriteFile(f.input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	f.args[slices.Index(f.args, "--backend-inputs-sha256")+1] = hex.EncodeToString(sum[:])
	e := &backendExecutor{}
	code, _ := f.run(t, context.Background(), e)
	if code == 0 || e.builds != 0 {
		t.Fatal("duplicate descriptor fields reached build")
	}
}

func TestBackendUnknownBuildReplyCleansTheRecordedTag(t *testing.T) {
	f := newBackendFixture(t)
	e := &backendExecutor{failure: "build-lost-reply"}
	code, ev := f.run(t, context.Background(), e)
	if code == 0 {
		t.Fatal("unknown build reply claimed success")
	}
	if len(e.images) != 0 {
		t.Fatal("image created before lost build reply escaped cleanup")
	}
	if ev["imageCleanup"] != "removed" {
		t.Fatalf("image cleanup is unverified: %v", ev)
	}
}
func TestBackendCleanupPreservesHarnessFailure(t *testing.T) {
	f := newBackendFixture(t)
	e := &backendExecutor{failure: "harness-and-cleanup"}
	code, ev := f.run(t, context.Background(), e)
	if code != 7 || ev["cleanup"] != "failed" {
		t.Fatalf("cleanup overwrote harness status or concealed failure: %d %v", code, ev)
	}
}

func backendValue[T any](t *testing.T, value any) T {
	t.Helper()
	result, ok := value.(T)
	if !ok {
		t.Fatalf("unexpected test value type: %T", value)
	}
	return result
}

func TestBackendEvidenceBindsFactoryBeforeBuild(t *testing.T) {
	f := newBackendFixture(t)
	e := &backendExecutor{failure: "check-prebuild-evidence"}
	code, ev := f.run(t, context.Background(), e)
	if code != 0 {
		t.Fatalf("source/build evidence was not durable before build: %d %v", code, ev)
	}
}

func TestBackendMissingFactoryStillRecordsFailureEvidence(t *testing.T) {
	f := newBackendFixture(t)
	f.args[slices.Index(f.args, "--factory-dir")+1] = filepath.Join(t.TempDir(), "missing-factory")
	code, ev := f.run(t, context.Background(), &backendExecutor{})
	if code == 0 || ev["stage"] != "factory-identity" {
		t.Fatalf("missing factory has no typed evidence: %d %v", code, ev)
	}
}

func TestBackendDoesNotCleanUnverifiableContainerInventory(t *testing.T) {
	f := newBackendFixture(t)
	e := &backendExecutor{failure: "malformed-inventory"}
	code, ev := f.run(t, context.Background(), e)
	if code == 0 || e.down || ev["cleanup"] != "failed" {
		t.Fatalf("unverifiable inventory allowed cleanup: %d down=%v %v", code, e.down, ev)
	}
}

func newBackendV2Fixture(t *testing.T) *backendFixture {
	t.Helper()
	f := newBackendFixture(t)
	f.document["version"] = 2
	f.document["fixtures"] = map[string]any{
		"protocol":       "umir-operational-fixtures/1.1.0",
		"bindingsSHA256": "af215b378140b8e690523fc537007679b8c2599ed3039f7f2a0c651a64c59410",
		"fixtureID":      "22222222-2222-4222-8222-222222222222",
		"initialInstant": "2026-10-13T09:00:00+03:00",
		"bootstrap": map[string]any{
			"user":         map[string]any{"userID": "11111111-1111-4111-8111-111111111111", "login": "e2e.initial", "displayName": "E2E Initial Operator", "credentialVersion": "1"},
			"passwordFile": map[string]any{"target": "auth", "path": "/run/umir-fixtures/secrets/bootstrap-password"},
			"stateFile":    "/var/lib/umir-auth/auth.json", "mode": "initialize-if-absent-validate-if-present",
		},
		"owners":     map[string]string{"auth": "auth-real-v2", "feedback": "feedback-real-v2", "warehouse": "warehouse-real-v2", "serving": "serving-real-v2", "gateway": "gateway-real-v2"},
		"sinkSource": nil,
	}
	for _, name := range backendServices {
		s := f.service(name)
		dir := backendValue[string](t, s["path"])
		dockerfile := filepath.Join(dir, "tests", "fixtures", "Dockerfile")
		if err := os.MkdirAll(filepath.Dir(dockerfile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(dir, "Dockerfile"), dockerfile); err != nil {
			t.Fatal(err)
		}
		backendGit(t, dir, "add", "-A")
		backendGit(t, dir, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-qm", "test: fixture wrapper context")
		ref := strings.TrimSpace(backendGit(t, dir, "rev-parse", "HEAD"))
		f.refs[name], s["ref"] = ref, ref
		files := backendValue[[]map[string]any](t, s["files"])
		files[0]["path"] = "tests/fixtures/Dockerfile"
		s["dockerfile"] = "tests/fixtures/Dockerfile"
		s["environment"] = map[string]string{}
		s["command"] = []string{"serve"}
		s["healthcheck"] = []string{"CMD", "/fixture-server", "healthcheck"}
		s["port"] = 8080
		if name == "gateway" {
			s["port"] = 8082
		}
		if name == "auth" || name == "feedback" {
			s["statePath"] = "/var/lib/umir-" + name
		}
	}
	sinks := backendValue[map[string]any](t, f.document["sinks"])
	backendValue[map[string]any](t, sinks["smtp"])["port"] = 2525
	backendValue[map[string]any](t, sinks["notification"])["port"] = 3128
	backendValue[map[string]any](t, sinks["notification"])["controlPort"] = 8026
	f.refresh(t)
	return f
}

func TestBackendBootstrapPasswordFitsFrozenPolicy(t *testing.T) {
	first, err := backendSecrets()
	if err != nil {
		t.Fatal(err)
	}
	second, err := backendSecrets()
	if err != nil {
		t.Fatal(err)
	}
	for _, secrets := range []map[string]string{first, second} {
		password := secrets["bootstrap-password"]
		if len(password) != 32 || !isSHA256(password+password) {
			t.Fatal("bootstrap password must be exactly 32 lowercase ASCII hex characters")
		}
		for _, name := range backendSecretNames {
			if name != "bootstrap-password" && !isSHA256(secrets[name]) {
				t.Fatal("service secret policy changed")
			}
		}
	}
	if first["bootstrap-password"] == second["bootstrap-password"] {
		t.Fatal("bootstrap password reused")
	}
}

func TestBackendVersion2ReviewedDescriptor(t *testing.T) {
	f := newBackendV2Fixture(t)
	o, err := parseOptions("http-acceptance", f.args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readBackendInputs(o); err != nil {
		t.Fatalf("reviewed version2 descriptor rejected: %v", err)
	}
}

func TestBackendVersion2RejectsInvalidBindingsBeforeBuild(t *testing.T) {
	for _, kind := range []string{"old-protocol", "wrong-bindings", "fixture-id", "instant", "bootstrap-target", "bootstrap-path", "bootstrap-profile", "missing-owner", "missing-sink-source", "inline-secret", "wrong-command", "wrong-port", "wrong-sink-port", "wrong-dockerfile", "legacy-fixtures"} {
		t.Run(kind, func(t *testing.T) {
			f := newBackendV2Fixture(t)
			fixtures := backendValue[map[string]any](t, f.document["fixtures"])
			bootstrap := backendValue[map[string]any](t, fixtures["bootstrap"])
			switch kind {
			case "old-protocol":
				fixtures["protocol"] = "umir-operational-fixtures/1.0.0"
			case "wrong-bindings":
				fixtures["bindingsSHA256"] = strings.Repeat("f", 64)
			case "fixture-id":
				fixtures["fixtureID"] = strings.Repeat("2", 36)
			case "instant":
				fixtures["initialInstant"] = "2026-10-13T09:00:00"
			case "bootstrap-target":
				backendValue[map[string]any](t, bootstrap["passwordFile"])["target"] = "gateway"
			case "bootstrap-path":
				backendValue[map[string]any](t, bootstrap["passwordFile"])["path"] = "/run/umir-fixtures/secrets/../private/password"
			case "bootstrap-profile":
				backendValue[map[string]any](t, bootstrap["user"])["login"] = "different"
			case "missing-owner":
				delete(backendValue[map[string]string](t, fixtures["owners"]), "auth")
			case "missing-sink-source":
				delete(fixtures, "sinkSource")
			case "inline-secret":
				f.service("auth")["environment"] = map[string]string{"AUTH_KEY": "{{secret:gateway-auth}}"}
			case "wrong-command":
				f.service("auth")["command"] = []string{"arbitrary"}
			case "wrong-port":
				f.service("gateway")["port"] = 8080
			case "wrong-sink-port":
				backendValue[map[string]any](t, backendValue[map[string]any](t, f.document["sinks"])["smtp"])["port"] = 25
			case "wrong-dockerfile":
				f.service("auth")["dockerfile"] = "Dockerfile"
			case "legacy-fixtures":
				f.document["version"] = 1
			}
			f.refresh(t)
			e := &backendExecutor{}
			code, _ := f.run(t, context.Background(), e)
			if code == 0 || e.builds != 0 || e.starts != 0 {
				t.Fatal("invalid version2 input reached build/start")
			}
		})
	}
}

type backendV2Executor struct {
	backendExecutor
	failureV2      string
	readyCalls     int
	harnessCalls   int
	buildArguments [][]string
}

func (e *backendV2Executor) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
	if name == "docker" && args[0] == "build" {
		e.buildArguments = append(e.buildArguments, slices.Clone(args))
		if e.failureV2 == "sink-build-lost" && valueAfter(args, "--build-arg") == "SINK=smtp" {
			_, _, err := e.backendExecutor.Run(ctx, dir, env, name, args...)
			if err != nil {
				return nil, nil, err
			}
			return nil, nil, testExitError{code: 9}
		}
	}
	if name == "docker" && args[0] == "compose" && slices.Contains(args, "ps") {
		out, stderr, err := e.backendExecutor.Run(ctx, dir, env, name, args...)
		if err != nil {
			return out, stderr, err
		}
		var containers []backendContainer
		if err := json.Unmarshal(out, &containers); err != nil {
			return nil, nil, err
		}
		for i := range containers {
			if containers[i].Service == "notification" {
				containers[i].Publishers[0].TargetPort = 8026
			}
		}
		encoded, err := json.Marshal(containers)
		return encoded, stderr, err
	}
	if filepath.Base(name) == "harness" {
		e.harnessCalls++
		data, err := os.ReadFile(valueAfter(args, "--backend-run"))
		if err != nil {
			return nil, nil, err
		}
		var descriptor map[string]any
		if err := json.Unmarshal(data, &descriptor); err != nil {
			return nil, nil, err
		}
		if descriptor["version"] != float64(2) {
			e.t.Error("version2 input emitted a legacy descriptor")
		}
		if _, present := descriptor["secrets"]; present {
			e.t.Error("version2 descriptor contains inline secrets")
		}
		fixtures, ok := descriptor["fixtures"].(map[string]any)
		if !ok {
			e.t.Error("version2 fixture metadata absent")
			return nil, nil, errors.New("missing fixture metadata")
		}
		if fixtures["protocol"] != "umir-operational-fixtures/1.1.0" || fixtures["bindingsSHA256"] != correctedBindingsDigest {
			e.t.Error("version2 descriptor reviewed binding or protocol differs")
		}
		root := backendValue[string](e.t, fixtures["harnessPrivateRoot"])
		credentials := backendValue[map[string]any](e.t, descriptor["credentialFiles"])
		for key, value := range credentials {
			ref := backendValue[map[string]any](e.t, value)
			if ref["target"] != "harness" {
				e.t.Error("harness credential target mismatch")
			}
			path := backendValue[string](e.t, ref["path"])
			secretPath := filepath.Join(root, strings.TrimPrefix(path, "/run/umir-fixtures/"))
			info, err := os.Lstat(secretPath)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				e.t.Error("harness file not private")
			}
			value, err := os.ReadFile(secretPath)
			if err != nil {
				return nil, nil, err
			}
			if bytes.Contains(data, value) {
				e.t.Errorf("credential %s embedded in descriptor", key)
			}
			if key == "bootstrap-password" && len(value) != 32 {
				e.t.Error("invalid bootstrap credential length")
			}
			if key == "telegram-bot" && (len(value) != 34 || !bytes.HasPrefix(value, []byte("1:"))) {
				e.t.Error("invalid synthetic Telegram token")
			}
		}
		if e.readyCalls != 5 {
			e.t.Error("harness ran without five owner capability probes")
		}
	}
	return e.backendExecutor.Run(ctx, dir, env, name, args...)
}

func backendV2SinkSource(t *testing.T, f *backendFixture) {
	t.Helper()
	backendGit(t, f.factory, "remote", "add", "origin", "https://github.com/JustIkra/umir-factory.git")
	for file, content := range map[string]string{"tests/http/go.mod": "module github.com/JustIkra/umir-factory/tests/http\n\ngo 1.26.9\n", "tests/http/fixtures/sinks/Dockerfile": "FROM scratch\n"} {
		path := filepath.Join(f.factory, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	backendGit(t, f.factory, "add", "-A")
	backendGit(t, f.factory, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-qm", "test: synthetic sink source")
	var files []map[string]any
	for _, file := range []string{".fixture", "tests/http/go.mod", "tests/http/fixtures/sinks/Dockerfile"} {
		data, err := os.ReadFile(filepath.Join(f.factory, file))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, map[string]any{"path": file, "sha256": backendDigest(data), "mode": 420})
	}
	backendValue[map[string]any](t, f.document["fixtures"])["sinkSource"] = map[string]any{"path": f.factory, "ref": strings.TrimSpace(backendGit(t, f.factory, "rev-parse", "HEAD")), "sourceMode": "clean", "files": files, "dockerfile": "tests/http/fixtures/sinks/Dockerfile"}
	for _, name := range []string{"smtp", "notification"} {
		delete(backendValue[map[string]any](t, backendValue[map[string]any](t, f.document["sinks"])[name]), "image")
	}
	f.refresh(t)
}

func TestBackendVersion2LocalSinkBuildAndCleanup(t *testing.T) {
	for _, failure := range []string{"", "sink-build-lost", "sink-origin", "sink-manifest", "mixed-image", "mixed-empty-image"} {
		t.Run(failure, func(t *testing.T) {
			f := newBackendV2Fixture(t)
			backendV2SinkSource(t, f)
			switch failure {
			case "sink-origin":
				backendGit(t, f.factory, "remote", "set-url", "origin", "https://example.invalid/unknown.git")
			case "sink-manifest":
				source := backendValue[map[string]any](t, backendValue[map[string]any](t, f.document["fixtures"])["sinkSource"])
				source["files"] = backendValue[[]map[string]any](t, source["files"])[1:]
			case "mixed-image":
				backendValue[map[string]any](t, backendValue[map[string]any](t, f.document["sinks"])["smtp"])["image"] = "example.invalid/smtp@sha256:" + strings.Repeat("d", 64)
			case "mixed-empty-image":
				backendValue[map[string]any](t, backendValue[map[string]any](t, f.document["sinks"])["smtp"])["image"] = ""
			}
			f.refresh(t)
			e := &backendV2Executor{backendExecutor: backendExecutor{t: t, fixture: f}, failureV2: failure}
			var stdout, stderr bytes.Buffer
			code := runWithExecutor(context.Background(), append([]string{"http-acceptance"}, f.args...), &stdout, &stderr, e)
			if failure == "" && code != 0 {
				t.Fatalf("reviewed local sink source failed: %s", stderr.String())
			}
			if failure != "" && code == 0 {
				t.Fatal("invalid sink source/build succeeded")
			}
			if len(e.images) != 0 || e.foreignCleanup {
				t.Fatal("owned sink image cleanup failed")
			}
			if failure == "" {
				if e.builds != 7 {
					t.Fatal("local source did not build five owners and two sinks")
				}
				var roles []string
				for _, args := range e.buildArguments {
					if value := valueAfter(args, "--build-arg"); value != "" {
						roles = append(roles, value)
					}
				}
				if !slices.Equal(roles, []string{"SINK=smtp", "SINK=notification"}) {
					t.Fatal("sink build selectors differ from reviewed binding")
				}
			}
		})
	}
}

func TestBackendVersion2RejectsInvalidGeneratedCredentialMaterial(t *testing.T) {
	for _, name := range backendSecretNames {
		t.Run(name, func(t *testing.T) {
			secrets, err := backendSecrets()
			if err != nil {
				t.Fatal(err)
			}
			secrets[name] = "invalid"
			if _, err := backendSyntheticCredentials(secrets); err == nil {
				t.Fatal("invalid secret material accepted")
			}
		})
	}
	secrets, err := backendSecrets()
	if err != nil {
		t.Fatal(err)
	}
	secrets["foreign-credential"] = "unreviewed"
	if _, err := backendSyntheticCredentials(secrets); err == nil {
		t.Fatal("unreviewed credential accepted")
	}
}

func TestBackendVersion2RejectsSubNanosecondInstants(t *testing.T) {
	f := newBackendV2Fixture(t)
	backendValue[map[string]any](t, f.document["fixtures"])["initialInstant"] = "2026-10-13T09:00:00.1234567891+03:00"
	f.refresh(t)
	o, err := parseOptions("http-acceptance", f.args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readBackendInputs(o); err == nil {
		t.Fatal("instant precision silently truncated")
	}
}

func TestBackendVersion2CredentialDistributionAndVerifiedTLS(t *testing.T) {
	f := newBackendV2Fixture(t)
	o, err := parseOptions("http-acceptance", f.args)
	if err != nil {
		t.Fatal(err)
	}
	input, err := readBackendInputs(o)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := backendSecrets()
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	run, harness, err := prepareBackendFixtures(temp, "umir-backend-0123456789abcdef01234567", input, secrets, "http://127.0.0.1:49152")
	if err != nil {
		t.Fatal(err)
	}
	if run.Protocol != "umir-operational-fixtures/1.1.0" || len(run.Owners) != 5 || len(harness) != len(reviewedBackendCredentialRefs(t, "harness")) {
		t.Fatal("fixture descriptor metadata differs")
	}
	for _, name := range append(slices.Clone(backendServices), "smtp", "notification") {
		dir := filepath.Join(temp, "fixtures", name, "secrets")
		if err := validateBackendPrivateDirectory(dir); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !slices.Contains(backendCredentialTargets[entry.Name()], name) {
				t.Fatal("credential shared outside named endpoints")
			}
		}
	}
	caPEM, err := os.ReadFile(filepath.Join(temp, "fixtures", "smtp", "secrets", "synthetic-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("invalid run CA")
	}
	for name, host := range map[string]string{"smtp": "smtp", "notification": "api.telegram.org"} {
		data, err := os.ReadFile(filepath.Join(temp, "fixtures", name, "secrets", name+"-tls-cert"))
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(data)
		if block == nil {
			t.Fatal("missing server certificate")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		instant, err := time.Parse(time.RFC3339Nano, "2026-10-13T09:00:00+03:00")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: instant}); err != nil {
			t.Fatal("synthetic TLS trust or SAN invalid")
		}
		for _, boundary := range []string{"2000-01-01T00:00:00+14:00", "2099-12-31T23:59:59.999999999-12:00"} {
			at, err := time.Parse(time.RFC3339Nano, boundary)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: at}); err != nil {
				t.Fatal("synthetic certificate does not cover allowed clock range")
			}
		}
	}
	if validator := os.Getenv("UMIR_BACKEND_CONFIG_VALIDATOR"); validator != "" {
		command := exec.Command("node", validator, temp)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("approved schema validation failed: %s", output)
		}
	}
}

func TestBackendVersion1KeepsLegacyManifestParserBounds(t *testing.T) {
	f := newBackendFixture(t)
	files := backendValue[[]map[string]any](t, f.service("auth")["files"])
	for i := 0; i < 10000; i++ {
		files = append(files, map[string]any{"path": fmt.Sprintf("legacy/file-%d.go", i), "sha256": strings.Repeat("a", 64), "mode": 420})
	}
	f.service("auth")["files"] = files
	f.refresh(t)
	o, err := parseOptions("http-acceptance", f.args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readBackendInputs(o); err != nil {
		t.Fatal("version2 manifest count bound changed the legacy parser")
	}
	if !safeBackendPath(strings.Repeat("segment/", 150) + "file.go") {
		t.Fatal("version2 path length bound changed the legacy parser")
	}
}

func (e *backendV2Executor) RunPrivate(ctx context.Context, dir string, env []string, stdin []byte, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name != "docker" {
		return nil, errors.New("unexpected private command")
	}
	if args[0] == "inspect" || args[0] == "compose" && (slices.Contains(args, "port") || slices.Contains(args, "ps")) {
		out, _, err := e.Run(ctx, dir, env, name, args...)
		return out, err
	}
	if args[0] == "cp" {
		parts := strings.SplitN(args[1], ":", 2)
		service := strings.TrimSuffix(parts[0], "-id")
		var contents map[string][]byte
		if parts[1] == "/fixture-server" || parts[1] == "/fixture-control" {
			contents = map[string][]byte{filepath.Base(parts[1]): []byte(filepath.Base(parts[1]))}
		} else {
			topologyService := backendValue[map[string]any](e.t, backendValue[map[string]any](e.t, e.topology["services"])[service])
			for _, raw := range backendValue[[]any](e.t, topologyService["volumes"]) {
				mount := backendValue[map[string]any](e.t, raw)
				if mount["target"] != parts[1] {
					continue
				}
				host := backendValue[string](e.t, mount["source"])
				entries, err := os.ReadDir(host)
				if err != nil {
					return nil, err
				}
				contents = map[string][]byte{}
				for _, entry := range entries {
					data, err := os.ReadFile(filepath.Join(host, entry.Name()))
					if err != nil {
						return nil, err
					}
					contents[entry.Name()] = data
				}
			}
		}
		var buffer bytes.Buffer
		writer := tar.NewWriter(&buffer)
		for key, data := range contents {
			mode, uid := int64(0o600), 65532
			if strings.HasPrefix(key, "fixture-") && (key == "fixture-server" || key == "fixture-control") {
				mode = 0o755
			}
			if e.failureV2 == "foreign-file-owner" {
				uid = 0
			}
			if err := writer.WriteHeader(&tar.Header{Name: key, Mode: mode, Uid: uid, Gid: 65532, Size: int64(len(data))}); err != nil {
				return nil, err
			}
			if _, err := writer.Write(data); err != nil {
				return nil, err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		return buffer.Bytes(), nil
	}
	e.readyCalls++
	var request struct {
		Version   int            `json:"version"`
		Project   string         `json:"project"`
		RequestID string         `json:"requestID"`
		Operation string         `json:"operation"`
		Data      map[string]any `json:"data"`
	}
	if err := decodeBackendJSON(stdin, &request); err != nil {
		return nil, err
	}
	if args[len(args)-2] != "/fixture-control" || args[len(args)-1] != "request" || request.Operation != "fixture.ready" {
		return nil, errors.New("invented control command")
	}
	owner := args[len(args)-3]
	service := backendValue[map[string]any](e.t, backendValue[map[string]any](e.t, e.topology["services"])[owner])
	var config []byte
	for _, raw := range backendValue[[]any](e.t, service["volumes"]) {
		mount := backendValue[map[string]any](e.t, raw)
		if mount["target"] == "/run/umir-fixtures/config" {
			var err error
			config, err = os.ReadFile(filepath.Join(backendValue[string](e.t, mount["source"]), "config.json"))
			if err != nil {
				return nil, err
			}
		}
	}
	capabilities := []string{"fixture.ready", "clock.read", "clock.set", "clock.checkpoint"}
	if owner == "auth" && e.failureV2 != "missing-discovery" {
		capabilities = append(capabilities, "owner.jobs.discover")
	}
	if e.failureV2 == "unknown-capability" {
		capabilities = append(capabilities, "unreviewed-placeholder")
	}
	if e.failureV2 == "wrong-target-capability" {
		capabilities = append(capabilities, "sink.otp.write")
	}
	if e.failureV2 == "duplicate-capability" {
		capabilities = append(capabilities, "clock.read")
	}
	configSHA := backendDigest(bytes.TrimSpace(config))
	if e.failureV2 == "config-mismatch" {
		configSHA = strings.Repeat("0", 64)
	}
	instance := fmt.Sprintf("33333333-3333-4333-8333-%012d", slices.Index(backendServices, owner)+1)
	if e.failureV2 == "reused-instance" {
		instance = "33333333-3333-4333-8333-333333333333"
	}
	data := map[string]any{"target": owner, "instanceID": instance, "protocol": "umir-operational-fixtures/1.1.0", "binarySHA256": backendDigest([]byte("fixture-server")), "configSHA256": configSHA, "ready": true, "capabilities": capabilities}
	if e.failureV2 == "old-protocol" {
		data["protocol"] = "umir-operational-fixtures/1.0.0"
	}
	if e.failureV2 == "oversize" {
		return bytes.Repeat([]byte("x"), 262145), nil
	}
	return json.Marshal(map[string]any{"version": 1, "project": request.Project, "requestID": request.RequestID, "operation": request.Operation, "data": data})
}

func TestBackendVersion2FilesTopologyAndCapabilities(t *testing.T) {
	for _, failure := range []string{"", "missing-discovery", "config-mismatch", "old-protocol", "oversize", "foreign-file-owner", "unknown-capability", "wrong-target-capability", "duplicate-capability", "reused-instance"} {
		t.Run(failure, func(t *testing.T) {
			f := newBackendV2Fixture(t)
			e := &backendV2Executor{backendExecutor: backendExecutor{t: t, fixture: f}, failureV2: failure}
			var stdout, stderr bytes.Buffer
			code := runWithExecutor(context.Background(), append([]string{"http-acceptance"}, f.args...), &stdout, &stderr, e)
			if failure == "" && code != 0 {
				t.Fatalf("version2 orchestration failed: %s", stderr.String())
			}
			if failure != "" && (code == 0 || e.harnessCalls != 0) {
				t.Fatal("invalid provider reached harness")
			}
			if !e.down || len(e.images) != 0 {
				t.Fatal("version2 resources escaped cleanup")
			}
			if failure == "" {
				services := backendValue[map[string]any](t, e.topology["services"])
				if len(services) != 7 {
					t.Fatal("wrong container count")
				}
				for _, name := range backendServices {
					service := backendValue[map[string]any](t, services[name])
					if service["user"] != "65532:65532" {
						t.Fatal("fixture runtime UID changed")
					}
					if _, ok := service["tmpfs"]; !ok {
						t.Fatal("writable private control mount absent")
					}
					for _, raw := range backendValue[[]any](t, service["volumes"]) {
						mount := backendValue[map[string]any](t, raw)
						if mount["type"] == "bind" && mount["read_only"] != true {
							t.Fatal("config/credential mount writable")
						}
						if (name == "serving" || name == "warehouse") && mount["type"] == "volume" {
							t.Fatal("business memory received durable volume")
						}
					}
				}
			}
		})
	}
}

func TestBackendVersion2RejectsInvalidRFC3339Syntax(t *testing.T) {
	for _, instant := range []string{
		"2026-10-13T09:00:00+24:00",
		"2026-10-13T09:00:00-24:00",
		"2026-10-13T09:00:00+01:60",
		"2026-10-13T09:00:00-01:60",
		"2026-10-13T09:00:00,1234567891+03:00",
		"2026-10-13T09:00:00,123Z",
		"2026-10-13T9:00:00Z",
		"2026-10-13T09:00:00.1234567891Z",
		"2026-02-30T09:00:00Z",
		"1999-12-31T23:59:59Z",
		"2100-01-01T00:00:00Z",
	} {
		t.Run(instant, func(t *testing.T) {
			f := newBackendV2Fixture(t)
			backendValue[map[string]any](t, f.document["fixtures"])["initialInstant"] = instant
			f.refresh(t)
			o, err := parseOptions("http-acceptance", f.args)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readBackendInputs(o); err == nil {
				t.Fatal("invalid timestamp accepted by version2 parser")
			}
		})
	}
}

func TestBackendVersion2KeepsValidRFC3339Instants(t *testing.T) {
	for _, instant := range []string{
		"2000-01-01T00:00:00+14:00",
		"2099-12-31T23:59:59.999999999-12:00",
		"2026-10-13T09:00:00Z",
		"2026-10-13T09:00:00.0Z",
		"2026-10-13T09:00:00.123456789+03:00",
		"2026-10-13T09:00:00-00:00",
		"2026-10-13T09:00:00+23:59",
		"2026-10-13T09:00:00-23:59",
	} {
		t.Run(instant, func(t *testing.T) {
			f := newBackendV2Fixture(t)
			backendValue[map[string]any](t, f.document["fixtures"])["initialInstant"] = instant
			f.refresh(t)
			o, err := parseOptions("http-acceptance", f.args)
			if err != nil {
				t.Fatal(err)
			}
			input, err := readBackendInputs(o)
			if err != nil {
				t.Fatal("valid timestamp rejected by version2 parser")
			}
			if input.Fixtures.InitialInstant != instant {
				t.Fatal("timestamp precision or offset changed")
			}
		})
	}
}

type backendArchivePathExecutor struct {
	backendV2Executor
	kind   string
	prefix string
	empty  bool
	noDir  bool
}

func (e *backendArchivePathExecutor) RunPrivate(ctx context.Context, dir string, env []string, stdin []byte, name string, args ...string) ([]byte, error) {
	data, err := e.backendV2Executor.RunPrivate(ctx, dir, env, stdin, name, args...)
	suffix := ":/run/umir-fixtures/" + e.kind
	if e.kind == "fixture-server" || e.kind == "fixture-control" {
		suffix = ":/" + e.kind
	}
	if err != nil || args[0] != "cp" || !strings.HasSuffix(args[1], suffix) {
		return data, err
	}
	reader := tar.NewReader(bytes.NewReader(data))
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if !e.noDir {
		if err := writer.WriteHeader(&tar.Header{Name: e.prefix, Typeflag: tar.TypeDir, Mode: 0o700, Uid: 65532, Gid: 65532}); err != nil {
			return nil, err
		}
	}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !e.empty {
			header.Name = e.prefix + header.Name
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := io.Copy(writer, reader); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func TestBackendVersion2ChecksExactPrivateArchivePathsBeforeHarness(t *testing.T) {
	for _, kind := range []string{"secrets", "config"} {
		for _, test := range []struct {
			name   string
			prefix string
			empty  bool
			noDir  bool
			valid  bool
		}{
			{"docker-root", kind + "/", false, false, true},
			{"docker-root-without-directory", kind + "/", false, true, true},
			{"nested-files", kind + "/unreviewed/", false, false, false},
			{"nested-files-without-directory", kind + "/unreviewed/", false, true, false},
			{"empty-nested-directory", kind + "/unreviewed/", true, false, false},
			{"foreign-root", "unreviewed/", false, false, false},
			{"foreign-root-without-directory", "unreviewed/", false, true, false},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				f := newBackendV2Fixture(t)
				e := &backendArchivePathExecutor{backendV2Executor: backendV2Executor{backendExecutor: backendExecutor{t: t, fixture: f}}, kind: kind, prefix: test.prefix, empty: test.empty, noDir: test.noDir}
				var stdout, stderr bytes.Buffer
				code := runWithExecutor(context.Background(), append([]string{"http-acceptance"}, f.args...), &stdout, &stderr, e)
				if test.valid {
					if code != 0 || e.harnessCalls != 1 {
						t.Fatalf("valid Docker archive root rejected: exit=%d harnessCalls=%d", code, e.harnessCalls)
					}
				} else if code == 0 || e.harnessCalls != 0 {
					t.Fatalf("undeclared archive path reached harness: exit=%d harnessCalls=%d", code, e.harnessCalls)
				}
				if !e.down || len(e.images) != 0 {
					t.Fatal("archive validation escaped owned resource cleanup")
				}
			})
		}
	}
}

func TestBackendVersion2RejectsNestedBinaryArchivesBeforeHarness(t *testing.T) {
	for _, binary := range []string{"fixture-server", "fixture-control"} {
		t.Run(binary, func(t *testing.T) {
			f := newBackendV2Fixture(t)
			e := &backendArchivePathExecutor{backendV2Executor: backendV2Executor{backendExecutor: backendExecutor{t: t, fixture: f}}, kind: binary, prefix: "unreviewed/", noDir: true}
			var stdout, stderr bytes.Buffer
			code := runWithExecutor(context.Background(), append([]string{"http-acceptance"}, f.args...), &stdout, &stderr, e)
			if code == 0 || e.harnessCalls != 0 {
				t.Fatalf("undeclared binary path reached harness: exit=%d harnessCalls=%d", code, e.harnessCalls)
			}
			if !e.down || len(e.images) != 0 {
				t.Fatal("binary validation escaped owned resource cleanup")
			}
		})
	}
}
