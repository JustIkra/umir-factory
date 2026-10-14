package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

type feedbackNamespace struct {
	Kind     string `json:"kind"`
	UserID   string `json:"userID,omitempty"`
	OrgID    string `json:"orgID,omitempty"`
	TenantID string `json:"tenantID,omitempty"`
}
type feedbackSubmission struct {
	Namespace    feedbackNamespace `json:"namespace"`
	SubmissionID string            `json:"submissionID"`
}
type feedbackJob struct {
	JobID      string `json:"jobID"`
	Channel    string `json:"channel"`
	State      string `json:"state"`
	Attempts   int    `json:"attempts"`
	AcceptedAt string `json:"acceptedAt"`
}
type feedbackRecord struct {
	SubmissionID string            `json:"submissionID"`
	Namespace    feedbackNamespace `json:"namespace"`
	CommandID    string            `json:"commandID"`
	Fingerprint  string            `json:"fingerprint"`
	ReceivedAt   string            `json:"receivedAt"`
	Jobs         []feedbackJob     `json:"jobs"`
}
type feedbackProjection struct {
	InstanceID     string           `json:"instanceID"`
	SnapshotID     string           `json:"snapshotID"`
	SnapshotSHA256 string           `json:"snapshotSHA256"`
	Recovered      bool             `json:"recovered"`
	Complete       bool             `json:"complete"`
	Records        []feedbackRecord `json:"records"`
}
type feedbackObservation struct {
	Request    json.RawMessage
	Response   json.RawMessage
	Projection feedbackProjection
	Clock      fixtureClock
}

func canonicalFeedbackJSON(value any) ([]byte, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, faultproxy.ErrProtocol
	}
	encoded := bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	result := make([]byte, 0, len(encoded))
	for i := 0; i < len(encoded); i++ {
		if encoded[i] == '\\' && i+1 < len(encoded) {
			if i+6 <= len(encoded) && (string(encoded[i:i+6]) == "\\u2028" || string(encoded[i:i+6]) == "\\u2029") {
				separator := "\u2028"
				if encoded[i+5] == '9' {
					separator = "\u2029"
				}
				result = append(result, separator...)
				i += 5
				continue
			}
			result = append(result, encoded[i], encoded[i+1])
			i++
			continue
		}
		result = append(result, encoded[i])
	}
	return result, nil
}
func feedbackPairKey(pair feedbackSubmission) (string, error) {
	raw, err := json.Marshal(pair.Namespace)
	if err != nil || faultproxy.ValidateOperational("#/$defs/Namespace", raw) != nil || !faultproxy.UUID(pair.SubmissionID) {
		return "", faultproxy.ErrProtocol
	}
	parts := []string{pair.Namespace.Kind}
	if pair.Namespace.Kind == "app" {
		parts = append(parts, pair.Namespace.UserID, pair.Namespace.OrgID, pair.Namespace.TenantID)
	}
	parts = append(parts, pair.SubmissionID)
	raw, err = canonicalFeedbackJSON(parts)
	return string(raw), err
}
func feedbackPairKeys(pairs []feedbackSubmission) ([]string, error) {
	if len(pairs) < 1 || len(pairs) > 64 {
		return nil, faultproxy.ErrProtocol
	}
	keys := make([]string, 0, len(pairs))
	seen := map[string]bool{}
	for _, pair := range pairs {
		key, err := feedbackPairKey(pair)
		if err != nil || seen[key] {
			return nil, errors.New("CorrelationConflict")
		}
		seen[key] = true
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}
func validateFeedbackObservation(requestRaw, responseRaw []byte, trusted []feedbackSubmission, instance string) (feedbackProjection, error) {
	var projection feedbackProjection
	if len(requestRaw) > 16384 || len(responseRaw) > 262144 || faultproxy.ValidateOperational("#/$defs/feedback_observe_request", requestRaw) != nil || faultproxy.ValidateOperational("#/$defs/feedback_observe_response", responseRaw) != nil {
		return projection, faultproxy.ErrProtocol
	}
	var request struct {
		Version                       int
		Project, RequestID, Operation string
		Data                          struct{ Submissions []feedbackSubmission }
	}
	var response struct {
		Version                       int
		Project, RequestID, Operation string
		Data                          feedbackProjection
	}
	if json.Unmarshal(requestRaw, &request) != nil || json.Unmarshal(responseRaw, &response) != nil {
		return projection, faultproxy.ErrProtocol
	}
	projection = response.Data
	if request.Version != response.Version || request.Project != response.Project || request.RequestID != response.RequestID || request.Operation != response.Operation || projection.InstanceID != instance || !faultproxy.UUID(instance) {
		return projection, errors.New("OwnershipMismatch")
	}
	requested, err := feedbackPairKeys(request.Data.Submissions)
	if err != nil {
		return projection, err
	}
	original, err := feedbackPairKeys(trusted)
	if err != nil || !slices.Equal(requested, original) {
		return projection, errors.New("ForeignOrOmittedSelector")
	}
	last := ""
	jobs := map[string]bool{}
	for _, record := range projection.Records {
		key, err := feedbackPairKey(feedbackSubmission{record.Namespace, record.SubmissionID})
		if err != nil || key <= last || !slices.Contains(requested, key) {
			return projection, errors.New("CorrelationConflict")
		}
		last = key
		for i, job := range record.Jobs {
			if job.Channel != []string{"smtp", "telegram"}[i] || jobs[job.JobID] || job.AcceptedAt != record.ReceivedAt {
				return projection, errors.New("CorrelationConflict")
			}
			jobs[job.JobID] = true
		}
	}
	if projection.Complete != (len(projection.Records) == len(requested)) {
		return projection, errors.New("IncompleteProjection")
	}
	raw, err := canonicalFeedbackJSON(projection.Records)
	if err != nil || faultproxy.SHA256(raw) != projection.SnapshotSHA256 {
		return projection, errors.New("ProjectionDigestMismatch")
	}
	return projection, nil
}
func stableFeedbackFacts(records []feedbackRecord) []feedbackRecord {
	result := slices.Clone(records)
	for i := range result {
		result[i].Jobs = slices.Clone(result[i].Jobs)
		for j := range result[i].Jobs {
			result[i].Jobs[j].State = ""
			result[i].Jobs[j].Attempts = 0
		}
	}
	return result
}
func equalControlReplay(beforeRequest, before, afterRequest, after []byte) error {
	for _, raw := range [][]byte{beforeRequest, before, afterRequest, after} {
		if _, err := faultproxy.StrictJSON(raw, 16); err != nil {
			return err
		}
	}
	a, err := faultproxy.StrictJSON(beforeRequest, 16)
	if err != nil {
		return err
	}
	b, err := faultproxy.StrictJSON(afterRequest, 16)
	if err != nil {
		return err
	}
	x, err := faultproxy.StrictJSON(before, 16)
	if err != nil {
		return err
	}
	y, err := faultproxy.StrictJSON(after, 16)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(x, y) {
		return errors.New("CorrelationConflict")
	}
	return nil
}
func compareFeedbackObservations(beforeRequest, before, afterRequest, after []byte, trusted []feedbackSubmission, beforeInstance, afterInstance, mode string) error {
	a, err := validateFeedbackObservation(beforeRequest, before, trusted, beforeInstance)
	if err != nil {
		return err
	}
	b, err := validateFeedbackObservation(afterRequest, after, trusted, afterInstance)
	if err != nil {
		return err
	}
	if !a.Complete || !b.Complete {
		return errors.New("IncompleteComparison")
	}
	if !reflect.DeepEqual(stableFeedbackFacts(a.Records), stableFeedbackFacts(b.Records)) {
		return errors.New("ReplayOrReopenIdentityChanged")
	}
	var r1, r2 fixtureRequest
	if json.Unmarshal(beforeRequest, &r1) != nil || json.Unmarshal(afterRequest, &r2) != nil {
		return faultproxy.ErrProtocol
	}
	switch mode {
	case "control-replay":
		return equalControlReplay(beforeRequest, before, afterRequest, after)
	case "business-replay":
		if r1.RequestID == r2.RequestID || a.InstanceID != b.InstanceID {
			return errors.New("CorrelationConflict")
		}
	case "reopen":
		if r1.RequestID == r2.RequestID || a.InstanceID == b.InstanceID || !b.Recovered {
			return errors.New("CorrelationConflict")
		}
	default:
		return faultproxy.ErrProtocol
	}
	return nil
}
func (c *backendClient) observeFeedback(ctx context.Context, trusted []feedbackSubmission, wait time.Duration) (feedbackObservation, error) {
	var observation feedbackObservation
	if _, err := feedbackPairKeys(trusted); err != nil {
		return observation, err
	}
	clock, err := c.clock(ctx, "feedback")
	if err != nil {
		return observation, err
	}
	milliseconds, err := backendWaitMilliseconds(ctx, wait)
	if err != nil {
		return observation, err
	}
	id, err := faultproxy.NewUUID()
	if err != nil {
		return observation, err
	}
	request := fixtureRequest{1, c.run.Project, id, "feedback.observe", map[string]any{"submissions": slices.Clone(trusted), "waitMs": milliseconds}}
	raw, err := json.Marshal(request)
	if err != nil {
		return observation, faultproxy.ErrProtocol
	}
	_, response, err := c.controlRequest(ctx, "feedback", request)
	if err != nil {
		return observation, err
	}
	projection, err := validateFeedbackObservation(raw, response, trusted, clock.InstanceID)
	if err != nil {
		return observation, err
	}
	after, err := c.clock(ctx, "feedback")
	if err != nil || !reflect.DeepEqual(clock, after) {
		return observation, errors.New("ClockUnavailable")
	}
	observation = feedbackObservation{raw, slices.Clone(response), projection, clock}
	c.retainFeedbackEvidence(observation)
	return observation, nil
}
func (c *backendClient) replayFeedbackControl(ctx context.Context, original feedbackObservation, trusted []feedbackSubmission) (feedbackObservation, error) {
	var request fixtureRequest
	if json.Unmarshal(original.Request, &request) != nil || request.Operation != "feedback.observe" {
		return feedbackObservation{}, faultproxy.ErrProtocol
	}
	clock, err := c.clock(ctx, "feedback")
	if err != nil || clock.InstanceID != original.Clock.InstanceID {
		return feedbackObservation{}, errors.New("ClockUnavailable")
	}
	_, response, err := c.controlRequest(ctx, "feedback", request)
	if err != nil {
		return feedbackObservation{}, err
	}
	projection, err := validateFeedbackObservation(original.Request, response, trusted, clock.InstanceID)
	if err != nil {
		return feedbackObservation{}, err
	}
	if err := equalControlReplay(original.Request, original.Response, original.Request, response); err != nil {
		return feedbackObservation{}, err
	}
	after, err := c.clock(ctx, "feedback")
	if err != nil || !reflect.DeepEqual(clock, after) {
		return feedbackObservation{}, errors.New("ClockUnavailable")
	}
	observation := feedbackObservation{slices.Clone(original.Request), slices.Clone(response), projection, clock}
	c.retainFeedbackEvidence(observation)
	return observation, nil
}
func feedbackSubmissionFromContext(submission string, contextValue map[string]any) (feedbackSubmission, error) {
	var pair feedbackSubmission
	raw, err := json.Marshal(contextValue)
	if err != nil || faultproxy.ValidateOperational("#/$defs/Namespace", raw) != nil || json.Unmarshal(raw, &pair.Namespace) != nil {
		return pair, faultproxy.ErrProtocol
	}
	pair.SubmissionID = submission
	if _, err := feedbackPairKey(pair); err != nil {
		return pair, err
	}
	return pair, nil
}
func backendWaitMilliseconds(ctx context.Context, wait time.Duration) (int, error) {
	deadline, ok := ctx.Deadline()
	if !ok || ctx.Err() != nil || wait < 0 || wait > 5*time.Second {
		return 0, errors.New("scenario budget unavailable")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, errors.New("scenario budget exhausted")
	}
	if wait > remaining {
		wait = remaining
	}
	return int(wait / time.Millisecond), nil
}

type backendFeedbackEvidence struct {
	Request            json.RawMessage `json:"request"`
	Response           json.RawMessage `json:"response"`
	Clock              fixtureClock    `json:"clock"`
	ServerBinarySHA256 string          `json:"serverBinarySHA256"`
	ConfigSHA256       string          `json:"configSHA256"`
	ImageID            string          `json:"imageID"`
}

func (c *backendClient) retainFeedbackEvidence(observation feedbackObservation) {
	owner := c.run.Fixtures.Owners["feedback"]
	c.feedbackEvidence = append(c.feedbackEvidence, backendFeedbackEvidence{slices.Clone(observation.Request), slices.Clone(observation.Response), observation.Clock, owner.ServerBinarySHA256, owner.ConfigSHA256, c.run.Images["feedback"].ID})
}
