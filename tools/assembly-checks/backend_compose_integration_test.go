//go:build composeintegration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBackendComposePreservesReviewedArguments(t *testing.T) {
	fixture := newBackendFixture(t)
	literals := []string{"$F2_REVIEW_LITERAL", "${F2_REVIEW_LITERAL}", "${F2_REVIEW_MISSING:-host-default}", "$$", "$", "price=$5"}
	commandArgs := append([]string{"/service"}, literals...)
	healthArgs := append([]string{"CMD", "/health"}, literals...)
	wantCommand := []string{"/service", "$$F2_REVIEW_LITERAL", "$${F2_REVIEW_LITERAL}", "$${F2_REVIEW_MISSING:-host-default}", "$$$$", "$$", "price=$$5"}
	wantHealth := []string{"CMD", "/health", "$$F2_REVIEW_LITERAL", "$${F2_REVIEW_LITERAL}", "$${F2_REVIEW_MISSING:-host-default}", "$$$$", "$$", "price=$$5"}
	for _, name := range backendServices {
		fixture.service(name)["command"] = commandArgs
		fixture.service(name)["healthcheck"] = healthArgs
	}
	for _, name := range []string{"smtp", "notification"} {
		backendValue[map[string]any](t, backendValue[map[string]any](t, fixture.document["sinks"])[name])["command"] = commandArgs
	}
	fixture.refresh(t)
	options, err := parseOptions("http-acceptance", fixture.args)
	if err != nil {
		t.Fatal(err)
	}
	input, err := readBackendInputs(options)
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]imageEvidence{}
	for _, name := range backendServices {
		images[name] = imageEvidence{Ref: "umir-backend/compose-test-" + name + ":candidate"}
	}
	secrets, err := backendSecrets()
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	compose, _, err := writeBackendTopology(temp, "umir-backend-compose-arguments", input, images, secrets, "http://127.0.0.1:49152", 49152)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("F2_REVIEW_LITERAL", "host-value-not-in-reviewed-input")
	t.Setenv("F2_REVIEW_MISSING", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", append(backendComposeArgs("umir-backend-compose-arguments", compose), "config", "--format", "json")...)
	command.Dir = temp
	data, err := command.Output()
	if err != nil {
		t.Fatalf("generated backend Compose config failed: %v", err)
	}
	var topology struct {
		Services map[string]struct {
			Command     []string `json:"command"`
			Healthcheck struct {
				Test []string `json:"test"`
			} `json:"healthcheck"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &topology); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(slices.Clone(backendServices), "smtp", "notification") {
		service := topology.Services[name]
		if !slices.Equal(service.Command, wantCommand) {
			t.Errorf("%s canonical command changed: got %q, want %q", name, service.Command, wantCommand)
		}
		if slices.Contains(backendServices, name) && !slices.Equal(service.Healthcheck.Test, wantHealth) {
			t.Errorf("%s canonical healthcheck changed: got %q, want %q", name, service.Healthcheck.Test, wantHealth)
		}
	}
	for _, name := range backendServices {
		if !slices.Equal(input.Services[name].Command, commandArgs) || !slices.Equal(input.Services[name].Healthcheck, healthArgs) {
			t.Fatal("topology mutated the reviewed input")
		}
	}
}

func TestBackendComposeResolvesOnlyOwnedIsolatedResources(t *testing.T) {
	fixture := newBackendFixture(t)
	options, err := parseOptions("http-acceptance", fixture.args)
	if err != nil {
		t.Fatal(err)
	}
	input, err := readBackendInputs(options)
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]imageEvidence{}
	for _, name := range backendServices {
		images[name] = imageEvidence{Ref: "umir-backend/compose-test-" + name + ":candidate"}
	}
	secrets, err := backendSecrets()
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	compose, _, err := writeBackendTopology(temp, "umir-backend-compose-test", input, images, secrets, "http://127.0.0.1:49152", 49152)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", append(backendComposeArgs("umir-backend-compose-test", compose), "config", "--format", "json")...)
	command.Dir = temp
	data, err := command.Output()
	if err != nil {
		t.Fatalf("generated backend Compose config failed: %v", err)
	}
	var topology map[string]any
	if err := json.Unmarshal(data, &topology); err != nil {
		t.Fatal(err)
	}
	services := backendValue[map[string]any](t, topology["services"])
	if len(services) != 7 {
		t.Fatalf("expected 7 services, got %d", len(services))
	}
	for _, name := range []string{"auth", "warehouse", "serving", "feedback"} {
		if _, ok := backendValue[map[string]any](t, services[name])["ports"]; ok {
			t.Fatalf("%s publishes a private port", name)
		}
	}
	for _, name := range []string{"gateway", "smtp", "notification"} {
		ports := backendValue[[]any](t, backendValue[map[string]any](t, services[name])["ports"])
		if len(ports) != 1 || backendValue[map[string]any](t, ports[0])["host_ip"] != "127.0.0.1" {
			t.Fatalf("%s is not loopback-only", name)
		}
	}
	if backendValue[map[string]any](t, backendValue[map[string]any](t, topology["networks"])["private"])["internal"] != true {
		t.Fatal("private network permits delivery egress")
	}
	volumes := backendValue[map[string]any](t, topology["volumes"])
	for _, name := range []string{"auth-state", "feedback-state"} {
		volume := backendValue[map[string]any](t, volumes[name])
		if volume["name"] != "umir-backend-compose-test_"+name {
			t.Fatalf("volume %s escapes project", name)
		}
		if external, ok := volume["external"]; ok && external != false {
			t.Fatal("uses external durable state")
		}
	}
	raw, err := os.ReadFile(compose)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret embedded into Compose")
		}
	}
	for _, name := range backendServices {
		info, err := os.Stat(filepath.Join(temp, name+".env"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s env file is not private", name)
		}
	}
}

func TestBackendComposeVersion2PrivateMounts(t *testing.T) {
	fixture := newBackendV2Fixture(t)
	options, err := parseOptions("http-acceptance", fixture.args)
	if err != nil {
		t.Fatal(err)
	}
	input, err := readBackendInputs(options)
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]imageEvidence{}
	for _, name := range backendServices {
		images[name] = imageEvidence{Ref: "umir-backend/compose-test-" + name + ":candidate"}
	}
	secrets, err := backendSecrets()
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	project := "umir-backend-0123456789abcdef01234567"
	if _, _, err := prepareBackendFixtures(temp, project, input, secrets, "http://127.0.0.1:49152"); err != nil {
		t.Fatal(err)
	}
	compose, _, err := writeBackendTopology(temp, project, input, images, secrets, "http://127.0.0.1:49152", 49152)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", append(backendComposeArgs(project, compose), "config", "--format", "json")...)
	data, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	var topology map[string]any
	if err := json.Unmarshal(data, &topology); err != nil {
		t.Fatal(err)
	}
	services := backendValue[map[string]any](t, topology["services"])
	if len(services) != 7 {
		t.Fatal("wrong version2 container count")
	}
	for _, name := range append(slices.Clone(backendServices), "smtp", "notification") {
		service := backendValue[map[string]any](t, services[name])
		if service["user"] != "65532:65532" {
			t.Fatal("wrong runtime user")
		}
		tmpfs := backendValue[[]any](t, service["tmpfs"])
		if len(tmpfs) != 1 || !strings.Contains(backendValue[string](t, tmpfs[0]), "/run/umir-fixtures:") {
			t.Fatal("private control tmpfs absent")
		}
		binds := 0
		for _, raw := range backendValue[[]any](t, service["volumes"]) {
			mount := backendValue[map[string]any](t, raw)
			if mount["type"] == "bind" {
				binds++
				if mount["read_only"] != true {
					t.Fatal("fixture bind writable")
				}
				target := backendValue[string](t, mount["target"])
				if target != "/run/umir-fixtures/config" && target != "/run/umir-fixtures/secrets" {
					t.Fatal("unexpected bind target")
				}
				host := backendValue[string](t, mount["source"])
				if err := validateBackendPrivateDirectory(host); err != nil {
					t.Fatal(err)
				}
			} else if name != "auth" && name != "feedback" {
				t.Fatal("unexpected durable mount")
			}
		}
		if binds != 2 {
			t.Fatal("credential/config directories must mount separately")
		}
		ports := backendValue[[]any](t, service["ports"])
		want := 1
		if name == "gateway" {
			want = 2
		}
		if len(ports) != want {
			t.Fatal("wrong version2 business/control mapping count")
		}
		for _, raw := range ports {
			if backendValue[map[string]any](t, raw)["host_ip"] != "127.0.0.1" {
				t.Fatal("version2 listener is not loopback-only")
			}
		}
		if slices.Contains(backendServices, name) {
			private := backendValue[map[string]any](t, ports[len(ports)-1])
			if private["target"] != float64(input.Services[name].Port) || private["published"] != "0" {
				t.Fatal("version2 private mapping differs from reviewed listener")
			}
		}
	}
	for _, secret := range secrets {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("credential in canonical Compose")
		}
	}
}
