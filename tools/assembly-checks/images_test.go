package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedImages(t *testing.T) {
	services := []string{"gateway", "serving", "warehouse", "frontend"}
	valid := make(map[string]composeService, len(services))
	for _, service := range services {
		valid[service] = composeService{Image: fmt.Sprintf("ghcr.io/justikra/umir-%s:sha-%s", service, strings.Repeat("a", 40))}
	}
	if err := validatePinnedImages(composeConfig{Services: valid}); err != nil {
		t.Fatalf("valid selected images rejected: %v", err)
	}
	for name, image := range map[string]string{
		"short hash":     "ghcr.io/justikra/umir-serving:sha-" + strings.Repeat("a", 39),
		"uppercase hash": "ghcr.io/justikra/umir-serving:sha-" + strings.Repeat("A", 40),
		"wrong owner":    "ghcr.io/other/umir-serving:sha-" + strings.Repeat("a", 40),
	} {
		t.Run(name, func(t *testing.T) {
			services := make(map[string]composeService, len(valid))
			for k, v := range valid {
				services[k] = v
			}
			services["serving"] = composeService{Image: image}
			if err := validatePinnedImages(composeConfig{Services: services}); err == nil {
				t.Fatalf("invalid image %q accepted", image)
			}
		})
	}
	missing := map[string]composeService{"gateway": valid["gateway"], "serving": valid["serving"], "warehouse": valid["warehouse"]}
	if err := validatePinnedImages(composeConfig{Services: missing}); err == nil {
		t.Fatal("missing frontend service accepted")
	}
	extra := map[string]composeService{"gateway": valid["gateway"], "serving": valid["serving"], "warehouse": valid["warehouse"], "frontend": valid["frontend"], "unexpected": valid["gateway"]}
	if err := validatePinnedImages(composeConfig{Services: extra}); err == nil {
		t.Fatal("unexpected fifth service accepted")
	}
}

type composeExecutor struct {
	output []byte
	call   recordedCall
}

func (e *composeExecutor) Run(_ context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
	e.call = recordedCall{name: name, args: append([]string(nil), args...), dir: dir}
	return e.output, nil, nil
}

func TestCheckImagesReadsComposeJSON(t *testing.T) {
	services := map[string]composeService{}
	for _, service := range []string{"gateway", "serving", "warehouse", "frontend"} {
		services[service] = composeService{Image: fmt.Sprintf("ghcr.io/justikra/umir-%s:sha-%s", service, strings.Repeat("a", 40))}
	}
	jsonBytes, err := json.Marshal(map[string]any{"name": "umir-factory", "services": services})
	if err != nil {
		t.Fatal(err)
	}
	executor := &composeExecutor{output: jsonBytes}
	factory := t.TempDir()
	images, err := checkImages(context.Background(), factory, executor)
	if err != nil {
		t.Fatal(err)
	}
	if images["warehouse"] != "ghcr.io/justikra/umir-warehouse:sha-"+strings.Repeat("a", 40) {
		t.Fatalf("warehouse image = %q", images["warehouse"])
	}
	if executor.call.name != "docker" || executor.call.dir != factory {
		t.Fatalf("Compose command = %#v", executor.call)
	}
	want := []string{"compose", "--env-file", ".env.example", "config", "--format", "json"}
	if fmt.Sprint(executor.call.args) != fmt.Sprint(want) {
		t.Fatalf("Compose args = %v, want %v", executor.call.args, want)
	}
}

func TestWriteGitHubOutputPropagatesWriteFailure(t *testing.T) {
	wantErr := errors.New("disk full")
	file := &failingOutput{writeErr: wantErr}
	if err := writeGitHubOutputFile(file, map[string]string{}); !errors.Is(err, wantErr) {
		t.Fatalf("write error = %v, want wrapped disk-full error", err)
	}
	if !file.closed {
		t.Fatal("output file was not closed after write failure")
	}
}

func TestWriteGitHubOutputPropagatesCloseFailure(t *testing.T) {
	wantErr := errors.New("close failed")
	file := &failingOutput{closeErr: wantErr}
	if err := writeGitHubOutputFile(file, map[string]string{}); !errors.Is(err, wantErr) {
		t.Fatalf("close error = %v, want wrapped close error", err)
	}
	if !file.closed {
		t.Fatal("output file was not closed")
	}
}

func TestCheckImagesDoesNotSucceedWhenGitHubOutputCannotOpen(t *testing.T) {
	factory := t.TempDir()
	initRepo(t, factory)
	services := make(map[string]composeService)
	for _, service := range []string{"gateway", "serving", "warehouse", "frontend"} {
		services[service] = composeService{Image: fmt.Sprintf("ghcr.io/justikra/umir-%s:sha-%s", service, strings.Repeat("a", 40))}
	}
	composeJSON, err := json.Marshal(map[string]any{"services": services})
	if err != nil {
		t.Fatal(err)
	}
	executor := &composeExecutor{output: composeJSON}
	var stdout, stderr strings.Builder
	code := runWithExecutor(context.Background(), []string{"check-images", "--factory-dir", factory, "--github-output", filepath.Join(factory, "missing", "output")}, &stdout, &stderr, executor)
	if code == 0 || stdout.Len() != 0 {
		t.Fatalf("check-images returned %d and stdout %q after output open failure", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), "open GitHub output") {
		t.Fatalf("missing output file error was not reported: %q", stderr.String())
	}
}

type failingOutput struct {
	writeErr error
	closeErr error
	closed   bool
}

func (f *failingOutput) Write(value []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(value), nil
}

func (f *failingOutput) Close() error {
	f.closed = true
	return f.closeErr
}

var _ io.WriteCloser = (*failingOutput)(nil)
