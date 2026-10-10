package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type recordedCall struct {
	name string
	args []string
	dir  string
	env  []string
}

type fakeExecutor struct {
	calls        []recordedCall
	fail         error
	workContents string
}

func (f *fakeExecutor) Run(_ context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
	call := recordedCall{name: name, args: append([]string(nil), args...), dir: dir}
	for _, entry := range env {
		if strings.HasPrefix(entry, "GOWORK=") {
			call.env = append(call.env, entry)
		}
	}
	f.calls = append(f.calls, call)
	if name == "go" {
		for _, entry := range env {
			if strings.HasPrefix(entry, "GOWORK=") {
				contents, err := os.ReadFile(strings.TrimPrefix(entry, "GOWORK="))
				if err != nil {
					return nil, nil, err
				}
				f.workContents = string(contents)
			}
		}
		return nil, nil, f.fail
	}
	return (&osExecutor{}).Run(context.Background(), dir, env, name, args...)
}

func TestModesExclusive(t *testing.T) {
	factory := factoryWithBusinessModule(t)
	var stdout, stderr bytes.Buffer
	executor := &fakeExecutor{}
	code := runWithExecutor(context.Background(), []string{"business", "--factory-dir", factory, "--local", "--warehouse-ref", strings.Repeat("a", 40), "--serving-ref", strings.Repeat("b", 40)}, &stdout, &stderr, executor)
	if code != 2 {
		t.Fatalf("business returned %d, want usage error 2", code)
	}
	if strings.TrimSpace(stderr.String()) != "--local and pinned refs are mutually exclusive" {
		t.Fatalf("wrong validation error: %q", stderr.String())
	}
	if len(executor.calls) != 0 {
		t.Fatalf("mode validation reached subprocess: %#v", executor.calls)
	}
}

func TestRequiresFactoryCheckoutBeforeCommands(t *testing.T) {
	executor := &fakeExecutor{}
	code := runWithExecutor(context.Background(), []string{"check-images", "--factory-dir", t.TempDir()}, &bytes.Buffer{}, &bytes.Buffer{}, executor)
	if code == 0 {
		t.Fatal("directory without Git checkout metadata was accepted")
	}
	if len(executor.calls) != 0 {
		t.Fatalf("command ran before checkout validation: %#v", executor.calls)
	}
}

func TestRejectsNonSHA(t *testing.T) {
	factory := factoryWithBusinessModule(t)
	for name, flags := range map[string][]string{
		"missing serving ref": {"--warehouse-ref", strings.Repeat("a", 40)},
		"short ref":           {"--warehouse-ref", strings.Repeat("a", 39), "--serving-ref", strings.Repeat("b", 40)},
		"uppercase ref":       {"--warehouse-ref", strings.Repeat("A", 40), "--serving-ref", strings.Repeat("b", 40)},
		"non-hex ref":         {"--warehouse-ref", strings.Repeat("g", 40), "--serving-ref", strings.Repeat("b", 40)},
	} {
		t.Run(name, func(t *testing.T) {
			executor := &fakeExecutor{}
			args := append([]string{"business", "--factory-dir", factory}, flags...)
			var stderr bytes.Buffer
			if code := runWithExecutor(context.Background(), args, &bytes.Buffer{}, &stderr, executor); code != 2 {
				t.Fatalf("invalid ref selection returned %d, want usage error 2", code)
			}
			wantError := "service refs must be exactly 40 lowercase hexadecimal characters"
			if name == "missing serving ref" {
				wantError = "business requires --local or both --warehouse-ref and --serving-ref"
			}
			if strings.TrimSpace(stderr.String()) != wantError {
				t.Fatalf("wrong validation error: %q", stderr.String())
			}
			if len(executor.calls) != 0 {
				t.Fatalf("invalid refs reached subprocess: %#v", executor.calls)
			}
		})
	}
}

func factoryWithBusinessModule(t *testing.T) string {
	t.Helper()
	factory := t.TempDir()
	initRepo(t, factory)
	businessDir := filepath.Join(factory, "tests", "business")
	if err := os.MkdirAll(businessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(businessDir, "go.mod"), []byte("module example.test/business\n\ngo 1.26.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return factory
}

func TestWorkspaceUsesRealPaths(t *testing.T) {
	factory := t.TempDir()
	initRepo(t, factory)
	parent := filepath.Dir(factory)
	warehouse := filepath.Join(parent, "umir-warehouse")
	serving := filepath.Join(parent, "umir-serving")
	initRepo(t, warehouse)
	initRepo(t, serving)
	if err := os.MkdirAll(filepath.Join(factory, "tests", "business"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factory, "tests", "business", "go.mod"), []byte("module example.test/business\n\ngo 1.26.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	code := runWithExecutor(context.Background(), []string{"business", "--factory-dir", factory, "--local"}, &bytes.Buffer{}, &bytes.Buffer{}, exec)
	if code != 0 {
		t.Fatalf("business returned %d", code)
	}
	var goCall *recordedCall
	for i := range exec.calls {
		if exec.calls[i].name == "go" {
			goCall = &exec.calls[i]
		}
	}
	if goCall == nil {
		t.Fatal("runner did not execute go test")
	}
	resolvedFactory, err := filepath.EvalSymlinks(factory)
	if err != nil {
		t.Fatal(err)
	}
	if goCall.dir != filepath.Join(resolvedFactory, "tests", "business") {
		t.Fatalf("go test dir = %q", goCall.dir)
	}
	if fmt.Sprint(goCall.args) != fmt.Sprint([]string{"test", "-race", "./..."}) {
		t.Fatalf("business command args = %v", goCall.args)
	}
	resolvedWarehouse, err := filepath.EvalSymlinks(warehouse)
	if err != nil {
		t.Fatal(err)
	}
	resolvedServing, err := filepath.EvalSymlinks(serving)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(resolvedFactory, "tests", "business"), resolvedWarehouse, resolvedServing} {
		if !strings.Contains(exec.workContents, "\""+path+"\"") {
			t.Errorf("go.work does not contain absolute module path %q: %s", path, exec.workContents)
		}
	}
	workPath := envValue(goCall.env, "GOWORK")
	if workPath == "" || !filepath.IsAbs(workPath) {
		t.Fatalf("GOWORK is not absolute: %q", workPath)
	}
	if _, err := os.Stat(workPath); !os.IsNotExist(err) {
		t.Fatalf("temporary go.work still exists after command: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(workPath)); !os.IsNotExist(err) {
		t.Fatalf("runner temporary directory still exists after command: %v", err)
	}
	for _, path := range []string{filepath.Join(warehouse, "go.mod"), filepath.Join(serving, "go.mod"), filepath.Join(factory, "tests", "business", "go.mod")} {
		if !filepath.IsAbs(path) {
			t.Fatalf("workspace path is not absolute: %q", path)
		}
	}
}

func TestReturnsGoFailure(t *testing.T) {
	factory := t.TempDir()
	initRepo(t, factory)
	parent := filepath.Dir(factory)
	initRepo(t, filepath.Join(parent, "umir-warehouse"))
	initRepo(t, filepath.Join(parent, "umir-serving"))
	if err := os.MkdirAll(filepath.Join(factory, "tests", "business"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factory, "tests", "business", "go.mod"), []byte("module example.test/business\n\ngo 1.26.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{fail: codedError(23)}
	code := runWithExecutor(context.Background(), []string{"business", "--factory-dir", factory, "--local"}, &bytes.Buffer{}, &bytes.Buffer{}, exec)
	if code != 23 {
		t.Fatalf("run returned %d, want exact child code 23", code)
	}
}

type codedError int

func (e codedError) Error() string { return "child failed" }
func (e codedError) ExitCode() int { return int(e) }

func TestCleansTempFiles(t *testing.T) {
	factory := t.TempDir()
	initRepo(t, factory)
	parent := filepath.Dir(factory)
	warehouse := filepath.Join(parent, "umir-warehouse")
	serving := filepath.Join(parent, "umir-serving")
	initRepo(t, warehouse)
	initRepo(t, serving)
	if err := os.MkdirAll(filepath.Join(factory, "tests", "business"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factory, "tests", "business", "go.mod"), []byte("module example.test/business\n\ngo 1.26.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	warehouseMod := read(t, filepath.Join(warehouse, "go.mod"))
	servingMod := read(t, filepath.Join(serving, "go.mod"))
	exec := &fakeExecutor{}
	runWithExecutor(context.Background(), []string{"business", "--factory-dir", factory, "--local"}, &bytes.Buffer{}, &bytes.Buffer{}, exec)
	if got := read(t, filepath.Join(warehouse, "go.mod")); got != warehouseMod {
		t.Fatal("runner rewrote warehouse go.mod")
	}
	if got := read(t, filepath.Join(serving, "go.mod")); got != servingMod {
		t.Fatal("runner rewrote serving go.mod")
	}
	for _, path := range []string{warehouse, serving, filepath.Join(factory, "tests", "business")} {
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "go.work") {
				t.Fatalf("runner left %s in checkout %s", entry.Name(), path)
			}
		}
	}
	if len(exec.calls) == 0 {
		t.Fatal("runner did not execute checks")
	}
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/service\n\ngo 1.26.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "go.mod"}, {"-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func envValue(env []string, key string) string {
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") {
			return strings.TrimPrefix(entry, key+"=")
		}
	}
	return ""
}
