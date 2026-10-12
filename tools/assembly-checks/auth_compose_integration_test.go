//go:build composeintegration

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type authComposeService struct {
	Environment map[string]string   `json:"environment"`
	Ports       []composePortConfig `json:"ports"`
	Volumes     []struct {
		Type   string `json:"type"`
		Source string `json:"source"`
		Target string `json:"target"`
	} `json:"volumes"`
}

type authComposeConfig struct {
	Services map[string]authComposeService `json:"services"`
	Volumes  map[string]json.RawMessage    `json:"volumes"`
}

func TestAuthCandidateOverridePublishesOnlyTheFrontend(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("docker CLI is required for composeintegration tests")
	}
	factoryDir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(factoryDir, "compose.auth.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains("\n"+string(marker), "\nx-umir-auth-acceptance: candidate\n") {
		t.Fatal("compose.auth.yaml has no auth candidate marker")
	}
	env := []string{
		"UMIR_BIND=127.0.0.1", "UMIR_WEB_PORT=18080", "UMIR_API_PORT=0",
		"OPERATOR_PUBLIC_ORIGIN=http://127.0.0.1:18080", "OPERATOR_BOOTSTRAP_LOGIN=operator",
		"OPERATOR_BOOTSTRAP_DISPLAY_NAME=Приёмка", "OPERATOR_BOOTSTRAP_PASSWORD_HASH=$argon2id$v=19$m=65536,t=2,p=1$c2FsdA$aGFzaA",
	}
	config := authComposeOutput(t, docker, factoryDir, env)
	for service, definition := range config.Services {
		if ports := definition.Ports; service != "frontend" && len(ports) != 0 {
			t.Errorf("auth candidate publishes %s ports: %#v", service, ports)
		}
	}
	frontend := config.Services["frontend"].Ports
	if len(frontend) != 1 || frontend[0].HostIP != "127.0.0.1" || frontend[0].Published != "18080" || frontend[0].Target != 8080 {
		t.Errorf("frontend ports = %#v, want only 127.0.0.1:18080->8080", frontend)
	}
	gateway := config.Services["gateway"]
	for key, want := range map[string]string{
		"OPERATOR_COOKIE_MODE": "loopback-development", "OPERATOR_PUBLIC_ORIGIN": "http://127.0.0.1:18080",
		"OPERATOR_BOOTSTRAP_LOGIN": "operator", "OPERATOR_BOOTSTRAP_PASSWORD_HASH": "$argon2id$v=19$m=65536,t=2,p=1$c2FsdA$aGFzaA",
	} {
		// The rendered config escapes $ as $$; the container receives the value unescaped.
		if got := strings.ReplaceAll(gateway.Environment[key], "$$", "$"); got != want {
			t.Errorf("gateway %s = %q, want %q", key, got, want)
		}
	}
	if len(gateway.Volumes) != 1 || gateway.Volumes[0].Type != "volume" || gateway.Volumes[0].Source != "operator-data" || gateway.Volumes[0].Target != "/data/operator" {
		t.Errorf("gateway volumes = %#v, want the operator-data volume at /data/operator", gateway.Volumes)
	}
	if _, ok := config.Volumes["operator-data"]; !ok {
		t.Error("the operator-data volume is not declared")
	}
	// The public origin is required: the gateway must never start with a guessed one.
	args := authComposeArgs(factoryDir)
	command := exec.Command(docker, append(args, "config", "--quiet")...)
	command.Dir = factoryDir
	command.Env = commandEnv("UMIR_BIND=127.0.0.1", "UMIR_WEB_PORT=0", "UMIR_API_PORT=0", "OPERATOR_PUBLIC_ORIGIN=")
	if out, err := command.CombinedOutput(); err == nil {
		t.Errorf("Compose accepted the auth candidate without OPERATOR_PUBLIC_ORIGIN: %s", out)
	}
}

func authComposeArgs(factoryDir string) []string {
	return []string{
		"compose", "--project-name", "umir-http-config", "--file", filepath.Join(factoryDir, "compose.yaml"),
		"--file", filepath.Join(factoryDir, "compose.auth.yaml"), "--env-file", filepath.Join(factoryDir, ".env.example"),
	}
}

func authComposeOutput(t *testing.T, docker, factoryDir string, env []string) authComposeConfig {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, docker, append(authComposeArgs(factoryDir), "config", "--format", "json")...)
	command.Dir = factoryDir
	command.Env = commandEnv(env...)
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose config failed: %s: %v", data, err)
	}
	var result authComposeConfig
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode Compose config: %v", err)
	}
	return result
}
