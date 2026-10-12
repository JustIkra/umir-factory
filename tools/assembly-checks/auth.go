package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The one-port operator auth candidate (gateway#8): compose.auth.yaml applied after compose.yaml, with
// this exact marker line, publishes only frontend and enables operator sign-in.
const (
	authOverrideFile = "compose.auth.yaml"
	authMarker       = "x-umir-auth-acceptance"
	authMarkerLine   = authMarker + ": candidate"
	authLogin        = "operator"
	authDisplayName  = "Приёмка"
)

// authAssembly is one auth acceptance run: a fixed loopback frontend port that is also the public origin,
// and a disposable operator whose password exists only in a private file of the run workspace.
type authAssembly struct {
	port         int
	origin       string
	login        string
	displayName  string
	passwordHash string
	passwordFile string
}

// inputRunner runs a command with stdin; hash-password reads the password only from there.
type inputRunner interface {
	RunInput(ctx context.Context, dir string, env []string, stdin []byte, name string, args ...string) ([]byte, []byte, error)
}

func (*osExecutor) RunInput(ctx context.Context, dir string, env []string, stdin []byte, name string, args ...string) ([]byte, []byte, error) {
	return runOS(ctx, dir, env, bytes.NewReader(stdin), name, args...)
}

// verifyAuthMarker requires the explicit marker as exactly one top-level line, never inferred from
// what the assembly answers.
func verifyAuthMarker(factoryDir string) error {
	data, err := os.ReadFile(filepath.Join(factoryDir, authOverrideFile))
	if err != nil {
		return fmt.Errorf("read %s: %w", authOverrideFile, err)
	}
	markers := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, authMarker+":") {
			if line != authMarkerLine {
				return fmt.Errorf("%s declares %q, want %q", authOverrideFile, line, authMarkerLine)
			}
			markers++
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", authOverrideFile, err)
	}
	if markers != 1 {
		return fmt.Errorf("%s must declare %q exactly once, found %d", authOverrideFile, authMarkerLine, markers)
	}
	return nil
}

// prepareAuthAssembly creates the disposable operator and picks the frontend port. The gateway image
// hashes the password, so the verifier has exactly the parameters the gateway accepts.
func prepareAuthAssembly(ctx context.Context, executor commandExecutor, factoryDir, tempDir, gatewayImage string, freePort func() (int, error)) (*authAssembly, error) {
	runner, ok := executor.(inputRunner)
	if !ok {
		return nil, errors.New("the command executor cannot pass a password on stdin")
	}
	var secret [12]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, fmt.Errorf("create the disposable password: %w", err)
	}
	password := hex.EncodeToString(secret[:])
	passwordFile := filepath.Join(tempDir, "operator-password")
	if err := os.WriteFile(passwordFile, []byte(password+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write the disposable password: %w", err)
	}
	out, errOut, err := runner.RunInput(ctx, factoryDir, commandEnv(), []byte(password+"\n"), "docker", "run", "--rm", "-i", "--network", "none", gatewayImage, "hash-password")
	if err != nil {
		return nil, fmt.Errorf("hash the disposable password: %s: %w", strings.TrimSpace(string(errOut)), err)
	}
	hash := strings.TrimSuffix(string(out), "\n")
	if !strings.HasPrefix(hash, "$argon2id$") || strings.ContainsAny(hash, "\r\n") {
		return nil, errors.New("hash-password did not print one Argon2id verifier")
	}
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("choose the frontend port: %w", err)
	}
	return &authAssembly{
		port: port, origin: "http://127.0.0.1:" + strconv.Itoa(port), login: authLogin, displayName: authDisplayName,
		passwordHash: hash, passwordFile: passwordFile,
	}, nil
}

// freeLoopbackPort asks the kernel for an unused loopback port. The origin must name the port before
// Compose starts, so the port cannot be chosen by Compose.
func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if err := listener.Close(); err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.New("the loopback listener has no TCP address")
	}
	return address.Port, nil
}

// composeEnv configures compose.auth.yaml for this run.
func (a *authAssembly) composeEnv() []string {
	return []string{
		"UMIR_WEB_PORT=" + strconv.Itoa(a.port),
		"OPERATOR_COOKIE_MODE=loopback-development",
		"OPERATOR_PUBLIC_ORIGIN=" + a.origin,
		"OPERATOR_BOOTSTRAP_LOGIN=" + a.login,
		"OPERATOR_BOOTSTRAP_DISPLAY_NAME=" + a.displayName,
		"OPERATOR_BOOTSTRAP_PASSWORD_HASH=" + a.passwordHash,
	}
}

type composePublisher struct {
	URL           string `json:"URL"`
	TargetPort    int    `json:"TargetPort"`
	PublishedPort int    `json:"PublishedPort"`
}

// checkPublishedPorts proves the one-port boundary from a Compose ps report: frontend publishes exactly
// its port 8080 on loopback at port, and no other application service publishes anything. An exposed
// port that is not published reports PublishedPort 0.
func checkPublishedPorts(report []byte, port int) error {
	type container struct {
		Service    string             `json:"Service"`
		Publishers []composePublisher `json:"Publishers"`
	}
	var containers []container
	trimmed := bytes.TrimSpace(report)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &containers); err != nil {
			return fmt.Errorf("decode Compose ports: %w", err)
		}
	} else {
		for _, line := range bytes.Split(trimmed, []byte("\n")) {
			var c container
			if err := json.Unmarshal(line, &c); err != nil {
				return fmt.Errorf("decode Compose ports: %w", err)
			}
			containers = append(containers, c)
		}
	}
	frontend := 0
	for _, c := range containers {
		switch c.Service {
		case "warehouse", "serving", "gateway", "frontend":
		default:
			continue
		}
		for _, p := range c.Publishers {
			if p.PublishedPort == 0 {
				continue
			}
			if c.Service != "frontend" {
				return fmt.Errorf("%s publishes host port %d; only frontend may publish", c.Service, p.PublishedPort)
			}
			if p.URL != "127.0.0.1" || p.TargetPort != 8080 || p.PublishedPort != port {
				return fmt.Errorf("frontend publishes %s:%d->%d, want 127.0.0.1:%d->8080", p.URL, p.PublishedPort, p.TargetPort, port)
			}
			frontend++
		}
	}
	if frontend != 1 {
		return fmt.Errorf("frontend publishes %d host ports, want exactly one", frontend)
	}
	return nil
}
