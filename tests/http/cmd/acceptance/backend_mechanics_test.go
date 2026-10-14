package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

type backendInfrastructureRig struct {
	client         *backendClient
	clock          fixtureClock
	running        bool
	trace          []string
	requests       []fixtureRequest
	replies        map[string][]byte
	configs        string
	projection     map[string]any
	tickComplete   bool
	faultOutcome   string
	configSource   string
	configWritable bool
}

func newBackendInfrastructureRig(t *testing.T, target string) *backendInfrastructureRig {
	t.Helper()
	raw, err := os.ReadFile("../../internal/faultproxy/protocol-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var examples struct {
		Positive []struct {
			Schema string
			Value  json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &examples); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	for _, ex := range examples.Positive {
		if ex.Schema == "#/$defs/OwnerConfig" {
			if err := json.Unmarshal(ex.Value, &config); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	config["owner"] = target
	switch target {
	case "auth":
		config["stateFile"] = "/var/lib/umir-auth/auth.json"
	case "feedback":
		config["stateFile"] = "/var/lib/umir-feedback/feedback.jsonl"
	default:
		config["stateFile"] = ""
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(tempRoot, "fixtures")
	path := filepath.Join(root, target, "config", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := []byte("synthetic-control-binary")
	owner := fixtureOwner{ConfigSHA256: faultproxy.SHA256(encoded), ControlBinarySHA256: faultproxy.SHA256(binary), ServerBinarySHA256: strings.Repeat("a", 64)}
	run := backendRunDescriptor{Project: text(config["project"]), ComposeFile: filepath.Join(root, "compose.yaml"), Images: map[string]backendImage{target: {ID: "sha256:" + strings.Repeat("b", 64)}}, Fixtures: fixtureDescriptor{HarnessPrivateRoot: filepath.Join(root, "harness"), FixtureID: text(config["fixtureID"]), Owners: map[string]fixtureOwner{target: owner}}}
	rig := &backendInfrastructureRig{client: newBackendClient(run), clock: fixtureClock{Owner: target, InstanceID: backendTestID, FixtureID: run.Fixtures.FixtureID, Source: "owner-injected", Generation: "3", Instant: "2026-10-13T10:00:00+03:00", InjectedConsumers: []string{"RealOwnerBindings"}, Healthy: true}, running: true, replies: map[string][]byte{}, configs: path, tickComplete: true, faultOutcome: "unknown"}
	rig.configSource = filepath.Dir(path)
	rig.client.execute = func(_ context.Context, input []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		rig.trace = append(rig.trace, joined)
		switch {
		case strings.Contains(joined, " ps "):
			return []byte(strings.Repeat("c", 64)), nil
		case len(args) > 2 && args[0] == "container" && strings.Contains(joined, ".Mounts"):
			mounts := []map[string]any{{"Type": "bind", "Source": rig.configSource, "Destination": "/run/umir-fixtures/config", "RW": rig.configWritable}}
			if target == "auth" || target == "feedback" {
				mounts = append(mounts, map[string]any{"Type": "volume", "Name": run.Project + "_" + target + "-state", "Destination": "/var/lib/umir-" + target, "RW": true})
			}
			return json.Marshal(mounts)
		case len(args) > 2 && args[0] == "container":
			return json.Marshal(map[string]any{"id": strings.Repeat("c", 64), "image": run.Images[target].ID, "running": rig.running, "project": run.Project, "service": target, "run": run.Project})
		case len(args) > 0 && args[0] == "cp":
			if len(args) != 3 || !strings.HasSuffix(args[1], ":/fixture-control") {
				return nil, errors.New("unexpected private copy")
			}
			var b bytes.Buffer
			tw := tar.NewWriter(&b)
			if err := tw.WriteHeader(&tar.Header{Name: "fixture-control", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
				return nil, err
			}
			if _, err := tw.Write(binary); err != nil {
				return nil, err
			}
			if err := tw.Close(); err != nil {
				return nil, err
			}
			return b.Bytes(), nil
		case strings.Contains(joined, " stop "):
			rig.running = false
			return nil, nil
		case strings.Contains(joined, " start "):
			updated, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var config map[string]any
			if err := json.Unmarshal(updated, &config); err != nil {
				return nil, err
			}
			rig.clock.Instant = text(config["initialInstant"])
			rig.clock.Generation = text(config["clockGeneration"])
			rig.clock.InstanceID = "33333333-3333-4333-8333-333333333333"
			rig.running = true
			rig.replies = map[string][]byte{}
			return nil, nil
		case strings.Contains(joined, " exec "):
			var request fixtureRequest
			if err := json.Unmarshal(input, &request); err != nil {
				return nil, err
			}
			rig.requests = append(rig.requests, request)
			if reply, ok := rig.replies[request.RequestID]; ok {
				return slices.Clone(reply), nil
			}
			data := map[string]any{}
			clockRaw, err := json.Marshal(rig.clock)
			if err != nil {
				return nil, err
			}
			parsed, err := faultproxy.StrictJSON(clockRaw, 16)
			if err != nil {
				return nil, err
			}
			switch request.Operation {
			case "fixture.ready":
				data = map[string]any{"target": target, "instanceID": rig.clock.InstanceID, "protocol": faultproxy.FixtureProtocol, "binarySHA256": owner.ServerBinarySHA256, "configSHA256": rig.client.run.Fixtures.Owners[target].ConfigSHA256, "ready": true, "capabilities": []string{"fixture.ready", "clock.read", "clock.checkpoint", "feedback.observe", "scheduler.tick", "owner.fault.arm", "owner.fault.read", "owner.fault.clear"}}
			case "clock.read":
				clock, ok := parsed.(map[string]any)
				if !ok {
					return nil, errors.New("clock shape")
				}
				data = clock
			case "clock.checkpoint":
				canonical, err := json.Marshal(parsed)
				if err != nil {
					return nil, err
				}
				data = map[string]any{"clock": parsed, "checkpointSHA256": faultproxy.SHA256(canonical)}
			case "feedback.observe":
				for k, v := range rig.projection {
					data[k] = v
				}
				data["instanceID"] = rig.clock.InstanceID
			case "scheduler.tick":
				data = map[string]any{"tickID": request.Data["tickID"], "clock": parsed, "completed": rig.tickComplete}
			case "owner.fault.arm":
				data = map[string]any{"faultID": request.Data["faultID"], "state": "armed"}
			case "owner.fault.read":
				data = map[string]any{"faultID": request.Data["faultID"], "state": "hit", "hits": 1, "ownerOutcome": rig.faultOutcome}
			case "owner.fault.clear":
				data = map[string]any{"faultID": request.Data["faultID"], "state": "cleared"}
			default:
				return nil, errors.New("unsupported infrastructure test operation")
			}
			reply, err := json.Marshal(map[string]any{"version": 1, "project": request.Project, "requestID": request.RequestID, "operation": request.Operation, "data": data})
			if err != nil {
				return nil, err
			}
			rig.replies[request.RequestID] = slices.Clone(reply)
			return reply, nil
		default:
			return nil, errors.New("unexpected Docker mechanics")
		}
	}
	attachPrivateRig(t, rig, target)
	return rig
}
func TestRestartInfrastructurePreservesVolumesConfigAndCheckpoint(t *testing.T) {
	for _, target := range []string{"auth", "feedback", "warehouse", "serving"} {
		t.Run(target, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, target)
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			if _, err := rig.client.control(ctx, target, "fixture.ready", map[string]any{}); err != nil {
				t.Fatal(err)
			}
			saved := rig.clock
			after, err := rig.client.restart(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateBackendRestartClock(saved, after); err != nil {
				t.Fatal(err)
			}
			if len(rig.client.restarts) != 1 || !rig.client.restarts[0].VolumesPreserved || rig.client.restarts[0].MemoryOnly != (target == "warehouse" || target == "serving") {
				t.Fatal("restart proof absent")
			}
			for _, call := range rig.trace {
				if strings.Contains(call, " down") || strings.Contains(call, " rm") || strings.Contains(call, " volume ") {
					t.Fatal("destructive restart")
				}
			}
		})
	}
}
func TestFeedbackFreshReadAndControlReplayUseDifferentCorrelations(t *testing.T) {
	rig := newBackendInfrastructureRig(t, "feedback")
	raw, err := os.ReadFile("testdata/feedback-observations.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct{ Positive []feedbackExample }
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	var reply struct{ Data map[string]any }
	if err := json.Unmarshal(corpus.Positive[0].Response, &reply); err != nil {
		t.Fatal(err)
	}
	rig.projection = reply.Data
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	if _, err := rig.client.control(ctx, "feedback", "fixture.ready", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	trusted := corpus.Positive[0].Options.TrustedSubmissions
	before, err := rig.client.observeFeedback(ctx, trusted, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := rig.client.replayFeedbackControl(ctx, before, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before.Response, replay.Response) {
		t.Fatal("cache reply changed")
	}
	fresh, err := rig.client.observeFeedback(ctx, trusted, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := compareFeedbackObservations(before.Request, before.Response, fresh.Request, fresh.Response, trusted, before.Clock.InstanceID, fresh.Clock.InstanceID, "business-replay"); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.client.restart(ctx, "feedback"); err != nil {
		t.Fatal(err)
	}
	reopened, err := rig.client.observeFeedback(ctx, trusted, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := compareFeedbackObservations(before.Request, before.Response, reopened.Request, reopened.Response, trusted, before.Clock.InstanceID, reopened.Clock.InstanceID, "reopen"); err != nil {
		t.Fatal(err)
	}
}
func TestSchedulerAndFaultHelpersRequireActualCompletedFacts(t *testing.T) {
	rig := newBackendInfrastructureRig(t, "gateway")
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	if _, err := rig.client.control(ctx, "gateway", "fixture.ready", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.client.schedulerTick(ctx); err != nil {
		t.Fatal(err)
	}
	rig.tickComplete = false
	if _, err := rig.client.schedulerTick(ctx); err == nil {
		t.Fatal("unfinished tick passed")
	}
	rig = newBackendInfrastructureRig(t, "auth")
	if _, err := rig.client.control(ctx, "auth", "fixture.ready", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	id, err := rig.client.armOwnerFault(ctx, "auth", "after-real-commit-return-unknown", "createOrganization", "original-business-command", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	state, err := rig.client.faultState(ctx, "auth", id)
	if err != nil || state["ownerOutcome"] != "unknown" {
		t.Fatal("unknown outcome lost")
	}
	if err := rig.client.clearFault(ctx, "auth", id); err != nil {
		t.Fatal(err)
	}
	var arm fixtureRequest
	for _, r := range rig.requests {
		if r.Operation == "owner.fault.arm" {
			arm = r
		}
	}
	if arm.Data["commandID"] != "original-business-command" || arm.Data["count"] != float64(1) {
		t.Fatal("fault lost command binding")
	}
	unsupported := slices.Clone(rig.client.capabilities["auth"])
	rig.client.capabilities["auth"] = []string{"fixture.ready"}
	calls := len(rig.trace)
	if _, err := rig.client.armOwnerFault(ctx, "auth", "before-commit", "createOrganization", "saved-command", time.Second); err == nil {
		t.Fatal("unavailable capability accepted")
	}
	if len(rig.trace) != calls {
		t.Fatal("unsupported operation executed")
	}
	if rig.client.observations[len(rig.client.observations)-1].ResponseValid {
		t.Fatal("local capability refusal fabricated an owner reply")
	}
	rig.client.capabilities["auth"] = unsupported
}
func TestObservationMissingRowsNeverAuthorizeBusinessRetry(t *testing.T) {
	rig := newBackendInfrastructureRig(t, "feedback")
	rig.projection = map[string]any{"snapshotID": backendOtherID, "snapshotSHA256": faultproxy.SHA256([]byte("[]")), "recovered": true, "complete": false, "records": []any{}}
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	if _, err := rig.client.control(ctx, "feedback", "fixture.ready", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	pairs := []feedbackSubmission{{Namespace: feedbackNamespace{Kind: "anonymous"}, SubmissionID: backendOtherID}}
	before, err := rig.client.observeFeedback(ctx, pairs, 0)
	if err != nil || before.Projection.Complete {
		t.Fatal("missing rows fabricated")
	}
	cached, err := rig.client.replayFeedbackControl(ctx, before, pairs)
	if err != nil || !reflect.DeepEqual(before.Projection, cached.Projection) {
		t.Fatal("incomplete cache reply replaced")
	}
	if err := compareFeedbackObservations(before.Request, before.Response, cached.Request, cached.Response, pairs, before.Clock.InstanceID, cached.Clock.InstanceID, "business-replay"); err == nil {
		t.Fatal("missing comparison passed")
	}
	for _, r := range rig.requests {
		if !slices.Contains([]string{"fixture.ready", "clock.read", "feedback.observe"}, r.Operation) {
			t.Fatal("observation created side effects")
		}
	}
}

func TestRestartRejectsForeignOrWritableConfigMountBeforeStop(t *testing.T) {
	for _, writable := range []bool{false, true} {
		t.Run(fmt.Sprint(writable), func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "auth")
			if writable {
				rig.configWritable = true
			} else {
				rig.configSource = filepath.Dir(rig.configSource)
			}
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			if _, err := rig.client.control(ctx, "auth", "fixture.ready", map[string]any{}); err != nil {
				t.Fatal(err)
			}
			if _, err := rig.client.restart(ctx, "auth"); err == nil {
				t.Fatal("foreign or writable config mount accepted")
			}
			for _, call := range rig.trace {
				if strings.Contains(call, " stop ") || strings.Contains(call, " start ") {
					t.Fatal("unverified mount mutated")
				}
			}
		})
	}
}
func TestFeedbackNamespaceContextDoesNotGuessOriginalIdentity(t *testing.T) {
	contextValue := map[string]any{"kind": "app", "userID": backendTestID, "orgID": backendOtherID, "tenantID": "33333333-3333-4333-8333-333333333333"}
	pair, err := feedbackSubmissionFromContext(backendTestID, contextValue)
	if err != nil || pair.Namespace.UserID != backendTestID {
		t.Fatal("trusted context lost")
	}
	delete(contextValue, "userID")
	if _, err := feedbackSubmissionFromContext(backendTestID, contextValue); err == nil {
		t.Fatal("identity guessed")
	}
}
func TestSinkFaultHelperUsesRealLocalHTTPProtocol(t *testing.T) {
	rig := newBackendInfrastructureRig(t, "smtp")
	seen := []fixtureRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var request fixtureRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Error(err)
			return
		}
		seen = append(seen, request)
		data := map[string]any{"faultID": request.Data["faultID"], "state": "armed"}
		if request.Operation == "fixture.ready" {
			data = map[string]any{"target": "smtp", "instanceID": backendTestID, "protocol": faultproxy.FixtureProtocol, "binarySHA256": strings.Repeat("a", 64), "configSHA256": strings.Repeat("b", 64), "ready": true, "capabilities": []string{"fixture.ready", "sink.fault.arm"}}
		}
		reply, err := json.Marshal(map[string]any{"version": 1, "project": request.Project, "requestID": request.RequestID, "operation": request.Operation, "data": data})
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := w.Write(reply); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	c := rig.client
	c.run.SinkURLs["smtp"] = server.URL
	c.run.CredentialFiles = map[string]faultproxy.FixtureFileRef{"fixture-smtp": {Target: "harness", Path: "/run/umir-fixtures/secrets/fixture-smtp"}}
	path := filepath.Join(c.run.Fixtures.HarnessPrivateRoot, "secrets", "fixture-smtp")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("synthetic-private-fixture-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	if _, err := c.control(ctx, "smtp", "fixture.ready", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	selector := map[string]any{"kind": "auth-otp", "jobID": backendTestID, "challengeID": backendOtherID}
	id, err := c.armSinkFault(ctx, "smtp", "accept-drop-reply", selector, time.Second)
	if err != nil || id == "" {
		t.Fatal(err)
	}
	if len(seen) != 2 || !reflect.DeepEqual(seen[1].Data["selector"], selector) || seen[1].Data["expireAfterMs"] != float64(1000) {
		t.Fatal("fault selector changed")
	}
}
