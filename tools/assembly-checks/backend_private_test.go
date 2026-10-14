package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBackendPrivateProbeTopology(t *testing.T) {
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
	project := "umir-backend-0123456789abcdef01234567"
	if _, _, err := prepareBackendFixtures(temp, project, input, secrets, "http://127.0.0.1:49152"); err != nil {
		t.Fatal(err)
	}
	images := map[string]imageEvidence{}
	for _, name := range backendServices {
		images[name] = imageEvidence{Ref: "private-probe-" + name}
	}
	compose, _, err := writeBackendTopology(temp, project, input, images, secrets, "http://127.0.0.1:49152", 49152)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(compose)
	if err != nil {
		t.Fatal(err)
	}
	var topology map[string]any
	if err := json.Unmarshal(data, &topology); err != nil {
		t.Fatal(err)
	}
	services := backendValue[map[string]any](t, topology["services"])
	for _, name := range backendServices {
		service := backendValue[map[string]any](t, services[name])
		var ports []any
		if raw, present := service["ports"]; present {
			ports = backendValue[[]any](t, raw)
		}
		wantCount := 1
		if name == "gateway" {
			wantCount = 2
		}
		if len(ports) != wantCount {
			t.Errorf("%s: missing private business listener mapping; got %d ports want %d", name, len(ports), wantCount)
			continue
		}
		private := backendValue[map[string]any](t, ports[len(ports)-1])
		if private["host_ip"] != "127.0.0.1" || private["published"] != "0" || private["target"] != float64(input.Services[name].Port) || private["protocol"] != "tcp" {
			t.Errorf("%s: private mapping differs from reviewed listener", name)
		}
	}
}

func privateProbeDescriptor() (backendRun, backendInputs, []backendContainer) {
	run := backendRun{Version: 2, GatewayURL: "http://127.0.0.1:49152", SinkURLs: map[string]string{"smtp": "http://127.0.0.1:49158", "notification": "http://127.0.0.1:49159"}, PrivateServiceURLs: map[string]string{}, Images: map[string]imageEvidence{}}
	input := backendInputs{Version: 2, Services: map[string]backendSource{}}
	containers := []backendContainer{}
	for i, name := range backendServices {
		port := 8080
		if name == "gateway" {
			port = 8082
		}
		input.Services[name] = backendSource{Port: port}
		image := "sha256:" + strings.Repeat(fmt.Sprint(i+1), 64)
		run.Images[name] = imageEvidence{ID: image}
		run.PrivateServiceURLs[name] = fmt.Sprintf("http://127.0.0.1:%d", 49153+i)
		containers = append(containers, backendContainer{ID: name + "-owned", Service: name, State: "running", Health: "healthy", ImageID: image, Publishers: []backendPublisher{{URL: "127.0.0.1", TargetPort: port, PublishedPort: 49153 + i, Protocol: "tcp"}}})
	}
	return run, input, containers
}

func TestBackendPrivateProbeDescriptorValidation(t *testing.T) {
	invalid := []string{"", "https://127.0.0.1:49153", "http://localhost:49153", "http://0.0.0.0:49153", "http://192.0.2.1:49153", "http://[::1]:49153", "http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:-1", "http://127.0.0.1:049153", "http://127.0.0.1:49153/", "http://127.0.0.1:49153?", "http://127.0.0.1:49153?q=a", "http://127.0.0.1:49153#f", "http://user@127.0.0.1:49153", "http://127.0.0.1:49153\n", "http://127.0.0.1:49153 http://127.0.0.1:1"}
	for _, value := range invalid {
		t.Run(value, func(t *testing.T) {
			run, input, containers := privateProbeDescriptor()
			run.PrivateServiceURLs["auth"] = value
			if err := validateBackendPrivateServiceURLs(run, input, containers); err == nil {
				t.Fatal("invalid private URL accepted")
			}
		})
	}
	for _, kind := range []string{"valid", "missing", "extra", "foreign", "duplicate-url", "public-alias", "sink-alias", "wrong-mapping", "wrong-image", "missing-container", "duplicate-container", "wrong-service", "missing-id", "unready", "wrong-target", "duplicate-target", "wrong-protocol", "wildcard-binding", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			run, input, containers := privateProbeDescriptor()
			switch kind {
			case "missing":
				delete(run.PrivateServiceURLs, "auth")
			case "extra":
				run.PrivateServiceURLs["foreign"] = "http://127.0.0.1:49160"
			case "foreign":
				delete(run.PrivateServiceURLs, "auth")
				run.PrivateServiceURLs["foreign"] = "http://127.0.0.1:49160"
			case "duplicate-url":
				run.PrivateServiceURLs["auth"] = run.PrivateServiceURLs["serving"]
			case "public-alias":
				run.PrivateServiceURLs["auth"] = run.GatewayURL
			case "sink-alias":
				run.PrivateServiceURLs["auth"] = run.SinkURLs["smtp"]
			case "wrong-mapping":
				run.PrivateServiceURLs["auth"] = "http://127.0.0.1:49160"
			case "wrong-image":
				containers[0].ImageID = "foreign"
			case "missing-container":
				containers = containers[1:]
			case "duplicate-container":
				containers = append(containers, containers[0])
			case "wrong-service":
				containers[0].Service = "foreign"
			case "missing-id":
				containers[0].ID = ""
			case "unready":
				containers[0].Health = "unhealthy"
			case "wrong-target":
				containers[0].Publishers[0].TargetPort = 9000
			case "duplicate-target":
				containers[0].Publishers = append(containers[0].Publishers, containers[0].Publishers[0])
			case "wrong-protocol":
				containers[0].Publishers[0].Protocol = "udp"
			case "wildcard-binding":
				containers[0].Publishers[0].URL = "0.0.0.0"
			case "legacy":
				run.Version = 1
			}
			err := validateBackendPrivateServiceURLs(run, input, containers)
			if kind == "valid" && err != nil || kind != "valid" && err == nil {
				t.Fatalf("%s: descriptor validation mismatch: %v", kind, err)
			}
		})
	}
	for _, data := range []string{`{"version":2,"privateServiceURLs":{"auth":"a","auth":"b"}}`, `{"version":2,"privateServiceURLs":{},"privateServiceURLs":{}}`, `{"version":2,"privateServiceURL":{}}`} {
		var run backendRun
		if err := decodeBackendJSON([]byte(data), &run); err == nil {
			t.Fatal("strict descriptor accepted unknown or duplicate field")
		}
	}
}

type privateProbeExecutor struct {
	backendV2Executor
	fault      string
	descriptor backendRun
	runFile    string
	portCalls  int
}

func (e *privateProbeExecutor) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
	if filepath.Base(name) == "harness" {
		e.runFile = valueAfter(args, "--backend-run")
		data, err := os.ReadFile(e.runFile)
		if err != nil {
			return nil, nil, err
		}
		if err := decodeBackendJSON(data, &e.descriptor); err != nil {
			return nil, nil, err
		}
	}
	out, stderr, err := e.backendV2Executor.Run(ctx, dir, env, name, args...)
	if err != nil || name != "docker" || e.readyCalls != 5 {
		return out, stderr, err
	}
	if args[0] == "compose" && slices.Contains(args, "ps") {
		if e.fault == "oversize-inventory" {
			return bytes.Repeat([]byte("x"), 262145), nil, nil
		}
		var containers []backendContainer
		if err := json.Unmarshal(out, &containers); err != nil {
			return nil, nil, err
		}
		switch e.fault {
		case "missing-private":
			containers[0].Publishers = nil
		case "duplicate-target":
			containers[0].Publishers = append(containers[0].Publishers, containers[0].Publishers[0])
		case "extra-target":
			containers[0].Publishers = append(containers[0].Publishers, backendPublisher{URL: "127.0.0.1", TargetPort: 9000, PublishedPort: 49180, Protocol: "tcp"})
		case "duplicate-host-port":
			containers[0].Publishers[0].PublishedPort = containers[2].Publishers[0].PublishedPort
		case "external-binding":
			containers[0].Publishers[0].URL = "0.0.0.0"
		case "duplicate-id":
			containers[0].ID = containers[1].ID
		case "container-replaced":
			containers[0].ID = "warehouse-id"
		case "owned-container-replaced":
			if e.portCalls > 0 {
				containers[0].ID = "auth-new-id"
			}
		case "mapping-drift":
			if e.portCalls > 0 {
				containers[0].Publishers[0].PublishedPort = 49180
			}
		}
		out, err = json.Marshal(containers)
	}
	if args[0] == "inspect" && args[len(args)-1] == "auth-id" && slices.Contains([]string{"foreign-project", "foreign-image", "wrong-run-label", "wrong-inspect-id"}, e.fault) {
		var inspected map[string]any
		if err := json.Unmarshal(out, &inspected); err != nil {
			return nil, nil, err
		}
		switch e.fault {
		case "foreign-image":
			inspected["Image"] = "foreign"
		case "wrong-inspect-id":
			inspected["Id"] = "foreign"
		default:
			key := "com.docker.compose.project"
			if e.fault == "wrong-run-label" {
				key = "io.umir.backend.run"
			}
			backendValue[map[string]any](e.t, backendValue[map[string]any](e.t, inspected["Config"])["Labels"])[key] = "foreign"
		}
		out, err = json.Marshal(inspected)
	}
	if args[0] == "inspect" && args[len(args)-1] == "auth-new-id" {
		var inspected map[string]any
		if err := json.Unmarshal(out, &inspected); err != nil {
			return nil, nil, err
		}
		backendValue[map[string]any](e.t, backendValue[map[string]any](e.t, inspected["Config"])["Labels"])["com.docker.compose.service"] = "auth"
		out, err = json.Marshal(inspected)
	}
	return out, stderr, err
}

func (e *privateProbeExecutor) RunPrivate(ctx context.Context, dir string, env []string, stdin []byte, name string, args ...string) ([]byte, error) {
	if args[0] == "inspect" || args[0] == "compose" && slices.Contains(args, "ps") {
		out, _, err := e.Run(ctx, dir, env, name, args...)
		return out, err
	}
	if args[0] != "compose" || !slices.Contains(args, "port") {
		return e.backendV2Executor.RunPrivate(ctx, dir, env, stdin, name, args...)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 10*time.Second {
		e.t.Fatal("endpoint discovery is not bounded")
	}
	if args[len(args)-2] == "auth" {
		e.portCalls++
		switch e.fault {
		case "wrong-discovered-port":
			return []byte("127.0.0.1:49180\n"), nil
		case "invalid-discovery":
			return []byte("127.0.0.1:65536\n"), nil
		case "multiple-discovery":
			return []byte("127.0.0.1:49180\n127.0.0.1:49181\n"), nil
		case "oversize-discovery":
			return bytes.Repeat([]byte("x"), 129), nil
		case "discovery-error":
			return nil, errors.New("endpoint unavailable")
		}
	}
	out, _, err := e.Run(ctx, dir, env, name, args...)
	return out, err
}

func TestBackendPrivateProbeDiscoveryOwnershipAndCleanup(t *testing.T) {
	for _, fault := range []string{"", "missing-private", "duplicate-target", "extra-target", "duplicate-host-port", "external-binding", "duplicate-id", "container-replaced", "owned-container-replaced", "mapping-drift", "foreign-project", "foreign-image", "wrong-run-label", "wrong-inspect-id", "oversize-inventory", "wrong-discovered-port", "invalid-discovery", "multiple-discovery", "oversize-discovery", "discovery-error"} {
		t.Run(fault, func(t *testing.T) {
			f := newBackendV2Fixture(t)
			e := &privateProbeExecutor{backendV2Executor: backendV2Executor{backendExecutor: backendExecutor{t: t, fixture: f}}, fault: fault}
			var stdout, stderr bytes.Buffer
			code := runWithExecutor(context.Background(), append([]string{"http-acceptance"}, f.args...), &stdout, &stderr, e)
			if fault == "" {
				if code != 0 || len(e.descriptor.PrivateServiceURLs) != 5 || e.harnessCalls != 1 || e.portCalls != 1 {
					t.Fatalf("private descriptor missing or routing failed: code=%d %s", code, stderr.String())
				}
				if e.descriptor.PrivateServiceURLs["gateway"] == e.descriptor.GatewayURL {
					t.Fatal("private Gateway aliases public listener")
				}
				if _, err := os.Stat(e.runFile); !os.IsNotExist(err) {
					t.Fatal("private descriptor survived cleanup")
				}
				for _, endpoint := range e.descriptor.PrivateServiceURLs {
					if strings.Contains(stdout.String()+stderr.String(), endpoint) {
						t.Fatal("private endpoint escaped evidence allowlist")
					}
				}
			} else if code == 0 || e.harnessCalls != 0 {
				t.Fatalf("unvalidated private mapping reached harness: %d %s", code, stderr.String())
			}
			foreign := slices.Contains([]string{"foreign-project", "duplicate-id", "container-replaced", "wrong-run-label", "wrong-inspect-id", "oversize-inventory"}, fault)
			if e.down == foreign || len(e.images) != 0 || e.foreignCleanup {
				t.Fatalf("cleanup did not preserve ownership boundary: down=%v foreign=%v", e.down, foreign)
			}
		})
	}
}

func TestBackendPrivateProbeDescriptorExtension(t *testing.T) {
	data := []byte(`{"version":2,"privateServiceURLs":{"auth":"http://127.0.0.1:49153","warehouse":"http://127.0.0.1:49154","serving":"http://127.0.0.1:49155","feedback":"http://127.0.0.1:49156","gateway":"http://127.0.0.1:49157"}}`)
	var descriptor backendRun
	if err := decodeBackendJSON(data, &descriptor); err != nil {
		t.Fatalf("strict backend-run decoder rejects authorized privateServiceURLs extension: %v", err)
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(backendValue[map[string]any](t, fields["privateServiceURLs"])) != 5 {
		t.Fatal("private service endpoints lost in descriptor")
	}
}
