package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

func TestReviewRestartWaitsForControlStartup(t *testing.T) {
	rig := newBackendInfrastructureRig(t, "warehouse")
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	if _, err := rig.client.control(ctx, "warehouse", "fixture.ready", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	execute := rig.client.execute
	started, unavailable, readyReads := false, false, 0
	rig.client.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " start ") {
			started = true
		}
		if started && strings.Contains(joined, " exec ") {
			var request fixtureRequest
			if err := json.Unmarshal(input, &request); err != nil {
				return nil, err
			}
			if request.Operation == "fixture.ready" {
				readyReads++
				if !unavailable {
					unavailable = true
					return nil, errors.New("owned Docker operation failed")
				}
			}
		}
		return execute(ctx, input, args...)
	}
	_, err := rig.client.restart(ctx, "warehouse")
	if err != nil {
		t.Fatalf("bounded restart aborted on startup transport failure after %d readiness attempt(s): %v", readyReads, err)
	}
	if readyReads < 2 {
		t.Fatal("startup readiness was not polled")
	}
}

func TestReviewControlReplaySurvivesClockAdvanceInSameProcess(t *testing.T) {
	rig := newBackendInfrastructureRig(t, "feedback")
	rig.projection = map[string]any{"snapshotID": backendOtherID, "snapshotSHA256": faultproxy.SHA256([]byte("[]")), "recovered": true, "complete": false, "records": []any{}}
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	if _, err := rig.client.control(ctx, "feedback", "fixture.ready", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	pairs := []feedbackSubmission{{Namespace: feedbackNamespace{Kind: "anonymous"}, SubmissionID: backendOtherID}}
	before, err := rig.client.observeFeedback(ctx, pairs, 0)
	if err != nil {
		t.Fatal(err)
	}
	rig.clock.Generation = "4"
	rig.clock.Instant = "2026-10-13T10:01:00+03:00"
	cached, err := rig.client.replayFeedbackControl(ctx, before, pairs)
	if err != nil {
		t.Fatalf("same-process cached reply rejected after completed clock advance: %v", err)
	}
	if err := equalControlReplay(before.Request, before.Response, cached.Request, cached.Response); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.client.observeFeedback(ctx, pairs, time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestRestartStartupRefusalsRemainTerminal(t *testing.T) {
	for _, refusal := range []string{"protocol", "ownership", "not-ready-terminal", "effect-unknown", "unsupported", "mutation"} {
		t.Run(refusal, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "warehouse")
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			if _, err := rig.client.control(ctx, "warehouse", "fixture.ready", map[string]any{}); err != nil {
				t.Fatal(err)
			}
			execute := rig.client.execute
			started, calls := false, 0
			rig.client.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, " start ") {
					started = true
					if refusal == "mutation" {
						calls++
						return nil, errors.New("owned Docker operation failed")
					}
				}
				if started && strings.Contains(joined, " exec ") {
					calls++
					var request fixtureRequest
					if err := json.Unmarshal(input, &request); err != nil {
						return nil, err
					}
					switch refusal {
					case "protocol":
						return []byte(`{"malformed":true}`), nil
					case "ownership":
						raw, err := execute(ctx, input, args...)
						var reply map[string]any
						if err != nil || json.Unmarshal(raw, &reply) != nil {
							return nil, faultproxy.ErrProtocol
						}
						data, ok := reply["data"].(map[string]any)
						if !ok {
							return nil, faultproxy.ErrProtocol
						}
						data["configSHA256"] = strings.Repeat("f", 64)
						return json.Marshal(reply)
					default:
						code, effect, retryable := "NotReady", "none", false
						if refusal == "effect-unknown" {
							code, effect, retryable = "OutcomeUnknown", "unknown", false
						}
						if refusal == "unsupported" {
							code = "UnsupportedOperation"
						}
						return json.Marshal(map[string]any{"version": 1, "project": request.Project, "requestID": request.RequestID, "operation": request.Operation, "error": map[string]any{"code": code, "effect": effect, "retryable": retryable}})
					}
				}
				return execute(ctx, input, args...)
			}
			if _, err := rig.client.restart(ctx, "warehouse"); err == nil {
				t.Fatal("terminal refusal accepted")
			}
			if calls != 1 {
				t.Fatalf("terminal refusal retried: %d calls", calls)
			}
		})
	}
}

func TestRestartStartupPollingHonorsBudgetAndOwnership(t *testing.T) {
	for _, failure := range []string{"budget", "ownership-drift"} {
		t.Run(failure, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "warehouse")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := rig.client.control(ctx, "warehouse", "fixture.ready", map[string]any{}); err != nil {
				t.Fatal(err)
			}
			execute := rig.client.execute
			started, calls := false, 0
			rig.client.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, " start ") {
					started = true
				}
				if started && strings.Contains(joined, " exec ") {
					calls++
					if failure == "budget" && calls == 2 {
						cancel()
					}
					if failure == "ownership-drift" {
						rig.running = false
					}
					return nil, errors.New("owned Docker operation failed")
				}
				return execute(ctx, input, args...)
			}
			if _, err := rig.client.restart(ctx, "warehouse"); err == nil {
				t.Fatal("failed startup accepted")
			}
			if failure == "budget" && (calls != 2 || ctx.Err() == nil) {
				t.Fatalf("budget was not honored after polling: calls=%d err=%v", calls, ctx.Err())
			}
			if failure == "ownership-drift" && calls != 1 {
				t.Fatalf("control sent after ownership changed: %d", calls)
			}
			if len(rig.client.restarts) != 0 {
				t.Fatal("failed restart recorded as successful")
			}
		})
	}
}

func TestCachedFeedbackReplayRejectsInstanceClockOrReplyChange(t *testing.T) {
	for _, change := range []string{"new-instance", "unhealthy", "clock-during-replay", "changed-reply"} {
		t.Run(change, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "feedback")
			rig.projection = map[string]any{"snapshotID": backendOtherID, "snapshotSHA256": faultproxy.SHA256([]byte("[]")), "recovered": true, "complete": false, "records": []any{}}
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			if _, err := rig.client.control(ctx, "feedback", "fixture.ready", map[string]any{}); err != nil {
				t.Fatal(err)
			}
			pairs := []feedbackSubmission{{Namespace: feedbackNamespace{Kind: "anonymous"}, SubmissionID: backendOtherID}}
			before, err := rig.client.observeFeedback(ctx, pairs, 0)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "new-instance":
				rig.clock.InstanceID = "33333333-3333-4333-8333-333333333333"
				rig.client.instances["feedback"] = rig.clock.InstanceID
			case "unhealthy":
				rig.clock.Healthy = false
			}
			execute := rig.client.execute
			replays := 0
			rig.client.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
				raw, err := execute(ctx, input, args...)
				if !strings.Contains(strings.Join(args, " "), " exec ") {
					return raw, err
				}
				var request fixtureRequest
				if err := json.Unmarshal(input, &request); err != nil {
					return nil, err
				}
				if request.Operation == "feedback.observe" {
					replays++
					if change == "clock-during-replay" {
						rig.clock.Generation = "4"
						rig.clock.Instant = "2026-10-13T10:01:00+03:00"
					}
					if change == "changed-reply" {
						var reply map[string]any
						if err != nil || json.Unmarshal(raw, &reply) != nil {
							return nil, faultproxy.ErrProtocol
						}
						data, ok := reply["data"].(map[string]any)
						if !ok {
							return nil, faultproxy.ErrProtocol
						}
						data["recovered"] = false
						return json.Marshal(reply)
					}
				}
				return raw, err
			}
			if _, err := rig.client.replayFeedbackControl(ctx, before, pairs); err == nil {
				t.Fatal("changed process, clock or cached reply accepted")
			}
			if (change == "new-instance" || change == "unhealthy") && replays != 0 {
				t.Fatal("replayed into an unverified process")
			}
			if (change == "clock-during-replay" || change == "changed-reply") && replays != 1 {
				t.Fatal("replay boundary not exercised")
			}
		})
	}
}

func TestRestartReadyPollsRetryableReadOnlyReplies(t *testing.T) {
	for _, code := range []string{"NotReady", "WaitExpired"} {
		t.Run(code, func(t *testing.T) {
			rig := newBackendInfrastructureRig(t, "warehouse")
			ctx, cancel := backendScenarioContext(context.Background())
			defer cancel()
			if _, err := rig.client.control(ctx, "warehouse", "fixture.ready", map[string]any{}); err != nil {
				t.Fatal(err)
			}
			execute := rig.client.execute
			started, reads := false, 0
			rig.client.execute = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, " start ") {
					started = true
				}
				if started && strings.Contains(joined, " exec ") {
					var request fixtureRequest
					if err := json.Unmarshal(input, &request); err != nil {
						return nil, err
					}
					if request.Operation == "fixture.ready" {
						reads++
						if reads == 1 {
							return json.Marshal(map[string]any{"version": 1, "project": request.Project, "requestID": request.RequestID, "operation": request.Operation, "error": map[string]any{"code": code, "effect": "none", "retryable": true}})
						}
					}
				}
				return execute(ctx, input, args...)
			}
			if _, err := rig.client.restart(ctx, "warehouse"); err != nil {
				t.Fatal(err)
			}
			if reads != 2 || len(rig.client.restarts) != 1 {
				t.Fatal("read-only readiness did not recover")
			}
		})
	}
}
