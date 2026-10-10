package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var serviceRepositories = map[string]string{
	"warehouse": "https://github.com/JustIkra/umir-warehouse.git",
	"serving":   "https://github.com/JustIkra/umir-serving.git",
}

func runBusiness(ctx context.Context, factoryDir string, options cliOptions, executor commandExecutor, stdout, stderr io.Writer) int {
	return runBusinessWithCleanup(ctx, factoryDir, options, executor, stdout, stderr, os.MkdirTemp, os.RemoveAll)
}

func runBusinessWithCleanup(ctx context.Context, factoryDir string, options cliOptions, executor commandExecutor, stdout, stderr io.Writer, createTemp func(string, string) (string, error), removeTemp func(string) error) (code int) {
	businessDir := filepath.Join(factoryDir, "tests", "business")
	if _, err := os.Stat(filepath.Join(businessDir, "go.mod")); err != nil {
		writeFailure(stderr, "business test module is unavailable: %v\n", err)
		return 1
	}
	tempDir, err := createTemp("", "umir-business-checks-")
	if err != nil {
		writeFailure(stderr, "create runner workspace: %v\n", err)
		return 1
	}
	defer func() {
		if err := removeTemp(tempDir); err != nil {
			if code == 0 {
				code = 1
			}
			writeFailure(stderr, "remove runner workspace: %v\n", err)
		}
	}()
	paths := map[string]string{}
	if options.local {
		parent := filepath.Dir(factoryDir)
		paths["warehouse"] = filepath.Join(parent, "umir-warehouse")
		paths["serving"] = filepath.Join(parent, "umir-serving")
		for service, path := range paths {
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err != nil {
				writeFailure(stderr, "local %s module is unavailable: %v\n", service, err)
				return 1
			}
			if _, err := verifyHead(ctx, executor, path, ""); err != nil {
				writeFailure(stderr, "verify local %s checkout: %v\n", service, err)
				return 1
			}
		}
	} else {
		refs := map[string]string{"warehouse": options.warehouse, "serving": options.serving}
		for _, service := range []string{"warehouse", "serving"} {
			path := filepath.Join(tempDir, service)
			if err := fetchPinned(ctx, executor, path, serviceRepositories[service], refs[service]); err != nil {
				writeFailure(stderr, "checkout pinned %s source: %v\n", service, err)
				return 1
			}
			paths[service] = path
		}
	}
	workFile := filepath.Join(tempDir, "go.work")
	content := "go 1.26.5\n\nuse (\n"
	for _, path := range []string{businessDir, paths["warehouse"], paths["serving"]} {
		content += "\t" + strconv.Quote(path) + "\n"
	}
	content += ")\n"
	if err := os.WriteFile(workFile, []byte(content), 0o600); err != nil {
		writeFailure(stderr, "write temporary go.work: %v\n", err)
		return 1
	}
	env := commandEnv("GOWORK=" + workFile)
	out, errOut, err := executor.Run(ctx, businessDir, env, "go", "test", "-race", "./...")
	stdoutErr := writeAll(stdout, out)
	stderrErr := writeAll(stderr, errOut)
	if err != nil {
		if childCode := processCode(err); childCode != 1 {
			return childCode
		}
		if stderrErr != nil {
			return 1
		}
		writeFailure(stderr, "business Go tests failed: %v\n", err)
		return 1
	}
	if stdoutErr != nil {
		writeFailure(stderr, "write business test output: %v\n", stdoutErr)
		return 1
	}
	if stderrErr != nil {
		return 1
	}
	return 0
}

func fetchPinned(ctx context.Context, executor commandExecutor, dir, remote, ref string) error {
	if !isSHA(ref) {
		return errors.New("ref must be exactly 40 lowercase hexadecimal characters")
	}
	if _, _, err := executor.Run(ctx, filepath.Dir(dir), commandEnv(), "git", "clone", "--no-checkout", "--", remote, dir); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}
	if _, _, err := executor.Run(ctx, dir, commandEnv(), "git", "fetch", "--depth=1", "origin", ref); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	if _, _, err := executor.Run(ctx, dir, commandEnv(), "git", "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("git checkout: %w", err)
	}
	if _, err := verifyHead(ctx, executor, dir, ref); err != nil {
		return err
	}
	return nil
}

func verifyHead(ctx context.Context, executor commandExecutor, dir, expected string) (string, error) {
	out, _, err := executor.Run(ctx, dir, commandEnv(), "git", "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	head := strings.TrimSpace(string(out))
	if !isSHA(head) {
		return "", fmt.Errorf("invalid HEAD %q", head)
	}
	if expected != "" && head != expected {
		return "", fmt.Errorf("HEAD %s does not match requested %s", head, expected)
	}
	return head, nil
}
