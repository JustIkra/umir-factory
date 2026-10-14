package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

type e5FeedbackReply struct {
	Receipt struct {
		SubmissionID string `json:"submissionID"`
		ReceivedAt   string `json:"receivedAt"`
	} `json:"receipt"`
	Scope feedbackNamespace `json:"scope"`
	State string            `json:"state"`
}

type e5FaultFact struct {
	FaultID      string `json:"faultID"`
	State        string `json:"state"`
	Hits         int    `json:"hits"`
	OwnerOutcome string `json:"ownerOutcome"`
}

func e5Decode(value any, result any) error {
	raw, err := json.Marshal(value)
	if err != nil || json.Unmarshal(raw, result) != nil {
		return faultproxy.ErrProtocol
	}
	return nil
}

func e5StoreFacts(client *backendClient, id string, value any) error {
	if client == nil || !filepath.IsAbs(client.run.EvidenceDirectory) {
		return errors.New("E5 evidence directory unavailable")
	}
	group, err := backendScenarioGroup(id)
	if err != nil || group != "e5" {
		return faultproxy.ErrProtocol
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return faultproxy.ErrProtocol
	}
	return writeBackendPrivateSnapshot(filepath.Join(client.run.EvidenceDirectory, id+"-facts.json"), raw)
}

func e5FeedbackRows(projection feedbackProjection, pairs []feedbackSubmission) error {
	keys, err := feedbackPairKeys(pairs)
	if err != nil || !projection.Complete || len(projection.Records) != len(keys) {
		return errors.New("E5 Feedback namespace observation incomplete")
	}
	seen, jobs := map[string]bool{}, map[string]bool{}
	for _, row := range projection.Records {
		key, err := feedbackPairKey(feedbackSubmission{row.Namespace, row.SubmissionID})
		if err != nil || !slices.Contains(keys, key) || seen[key] || row.CommandID != row.SubmissionID || !isDigest(row.Fingerprint) || len(row.Jobs) != 2 {
			return errors.New("E5 Feedback namespace identity differs")
		}
		seen[key] = true
		if _, err := time.Parse(time.RFC3339Nano, row.ReceivedAt); err != nil {
			return errors.New("E5 Feedback accepted instant unavailable")
		}
		for i, job := range row.Jobs {
			if !faultproxy.UUID(job.JobID) || jobs[job.JobID] || job.Channel != []string{"smtp", "telegram"}[i] || job.AcceptedAt != row.ReceivedAt {
				return errors.New("E5 Feedback job identity differs")
			}
			jobs[job.JobID] = true
		}
	}
	return nil
}

func e5FeedbackReceipt(data map[string]any, pair feedbackSubmission, state string) (e5FeedbackReply, error) {
	var reply e5FeedbackReply
	if e5Decode(data, &reply) != nil || reply.Receipt.SubmissionID != pair.SubmissionID || reply.Scope != pair.Namespace || reply.State != state {
		return reply, errors.New("E5 Feedback receipt or namespace differs")
	}
	if _, err := time.Parse(time.RFC3339Nano, reply.Receipt.ReceivedAt); err != nil {
		return reply, errors.New("E5 Feedback receipt instant unavailable")
	}
	return reply, nil
}

func e5FeedbackRestart(ctx context.Context, input backendScenarioInput) (result error) {
	b, c := input.Session, input.Client
	me, err := b.call(ctx, "me", nil, nil, nil, 200)
	if err != nil {
		return err
	}
	var identity struct{ User struct{ UserID string } }
	if e5Decode(me, &identity) != nil || !faultproxy.UUID(identity.User.UserID) {
		return errors.New("E5 Feedback real actor unavailable")
	}
	id, err := faultproxy.NewUUID()
	if err != nil {
		return err
	}
	pairs := []feedbackSubmission{}
	for _, scope := range input.Scopes[:2] {
		pair, err := feedbackSubmissionFromContext(id, map[string]any{"kind": "app", "userID": identity.User.UserID, "orgID": scope.OrgID, "tenantID": scope.RestaurantID})
		if err != nil {
			return err
		}
		pairs = append(pairs, pair)
	}
	pairs = append(pairs, feedbackSubmission{Namespace: feedbackNamespace{Kind: "anonymous"}, SubmissionID: id})
	if _, err := feedbackPairKeys(pairs); err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]any{"submissionID": id, "name": "Synthetic Operator", "email": "restart-feedback@umir.invalid", "message": "Synthetic restart acceptance", "consent": map[string]any{"accepted": true, "documentVersion": "e2e-fixture-v1"}})
	if err != nil {
		return err
	}
	fault, err := c.armOwnerFault(ctx, "feedback", "after-durable-before-reply", "appFeedback", id, 30*time.Second)
	if err != nil {
		return err
	}
	armed := true
	defer func() {
		if armed {
			result = errors.Join(result, c.clearFault(ctx, "feedback", fault))
		}
	}()
	_, dropped := b.callBytes(ctx, "appFeedback", map[string]string{"restaurantID": pairs[0].Namespace.TenantID}, nil, raw, nil, true, 201)
	if dropped == nil {
		return errors.New("E5 Feedback durable reply was not lost")
	}
	faultData, err := c.faultState(ctx, "feedback", fault)
	if err != nil {
		return err
	}
	var hit e5FaultFact
	if e5Decode(faultData, &hit) != nil || hit.FaultID != fault || hit.State != "hit" || hit.Hits != 1 || hit.OwnerOutcome != "committed" {
		return errors.New("E5 Feedback lost reply lacks real durable commit proof")
	}
	if err := c.clearFault(ctx, "feedback", fault); err != nil {
		return err
	}
	armed = false
	for _, pair := range pairs[1:] {
		op, params := e5FeedbackRoute(pair)
		data, err := b.callBytes(ctx, op, params, nil, raw, nil, true, 201)
		if err != nil {
			return err
		}
		if _, err := e5FeedbackReceipt(data, pair, "accepted"); err != nil {
			return err
		}
	}
	before, err := c.observeFeedback(ctx, pairs, 0)
	if err != nil {
		return err
	}
	if err := e5FeedbackRows(before.Projection, pairs); err != nil {
		return err
	}
	if _, err := c.restart(ctx, "feedback"); err != nil {
		return err
	}
	for _, pair := range pairs {
		op, params := e5FeedbackRoute(pair)
		data, err := b.callBytes(ctx, op, params, nil, raw, nil, true, 201)
		if err != nil {
			return err
		}
		reply, err := e5FeedbackReceipt(data, pair, "duplicate")
		if err != nil {
			return err
		}
		found := false
		for _, row := range before.Projection.Records {
			if row.Namespace == pair.Namespace && row.SubmissionID == pair.SubmissionID && row.ReceivedAt == reply.Receipt.ReceivedAt {
				found = true
			}
		}
		if !found {
			return errors.New("E5 Feedback replay changed original receipt")
		}
	}
	after, err := c.observeFeedback(ctx, pairs, 0)
	if err != nil {
		return err
	}
	if err := e5FeedbackRows(after.Projection, pairs); err != nil {
		return err
	}
	if err := compareFeedbackObservations(before.Request, before.Response, after.Request, after.Response, pairs, before.Clock.InstanceID, after.Clock.InstanceID, "reopen"); err != nil {
		return err
	}
	var changed map[string]any
	if json.Unmarshal(raw, &changed) != nil {
		return faultproxy.ErrProtocol
	}
	changed["message"] = "Changed synthetic content"
	for _, pair := range pairs {
		op, params := e5FeedbackRoute(pair)
		conflict, err := b.call(ctx, op, params, changed, nil, 409)
		if err != nil {
			return err
		}
		if conflict["code"] != "CommandConflict" || conflict["outcome"] != "not_committed" {
			return errors.New("E5 Feedback changed payload did not conflict")
		}
	}
	final, err := c.observeFeedback(ctx, pairs, 0)
	if err != nil {
		return err
	}
	if err := e5FeedbackRows(final.Projection, pairs); err != nil {
		return err
	}
	if err := compareFeedbackObservations(after.Request, after.Response, final.Request, final.Response, pairs, after.Clock.InstanceID, final.Clock.InstanceID, "business-replay"); err != nil {
		return err
	}
	if !reflect.DeepEqual(stableFeedbackFacts(before.Projection.Records), stableFeedbackFacts(final.Projection.Records)) {
		return errors.New("E5 Feedback conflict mutated durable identities")
	}
	return e5StoreFacts(c, "BE-E5-13", struct {
		Fault                e5FaultFact          `json:"fault"`
		Pairs                []feedbackSubmission `json:"pairs"`
		Before, After, Final feedbackProjection
	}{hit, pairs, before.Projection, after.Projection, final.Projection})
}

func e5FeedbackRoute(pair feedbackSubmission) (string, map[string]string) {
	if pair.Namespace.Kind == "anonymous" {
		return "anonymousFeedback", nil
	}
	return "appFeedback", map[string]string{"restaurantID": pair.Namespace.TenantID}
}
