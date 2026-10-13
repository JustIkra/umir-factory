package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

func privateDescriptorFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile("testdata/private-descriptor.json")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d["requirements"] = []string{"BE-E2-01"}
	d["composeFile"] = filepath.Join(dir, "compose.yaml")
	d["evidenceDirectory"] = dir
	fixtures, ok := d["fixtures"].(map[string]any)
	if !ok {
		t.Fatal("fixtures")
	}
	fixtures["harnessPrivateRoot"] = filepath.Join(dir, "fixtures", "harness")
	if err := os.WriteFile(privateValue[string](t, d["composeFile"]), []byte("services: {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "backend-run.json"), d
}
func writePrivateDescriptor(t *testing.T, path string, d map[string]any) {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
func TestPrivateDescriptorAmendment(t *testing.T) {
	path, d := privateDescriptorFixture(t)
	writePrivateDescriptor(t, path, d)
	if _, err := readBackendRun(path); err != nil {
		t.Fatal("reviewed routing descriptor rejected")
	}
	delete(d, "privateServiceURLs")
	writePrivateDescriptor(t, path, d)
	if _, err := readBackendRun(path); err == nil {
		t.Fatal("old descriptor without routing accepted")
	}
}
func TestPrivateDescriptorStrictEndpoints(t *testing.T) {
	invalid := []string{"http://localhost:49200", "https://127.0.0.1:49200", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:049200", "http://user@127.0.0.1:49200", "http://127.0.0.1:49200/", "http://127.0.0.1:49200?", "http://127.0.0.1:49200#", "http://127.0.0.1:49200?q=x", "http://127.0.0.1:49200#x"}
	for i, v := range invalid {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			path, d := privateDescriptorFixture(t)
			urls := privateValue[map[string]any](t, d["privateServiceURLs"])
			urls["auth"] = v
			writePrivateDescriptor(t, path, d)
			if _, err := readBackendRun(path); err == nil {
				t.Fatal("noncanonical endpoint accepted")
			}
		})
	}
	for _, mutate := range []func(map[string]any){func(d map[string]any) {
		privateValue[map[string]any](t, d["privateServiceURLs"])["extra"] = "http://127.0.0.1:49999"
	}, func(d map[string]any) { delete(privateValue[map[string]any](t, d["privateServiceURLs"]), "auth") }, func(d map[string]any) {
		privateValue[map[string]any](t, d["privateServiceURLs"])["auth"] = d["gatewayURL"]
	}, func(d map[string]any) {
		privateValue[map[string]any](t, d["privateServiceURLs"])["auth"] = privateValue[map[string]any](t, d["sinkURLs"])["smtp"]
	}, func(d map[string]any) {
		u := privateValue[map[string]any](t, d["privateServiceURLs"])
		u["auth"] = u["warehouse"]
	}} {
		path, d := privateDescriptorFixture(t)
		mutate(d)
		writePrivateDescriptor(t, path, d)
		if _, err := readBackendRun(path); err == nil {
			t.Fatal("invalid map accepted")
		}
	}
	path, d := privateDescriptorFixture(t)
	writePrivateDescriptor(t, path, d)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{string(raw) + " {}", strings.Replace(string(raw), `"auth":"http:`, `"auth":"http://127.0.0.1:49999","auth":"http:`, 1), strings.TrimSuffix(string(raw), "}") + `,"unknown":true}`} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBackendRun(path); err == nil {
			t.Fatal("nonstrict JSON accepted")
		}
	}
}

func attachPrivateRig(t *testing.T, rig *backendInfrastructureRig, target string) {
	t.Helper()
	c := rig.client
	c.run.GatewayURL = "http://127.0.0.1:49152"
	c.run.Origin = c.run.GatewayURL
	c.run.SinkURLs = map[string]string{"smtp": "http://127.0.0.1:49250", "notification": "http://127.0.0.1:49260"}
	c.run.PrivateServiceURLs = map[string]string{"auth": "http://127.0.0.1:49200", "gateway": "http://127.0.0.1:49211", "warehouse": "http://127.0.0.1:49220", "serving": "http://127.0.0.1:49230", "feedback": "http://127.0.0.1:49240"}
	ids := map[string]string{}
	rows := []map[string]string{}
	for i, name := range backendTargets {
		id := fmt.Sprintf("%064x", i+1)
		if name == target {
			id = strings.Repeat("c", 64)
		}
		ids[name] = id
		if _, ok := c.run.Images[name]; !ok {
			c.run.Images[name] = backendImage{ID: "sha256:" + strings.Repeat("b", 64)}
		}
		rows = append(rows, map[string]string{"ID": id, "Service": name, "imageID": c.run.Images[name].ID})
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.run.EvidenceDirectory = dir
	raw, err := json.Marshal(map[string]any{"project": c.run.Project, "containers": rows})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.run.Fixtures.HarnessPrivateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	previous := c.execute
	c.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " ps ") && args[len(args)-1] == "--quiet" {
			values := []string{}
			for _, name := range backendTargets {
				values = append(values, ids[name])
			}
			return []byte(strings.Join(values, "\n")), nil
		}
		requested := target
		if len(args) > 0 && args[0] == "container" {
			for name, id := range ids {
				if args[len(args)-1] == id {
					requested = name
				}
			}
		}
		if strings.Contains(joined, " ps ") && slices.Contains(backendTargets, args[len(args)-1]) && args[len(args)-1] != target {
			return []byte(ids[args[len(args)-1]]), nil
		}
		if requested != target && len(args) > 0 && args[0] == "container" && !strings.Contains(joined, ".NetworkSettings.Ports") {
			return json.Marshal(map[string]any{"id": ids[requested], "image": c.run.Images[requested].ID, "running": true, "project": c.run.Project, "service": requested, "run": c.run.Project})
		}
		endpoints := map[int]string{}
		switch requested {
		case "smtp":
			endpoints[8025] = c.run.SinkURLs[requested]
		case "notification":
			endpoints[8026] = c.run.SinkURLs[requested]
		default:
			endpoints[backendPrivatePort(requested)] = c.run.PrivateServiceURLs[requested]
		}
		if requested == "gateway" {
			endpoints[8080] = c.run.GatewayURL
		}
		if strings.Contains(joined, " port ") {
			port, err := strconv.Atoi(args[len(args)-1])
			if err != nil {
				return nil, err
			}
			return []byte(strings.TrimPrefix(endpoints[port], "http://") + "\n"), nil
		}
		if strings.Contains(joined, ".NetworkSettings.Ports") {
			ports := map[string][]backendPortBinding{}
			for port, endpoint := range endpoints {
				u, err := url.Parse(endpoint)
				if err != nil {
					return nil, err
				}
				ports[strconv.Itoa(port)+"/tcp"] = []backendPortBinding{{HostIP: u.Hostname(), HostPort: u.Port()}}
			}
			return json.Marshal(backendProbeInspection{ID: ids[requested], Image: c.run.Images[requested].ID, Project: c.run.Project, Service: requested, Run: c.run.Project, Running: requested != target || rig.running, Health: "healthy", Ports: ports})
		}
		return previous(ctx, input, args...)
	}
}

func TestPrivateQualificationFreshAndFailClosed(t *testing.T) {
	for _, failure := range []string{"none", "stale", "replaced", "foreign", "image", "unhealthy", "nonloopback", "extra", "duplicate", "missing", "wrong-target", "endpoint-oversize", "subprocess-oversize", "cancel", "new-instance"} {
		t.Run(failure, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "warehouse")
			c := rig.client
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			if err := c.qualifyProbe(ctx, "warehouse"); err != nil {
				t.Fatal(err)
			}
			execute := c.execute
			lookups := 0
			c.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
				raw, err := execute(ctx, input, args...)
				joined := strings.Join(args, " ")
				if strings.Contains(joined, " port ") {
					lookups++
					if failure == "stale" {
						return []byte("127.0.0.1:49999"), nil
					}
					if failure == "endpoint-oversize" {
						return []byte(strings.Repeat("x", 129)), nil
					}
					if failure == "cancel" {
						cancel()
					}
				}
				if strings.Contains(joined, ".NetworkSettings.Ports") {
					if failure == "subprocess-oversize" {
						return []byte(strings.Repeat("x", 262145)), nil
					}
					var info backendProbeInspection
					if err != nil || json.Unmarshal(raw, &info) != nil {
						return nil, errors.New("test inspection unavailable")
					}
					switch failure {
					case "replaced":
						info.ID = strings.Repeat("d", 64)
					case "foreign":
						info.Project = "foreign"
					case "image":
						info.Image = "sha256:" + strings.Repeat("d", 64)
					case "unhealthy":
						info.Health = "starting"
					case "nonloopback":
						info.Ports["8080/tcp"][0].HostIP = "0.0.0.0"
					case "extra":
						info.Ports["9090/tcp"] = info.Ports["8080/tcp"]
					case "duplicate":
						info.Ports["8080/tcp"] = append(info.Ports["8080/tcp"], info.Ports["8080/tcp"][0])
					case "missing":
						delete(info.Ports, "8080/tcp")
					case "wrong-target":
						info.Ports["8082/tcp"] = info.Ports["8080/tcp"]
						delete(info.Ports, "8080/tcp")
					}
					return json.Marshal(info)
				}
				return raw, err
			}
			if failure == "new-instance" {
				rig.clock.InstanceID = "33333333-3333-4333-8333-333333333333"
			}
			err := c.qualifyProbe(ctx, "warehouse")
			if failure == "none" {
				if err != nil || lookups < 2 {
					t.Fatal("fresh lookup missing")
				}
			} else if err == nil {
				t.Fatal("unsafe qualification accepted")
			}
			if failure == "new-instance" && c.qualifyProbe(ctx, "warehouse") == nil {
				t.Fatal("replacement silently accepted on second call")
			}
		})
	}
}
func TestPrivateInitialEvidenceBindings(t *testing.T) {
	for _, failure := range []string{"project", "ID", "imageID", "service", "duplicate", "missing", "trailing", "unsafe-mode"} {
		t.Run(failure, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "warehouse")
			c := rig.client
			path := filepath.Join(c.run.EvidenceDirectory, "evidence.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var ev map[string]any
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatal(err)
			}
			rows := privateValue[[]any](t, ev["containers"])
			row := privateValue[map[string]any](t, rows[0])
			switch failure {
			case "project":
				ev["project"] = "foreign"
			case "ID":
				row["ID"] = strings.Repeat("d", 64)
			case "imageID":
				row["imageID"] = "sha256:" + strings.Repeat("d", 64)
			case "service":
				row["Service"] = "foreign"
			case "duplicate":
				ev["containers"] = append(rows[:6], rows[0])
			case "missing":
				ev["containers"] = rows[:6]
			}
			raw, err = json.Marshal(ev)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "trailing" {
				raw = append(raw, []byte(" {}")...)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if failure == "unsafe-mode" {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			if c.qualifyProbe(ctx, "warehouse") == nil {
				t.Fatal("bad captured binding accepted")
			}
		})
	}
}
func privateServerRig(t *testing.T, target string, handler http.HandlerFunc) (*backendInfrastructureRig, *httptest.Server) {
	t.Helper()
	rig := newBackendInfrastructureRig(t, target)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	rig.client.run.PrivateServiceURLs[target] = server.URL
	name := "gateway-" + target
	if target == "gateway" {
		name = "auth-gateway"
	}
	rig.client.run.CredentialFiles = map[string]faultproxy.FixtureFileRef{name: {Target: "harness", Path: "/run/umir-fixtures/secrets/" + name}}
	dir := filepath.Join(rig.client.run.Fixtures.HarnessPrivateRoot, "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	return rig, server
}
func TestPrivateRequestCredentialsSchemasAndAuthFlow(t *testing.T) {
	calls := 0
	rig, _ := privateServerRig(t, "auth", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Umir-Service-Key") != strings.Repeat("a", 64) {
			t.Error("scoped key missing")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var payload map[string]any
		if len(raw) > 0 && json.Unmarshal(raw, &payload) != nil {
			t.Error("body unavailable")
		}
		var reply string
		switch r.URL.Path {
		case "/internal/auth/v1/public/csrf":
			reply = `{"csrfToken":"private-csrf","preauthToken":"private-preauth","expiresAt":"2026-10-13T10:05:00+03:00"}`
		case "/internal/auth/v1/public/session":
			if r.Header.Get("X-Umir-Preauth") != "private-preauth" || r.Header.Get("X-Umir-CSRF-Token") != "private-csrf" || payload["password"] != "synthetic-password" {
				t.Error("real session flow omitted")
			}
			reply = `{"view":{"user":{"userID":"11111111-1111-4111-8111-111111111111","login":"e2e.initial","displayName":"Fixture","credentialVersion":"1"},"session":{"expiresAt":"2026-10-13T10:05:00+03:00","idleExpiresAt":"2026-10-13T10:05:00+03:00","absoluteExpiresAt":"2026-10-14T10:00:00+03:00","rememberMe":false,"csrfToken":"private-csrf"}},"sessionToken":"real-authority-session"}`
		case "/internal/auth/v1/authorize":
			if payload["sessionToken"] != "real-authority-session" || payload["restaurantID"] != backendOtherID || payload["activity"] != "background" {
				t.Error("session scope lost")
			}
			reply = `{"userID":"11111111-1111-4111-8111-111111111111","sessionID":"22222222-2222-4222-8222-222222222222","sessionVersion":"1","credentialVersion":"1","accessVersion":"1","orgID":"33333333-3333-4333-8333-333333333333","tenantID":"22222222-2222-4222-8222-222222222222","permissions":["stock.read"],"settings":{"timeZone":"Europe/Moscow","settingsVersion":"1"},"lifecycleVersion":"1","token":"issued-only-by-authority","expiresAt":"2026-10-13T10:01:00+03:00","session":{"expiresAt":"2026-10-13T10:05:00+03:00","idleExpiresAt":"2026-10-13T10:05:00+03:00","absoluteExpiresAt":"2026-10-14T10:00:00+03:00","rememberMe":false,"csrfToken":"private-csrf"}}`
		default:
			t.Error("unreviewed route")
		}
		if _, err := w.Write([]byte(reply)); err != nil {
			t.Error(err)
		}
	})
	c := rig.client
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	session, err := c.privateAuthLogin(ctx, "e2e.initial", []byte("synthetic-password"), "fixture-login")
	if err != nil {
		t.Fatal(err)
	}
	token, err := c.scopedAuthToken(ctx, session, backendOtherID, "inventory", []string{"umir-warehouse"})
	if err != nil || token.value != "issued-only-by-authority" {
		t.Fatal("authority flow rejected", err)
	}
	if calls != 3 {
		t.Fatal("unexpected token request count")
	}
	if _, err := c.privateCall(ctx, "auth", "jwks", nil, nil, nil, backendPrivateCredential{Name: "gateway-warehouse"}, 200); err == nil || calls != 3 {
		t.Fatal("cross-target credential allowed")
	}
	if _, err := c.privateReject(ctx, "auth", "authorize", nil, nil, []byte(`{`), nil, backendPrivateCredential{Name: "gateway-auth"}, 200); err == nil || calls != 3 {
		t.Fatal("malformed input allowed for success")
	}
}
func TestPrivateHTTPBudgetsRedirectsAndRejection(t *testing.T) {
	for _, failure := range []string{"redirect", "oversize", "malformed-response", "cancel", "rejection", "foreign-owner", "malformed-success", "oversize-request", "bad-header"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			rig, _ := privateServerRig(t, "auth", func(w http.ResponseWriter, r *http.Request) {
				calls++
				if failure == "redirect" {
					http.Redirect(w, r, "http://example.invalid/private", http.StatusFound)
					return
				}
				if failure == "oversize" {
					if _, err := w.Write([]byte(strings.Repeat("x", (1<<20)+1))); err != nil {
						return
					}
					return
				}
				if failure == "rejection" {
					if r.Header.Get("Authorization") != "Bearer malformed-token" {
						t.Error("rejection token missing")
					}
					w.WriteHeader(401)
					if _, err := w.Write([]byte(`{"code":"Unauthenticated","outcome":"not_committed","message":"Denied","retryable":false}`)); err != nil {
						t.Error(err)
					}
					return
				}
				if _, err := w.Write([]byte(`{"token":"private","extra":"invalid"}`)); err != nil {
					t.Error(err)
				}
			})
			c := rig.client
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			if failure == "cancel" {
				cancel()
			}
			cred := backendPrivateCredential{Name: "gateway-auth"}
			payload := []byte(`{`)
			target := "auth"
			status := 401
			headers := http.Header{"Authorization": []string{"Bearer malformed-token"}}
			if failure == "foreign-owner" {
				target = "warehouse"
			}
			if failure == "malformed-success" {
				status = 200
			}
			if failure == "oversize-request" {
				payload = []byte(strings.Repeat("x", (1<<20)+1))
			}
			if failure == "bad-header" {
				headers.Set("X-Extra", strings.Repeat("x", 16385))
			}
			reply, err := c.privateReject(ctx, target, "authorize", nil, nil, payload, headers, cred, status)
			if failure == "rejection" {
				if err != nil || reply.Status != 401 || calls != 1 {
					t.Fatal("typed rejection failed", err)
				}
			} else if err == nil {
				t.Fatal("unsafe response passed")
			}
			if failure == "cancel" || failure == "foreign-owner" || failure == "malformed-success" || failure == "oversize-request" || failure == "bad-header" {
				if calls != 0 {
					t.Fatal("invalid probe sent")
				}
			}
		})
	}
}

func TestPrivateQualifiedRecreationUpdatesIdentityMappingAtomically(t *testing.T) {
	rig := newBackendInfrastructureRig(t, "warehouse")
	c := rig.client
	prior := c.execute
	changed := false
	mutations := 0
	oldID := strings.Repeat("c", 64)
	newID := strings.Repeat("d", 64)
	newEndpoint := "http://127.0.0.1:49998"
	c.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " up ") {
			mutations++
			changed = true
			return prior(ctx, input, c.compose("start", "warehouse")...)
		}
		raw, err := prior(ctx, input, args...)
		if !changed {
			return raw, err
		}
		if strings.Contains(joined, " ps ") {
			return []byte(strings.ReplaceAll(string(raw), oldID, newID)), err
		}
		if strings.Contains(joined, " port ") {
			return []byte(strings.TrimPrefix(newEndpoint, "http://") + "\n"), nil
		}
		if len(args) > 0 && args[0] == "container" && !strings.Contains(joined, ".Mounts") && (args[len(args)-1] == oldID || args[len(args)-1] == newID) {
			var info map[string]any
			if err != nil || json.Unmarshal(raw, &info) != nil {
				return nil, errors.New("test inspect unavailable")
			}
			if _, ok := info["ID"]; ok {
				info["ID"] = newID
			} else {
				info["id"] = newID
			}
			if strings.Contains(joined, ".NetworkSettings.Ports") {
				info["Ports"] = map[string][]backendPortBinding{"8080/tcp": {{HostIP: "127.0.0.1", HostPort: "49998"}}}
			}
			return json.Marshal(info)
		}
		return raw, err
	}
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	saved := rig.clock
	after, err := c.recreate(ctx, "warehouse")
	if err != nil {
		t.Fatal(err)
	}
	if validateBackendRestartClock(saved, after) != nil || mutations != 1 || c.containers["warehouse"] != newID || c.run.PrivateServiceURLs["warehouse"] != newEndpoint {
		t.Fatal("recreation qualification incomplete")
	}
	raw, err := faultproxy.ReadPrivateFile(filepath.Join(c.run.Fixtures.HarnessPrivateRoot, "private-probe-state.json"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Run           backendRunDescriptor
		Containers    map[string]string
		Before, After fixtureClock
	}
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Containers["warehouse"] != newID || snapshot.Run.PrivateServiceURLs["warehouse"] != newEndpoint || !reflect.DeepEqual(snapshot.Before, saved) || snapshot.After.InstanceID != after.InstanceID {
		t.Fatal("atomic metadata/evidence lost")
	}
	if err := c.qualifyProbe(ctx, "warehouse"); err != nil {
		t.Fatal("new probes rejected qualified recreation", err)
	}
}
func TestPrivateRestartQualificationFailureBlocksFurtherProbes(t *testing.T) {
	for _, failure := range []string{"mapping", "metadata", "mutation"} {
		t.Run(failure, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "warehouse")
			c := rig.client
			execute := c.execute
			started := false
			starts := 0
			if failure == "metadata" {
				path := filepath.Join(c.run.Fixtures.HarnessPrivateRoot, "private-probe-state.json")
				if err := os.Symlink(rig.configs, path); err != nil {
					t.Fatal(err)
				}
			}
			c.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, " start ") {
					started = true
					starts++
					if failure == "mutation" {
						return nil, errors.New("unknown mutation outcome")
					}
				}
				if started && failure == "mapping" && strings.Contains(joined, " port ") {
					return []byte("0.0.0.0:49998"), nil
				}
				return execute(ctx, input, args...)
			}
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			if _, err := c.restart(ctx, "warehouse"); err == nil {
				t.Fatal("unqualified restart passed")
			}
			if starts != 1 || len(c.restarts) != 0 || c.qualifyProbe(ctx, "warehouse") == nil {
				t.Fatal("unsafe restart retry or new probe allowed")
			}
		})
	}
}
func TestPrivateEndpointTransportCannotConnectOutsideQualification(t *testing.T) {
	transport := backendProbeTransport("127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := transport.DialContext(ctx, "tcp", "example.invalid:80"); err == nil {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
		t.Fatal("outside connection permitted")
	}
}

func privateValue[T any](t *testing.T, v any) T {
	t.Helper()
	value, ok := v.(T)
	if !ok {
		t.Fatal("test value type mismatch")
	}
	return value
}

func TestPrivateFrozenOperationRoutesUseConcreteListener(t *testing.T) {
	wire, err := backendPrivateWire.ReadFile("backend-private.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage
	}
	if err := json.Unmarshal(wire, &doc); err != nil {
		t.Fatal(err)
	}
	count := 0
	for path, methods := range doc.Paths {
		for _, raw := range methods {
			var op struct {
				ID string `json:"operationId"`
			}
			if json.Unmarshal(raw, &op) != nil || op.ID == "" {
				continue
			}
			_, _, target, err := privateOperation(op.ID)
			if err != nil || target != strings.Split(path, "/")[2] {
				t.Fatal("operation routes to nonconcrete owner", op.ID)
			}
			count++
		}
	}
	if count != 80 {
		t.Fatal("frozen private operation count changed", count)
	}
}

func TestPrivateScopedCredentialAndIssuedTokenReachOwner(t *testing.T) {
	calls := 0
	rig, _ := privateServerRig(t, "warehouse", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Umir-Service-Key") != strings.Repeat("a", 64) || r.Header.Get("Authorization") != "Bearer authority-test-double-token" || r.URL.Path != "/internal/warehouse/v2/runtime" {
			t.Error("target credentials lost")
		}
		if _, err := w.Write([]byte(`{"service":"warehouse","epoch":"22222222-2222-4222-8222-222222222222","ready":true,"tzdataVersion":"test-double"}`)); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	c := rig.client
	reply, err := c.privateCall(ctx, "warehouse", "warehouseRuntime", nil, nil, nil, backendPrivateCredential{Name: "gateway-warehouse", Token: backendAuthToken{value: "authority-test-double-token"}}, 200)
	if err != nil || calls != 1 || reply.Status != 200 || len(c.privateObservations) != 1 || !c.privateObservations[0].ResponseValid {
		t.Fatal("private owner forwarding failed", err)
	}
}
func TestPrivateAuthServiceAndAnonymousTokensUseRealInterfaces(t *testing.T) {
	calls := 0
	rig, _ := privateServerRig(t, "auth", func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/internal/auth/v1/service-tokens":
			if payload["restaurantID"] != backendTestID || payload["epoch"] != backendOtherID || payload["operation"] != "expiry.due" {
				t.Error("service scope lost")
			}
		case "/internal/auth/v1/anonymous-feedback-token":
			if payload["operation"] != "feedback.anonymous.submit" {
				t.Error("anonymous grant invented")
			}
		default:
			t.Error("unreviewed token route")
		}
		if _, err := w.Write([]byte(`{"token":"authority-test-double-token","expiresAt":"2026-10-13T10:01:00+03:00"}`)); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	if token, err := rig.client.serviceAuthToken(ctx, backendTestID, "expiry.due", backendOtherID); err != nil || token.value == "" {
		t.Fatal(err)
	}
	if token, err := rig.client.anonymousAuthToken(ctx); err != nil || token.value == "" {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("unexpected authority request count")
	}
}
func TestPrivateSerializedProbeHonorsCancellation(t *testing.T) {
	c := newBackendClient(backendRunDescriptor{})
	release, err := c.lockPrivate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.lockPrivate(ctx); err == nil {
		t.Fatal("cancelled lock wait accepted")
	}
}

func TestPrivateReplayPreservesExactBytesAndDoesNotRetryMutation(t *testing.T) {
	received := []string{}
	rig, _ := privateServerRig(t, "auth", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		received = append(received, string(raw))
		w.WriteHeader(503)
		if _, err := w.Write([]byte(`{"code":"AuthUnavailable","outcome":"unknown","message":"Unavailable","retryable":false}`)); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	raw := []byte(`{ "operation": "expiry.due", "epoch": "22222222-2222-4222-8222-222222222222", "restaurantID": "11111111-1111-4111-8111-111111111111" }`)
	for i := 1; i <= 2; i++ {
		if _, err := rig.client.privateReplay(ctx, "auth", "serviceToken", nil, nil, raw, backendPrivateCredential{Name: "gateway-auth"}, 503); err != nil {
			t.Fatal(err)
		}
		if len(received) != i {
			t.Fatal("mutation was retried")
		}
	}
	if received[0] != string(raw) || received[1] != received[0] {
		t.Fatal("retained bytes changed")
	}
}

func TestPrivateEmptySuccessRequiresDocumentedNoBodyStatus(t *testing.T) {
	rig, _ := privateServerRig(t, "auth", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	if _, err := rig.client.privateCall(ctx, "auth", "jwks", nil, nil, nil, backendPrivateCredential{Name: "gateway-auth"}, 204); err == nil {
		t.Fatal("undocumented empty success passed strict schema")
	}
	if _, err := rig.client.privateCall(ctx, "auth", "authFacadelogout", nil, nil, nil, backendPrivateCredential{Name: "gateway-auth", Headers: http.Header{"X-Umir-Session": []string{"authority-test-double-session"}}}, 204); err != nil {
		t.Fatal("documented no-body response rejected", err)
	}
}
