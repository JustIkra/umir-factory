package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

type commandExecutor interface {
	Run(context.Context, string, []string, string, ...string) ([]byte, []byte, error)
}

type osExecutor struct{}

func (*osExecutor) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
	return runOS(ctx, dir, env, nil, name, args...)
}

func runOS(ctx context.Context, dir string, env []string, stdin io.Reader, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runWithExecutor(ctx, args, stdout, stderr, &osExecutor{})
}

func runWithExecutor(ctx context.Context, args []string, stdout, stderr io.Writer, executor commandExecutor) int {
	if len(args) == 0 {
		writeFailure(stderr, "expected business, check-images, http-acceptance, or smoke command\n")
		return 2
	}
	command := args[0]
	if command != "business" && command != "check-images" && command != "http-acceptance" && command != "smoke" {
		writeFailure(stderr, "unknown command %q\n", command)
		return 2
	}
	options, err := parseOptions(command, args[1:])
	if err != nil {
		writeFailure(stderr, "%v\n", err)
		return 2
	}
	factoryDir, err := absoluteFactoryDir(options.factoryDir)
	if err != nil {
		writeFailure(stderr, "%v\n", err)
		return 2
	}
	switch command {
	case "business":
		return runBusiness(ctx, factoryDir, options, executor, stdout, stderr)
	case "check-images":
		images, err := checkImages(ctx, factoryDir, executor)
		if err != nil {
			writeFailure(stderr, "%v\n", err)
			return 1
		}
		if options.githubOutput != "" {
			if err := writeGitHubOutput(options.githubOutput, images); err != nil {
				writeFailure(stderr, "%v\n", err)
				return 1
			}
		}
		if err := writeMessage(stdout, "All service images use full commit SHA tags\n"); err != nil {
			return 1
		}
		return 0
	case "http-acceptance":
		return runHTTPAcceptance(ctx, factoryDir, options, executor, stdout, stderr)
	case "smoke":
		targets, err := discoverSmokeTargets(ctx, factoryDir, options, executor)
		if err != nil {
			writeFailure(stderr, "%v\n", err)
			return 1
		}
		if err := runSmoke(ctx, defaultHTTPClient(), targets); err != nil {
			writeFailure(stderr, "%v\n", err)
			return 1
		}
		if err := writeMessage(stdout, "Smoke checks passed\n"); err != nil {
			return 1
		}
		return 0
	}
	return 2
}

type cliOptions struct {
	factoryDir string
	envFile    string
	webURL     string
	apiURL     string
	local      bool
	warehouse  string
	serving    string
	gateway    string
	frontend   string
	candidate  bool
	selected   bool
	// auth runs the candidate as the one-port operator auth assembly of compose.auth.yaml.
	auth         bool
	feedbackRef  string
	githubOutput string
}

func parseOptions(command string, args []string) (cliOptions, error) {
	var options cliOptions
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.factoryDir, "factory-dir", "", "absolute factory checkout path")
	if command == "business" {
		flags.BoolVar(&options.local, "local", false, "use sibling service checkouts")
		flags.StringVar(&options.warehouse, "warehouse-ref", "", "full warehouse commit SHA")
		flags.StringVar(&options.serving, "serving-ref", "", "full serving commit SHA")
	}
	if command == "check-images" {
		flags.StringVar(&options.githubOutput, "github-output", "", "append selected service refs to a GitHub Actions output file")
	}
	if command == "http-acceptance" {
		flags.BoolVar(&options.candidate, "candidate", false, "build exact supplied commits as local test images")
		flags.BoolVar(&options.selected, "selected", false, "pull and verify the selected published images")
		flags.BoolVar(&options.auth, "auth", false, "run the candidate as the one-port operator auth assembly")
		flags.StringVar(&options.feedbackRef, "feedback-ref", "", "full feedback service commit SHA for private feedback acceptance")
		flags.StringVar(&options.warehouse, "warehouse-ref", "", "full warehouse commit SHA")
		flags.StringVar(&options.serving, "serving-ref", "", "full serving commit SHA")
		flags.StringVar(&options.gateway, "gateway-ref", "", "full gateway commit SHA")
		flags.StringVar(&options.frontend, "frontend-ref", "", "full frontend commit SHA")
	}
	if command == "smoke" {
		flags.StringVar(&options.envFile, "env-file", "", "Compose environment file, relative to the factory checkout")
		flags.StringVar(&options.webURL, "web-url", "", "override the frontend URL")
		flags.StringVar(&options.apiURL, "api-url", "", "override the gateway URL")
	}
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if options.factoryDir == "" {
		return options, errors.New("--factory-dir is required")
	}
	if !filepath.IsAbs(options.factoryDir) {
		return options, errors.New("--factory-dir must be an absolute path")
	}
	if command == "business" {
		if options.local {
			if options.warehouse != "" || options.serving != "" {
				return options, errors.New("--local and pinned refs are mutually exclusive")
			}
			return options, nil
		}
		if options.warehouse == "" || options.serving == "" {
			return options, errors.New("business requires --local or both --warehouse-ref and --serving-ref")
		}
		if !isSHA(options.warehouse) || !isSHA(options.serving) {
			return options, errors.New("service refs must be exactly 40 lowercase hexadecimal characters")
		}
	}
	if command == "http-acceptance" {
		if options.candidate == options.selected {
			return options, errors.New("choose exactly one of --candidate or --selected")
		}
		if options.auth && !options.candidate {
			return options, errors.New("--auth requires --candidate: the selected assembly keeps its published contract")
		}
		if options.feedbackRef != "" {
			if !options.candidate || !options.auth {
				return options, errors.New("--feedback-ref requires --candidate and --auth")
			}
			if !isSHA(options.feedbackRef) {
				return options, errors.New("feedback ref must be exactly 40 lowercase hexadecimal characters")
			}
		}
		refs := []string{options.warehouse, options.serving, options.gateway, options.frontend}
		if options.candidate {
			for _, ref := range refs {
				if !isSHA(ref) {
					return options, errors.New("candidate requires four full lowercase 40-character service SHAs")
				}
			}
		} else {
			for _, ref := range refs {
				if ref != "" {
					return options, errors.New("--selected does not accept candidate refs")
				}
			}
			if options.feedbackRef != "" {
				return options, errors.New("--selected does not accept --feedback-ref")
			}
		}
	}
	if command == "smoke" {
		if options.webURL != "" {
			if _, err := parseSmokeURL(options.webURL); err != nil {
				return options, fmt.Errorf("invalid --web-url: %w", err)
			}
		}
		if options.apiURL != "" {
			if _, err := parseSmokeURL(options.apiURL); err != nil {
				return options, fmt.Errorf("invalid --api-url: %w", err)
			}
		}
	}
	return options, nil
}

func absoluteFactoryDir(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("--factory-dir must be an absolute path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("factory directory: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("factory directory is not an existing directory: %s", path)
	}
	if _, err := os.Stat(filepath.Join(resolved, ".git")); err != nil {
		return "", fmt.Errorf("factory directory is not a Git checkout: %s", path)
	}
	return resolved, nil
}

func isSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' {
			continue
		}
		return false
	}
	return true
}

func processCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		if code > 0 && code <= 255 {
			return code
		}
	}
	return 1
}

func commandEnv(overrides ...string) []string {
	env := os.Environ()
	for _, override := range overrides {
		key := strings.SplitN(override, "=", 2)[0]
		filtered := env[:0]
		for _, value := range env {
			if !strings.HasPrefix(value, key+"=") {
				filtered = append(filtered, value)
			}
		}
		env = append(filtered, override)
	}
	return env
}

func writeGitHubOutput(path string, images map[string]string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open GitHub output: %w", err)
	}
	return writeGitHubOutputFile(file, images)
}

func writeMessage(w io.Writer, format string, values ...any) error {
	return writeAll(w, []byte(fmt.Sprintf(format, values...)))
}

func writeFailure(w io.Writer, format string, values ...any) {
	if err := writeMessage(w, format, values...); err != nil {
		return
	}
}

func writeAll(w io.Writer, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	written, err := w.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func writeGitHubOutputFile(file io.WriteCloser, images map[string]string) error {
	var writeErr error
	for _, service := range []string{"warehouse", "serving"} {
		line := fmt.Sprintf("%s_ref=%s\n", service, strings.TrimPrefix(images[service], "ghcr.io/justikra/umir-"+service+":sha-"))
		written, err := io.WriteString(file, line)
		if err != nil {
			writeErr = fmt.Errorf("write GitHub output: %w", err)
			break
		}
		if written != len(line) {
			writeErr = fmt.Errorf("write GitHub output: %w", io.ErrShortWrite)
			break
		}
	}
	if err := file.Close(); err != nil {
		writeErr = errors.Join(writeErr, fmt.Errorf("close GitHub output: %w", err))
	}
	return writeErr
}
