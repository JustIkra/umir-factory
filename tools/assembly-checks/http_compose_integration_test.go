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

func TestHTTPComposeOverridesResolveAsIsolatedTopologies(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("docker CLI is required for composeintegration tests")
	}
	factoryDir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	temporary, err := os.MkdirTemp("", "umir-http-compose-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(temporary); err != nil {
			t.Errorf("remove compose integration files: %v", err)
		}
	}()
	options := cliOptions{candidate: true, warehouse: strings.Repeat("a", 40), serving: strings.Repeat("b", 40), gateway: strings.Repeat("c", 40), frontend: strings.Repeat("d", 40)}
	normal := filepath.Join(temporary, "normal.json")
	if err := writeHTTPImageOverride(normal, options, "umir-http-integration"); err != nil {
		t.Fatal(err)
	}
	normalConfig := composeConfigOutput(t, docker, factoryDir, temporary, normal)
	for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
		if normalConfig.Services[service].Image != candidateImageRef(service, map[string]string{"warehouse": options.warehouse, "serving": options.serving, "gateway": options.gateway, "frontend": options.frontend}[service], "umir-http-integration") {
			t.Errorf("normal candidate config substituted %s image: %s", service, normalConfig.Services[service].Image)
		}
	}
	if len(normalConfig.Services["warehouse"].Ports) != 0 {
		t.Fatalf("normal topology publishes warehouse ports: %#v", normalConfig.Services["warehouse"].Ports)
	}
	faults := filepath.Join(temporary, "faults.json")
	if err := writeHTTPOverride(faults, options, "umir-http-integration", "umir-factory/http-fault:test", strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	faultConfig := composeConfigOutput(t, docker, factoryDir, temporary, faults)
	if len(faultConfig.Services) != 6 {
		t.Fatalf("fault topology has %d services, want four services and two fixtures", len(faultConfig.Services))
	}
	if faultConfig.Services["serving"].Environment["WAREHOUSE_INTERNAL_URL"] != "http://http-fault-warehouse:8082" {
		t.Fatalf("serving does not route internal requests through the warehouse fixture: %#v", faultConfig.Services["serving"].Environment)
	}
	if faultConfig.Services["gateway"].Environment["SERVING_URL"] != "http://http-fault-serving:8080" {
		t.Fatalf("gateway does not route serving requests through its fixture: %#v", faultConfig.Services["gateway"].Environment)
	}
	if len(faultConfig.Services["warehouse"].Ports) != 0 || len(faultConfig.Services["serving"].Ports) != 0 {
		t.Fatal("fault topology publishes a service listener to the host")
	}
	for name, fixture := range map[string]string{"http-fault-serving": "serving", "http-fault-warehouse": "warehouse"} {
		if len(faultConfig.Services[name].Ports) != 1 || faultConfig.Services[name].Ports[0].HostIP != "127.0.0.1" {
			t.Errorf("%s control port is not bound to loopback: %#v", name, faultConfig.Services[name].Ports)
		}
		if faultConfig.Services[name].DependsOn[fixture].Condition != "service_healthy" {
			t.Errorf("%s has no healthy upstream dependency", name)
		}
	}
	selectedOptions := cliOptions{selected: true}
	selected := filepath.Join(temporary, "selected.json")
	if err := writeHTTPOverride(selected, selectedOptions, "umir-http-integration", "umir-factory/http-fault:test", strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	selectedConfig := composeConfigOutput(t, docker, factoryDir, temporary, selected)
	baseConfig := composeConfigOutput(t, docker, factoryDir, temporary, "")
	for _, service := range []string{"warehouse", "serving", "gateway", "frontend"} {
		if selectedConfig.Services[service].Image != baseConfig.Services[service].Image {
			t.Errorf("selected topology substituted %s image", service)
		}
	}
}

func composeConfigOutput(t *testing.T, docker, factoryDir, temporary, override string) composeConfig {
	t.Helper()
	args := []string{"compose", "--project-name", "umir-http-config", "--file", filepath.Join(factoryDir, "compose.yaml")}
	if override != "" {
		args = append(args, "--file", override)
	}
	args = append(args, "--env-file", filepath.Join(factoryDir, ".env.example"), "config", "--format", "json")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, docker, args...)
	command.Dir = factoryDir
	command.Env = commandEnv("UMIR_BIND=127.0.0.1", "UMIR_WEB_PORT=0", "UMIR_API_PORT=0")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose config failed: %s: %v", data, err)
	}
	var result composeConfig
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode Compose config: %v", err)
	}
	return result
}
