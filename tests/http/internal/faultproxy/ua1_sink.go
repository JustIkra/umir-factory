package faultproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"regexp"
	"sync"
	"time"
)

type ua1Captured struct {
	view UA1LinkCapture
}

type ua1LinkBinding struct {
	owner                UA1Identity
	org, revision        string
	requestID, digest    string
	messageID, expiresAt string
}

type ua1LinkFault struct {
	selector UA1LinkSelector
	mode     string
	deadline time.Time
	observed UA1FaultObservation
}

type UA1MailReceiver struct {
	legacy         *FixtureSink
	fixture        string
	mu             sync.Mutex
	closed         bool
	owner          UA1Identity
	instant        string
	revision       string
	retired        map[string]bool
	bindings       map[UA1LinkSelector]ua1LinkBinding
	captures       []ua1Captured
	faults         map[string]*ua1LinkFault
	sequence       uint64
	epochStart     uint64
	total, dropped int
}

func NewUA1MailReceiver(legacy *FixtureSink, fixture, binding string) (*UA1MailReceiver, error) {
	if legacy == nil || legacy.target != "smtp" || !canonicalFixtureID(fixture) || binding != UA1MailBindingSHA256 {
		return nil, fixtureError("IdentityConflict")
	}
	return &UA1MailReceiver{legacy: legacy, fixture: fixture, retired: map[string]bool{}, bindings: map[UA1LinkSelector]ua1LinkBinding{}, faults: map[string]*ua1LinkFault{}, sequence: 1, epochStart: 1}, nil
}

var ua1Identifier = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func canonicalFixtureID(value string) bool { return ua1Identifier.MatchString(value) }

func (r *UA1MailReceiver) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.captures = nil
	r.bindings = nil
	r.faults = nil
}

func (r *UA1MailReceiver) BindDiscovery(expected UA1Identity, raw []byte) error {
	if err := validateUA1Mail("Response", raw); err != nil {
		return fixtureError("InvalidRequest")
	}
	var input ua1DiscoveryResponse
	if json.Unmarshal(raw, &input) != nil || input.Project != r.legacy.project || input.BindingSHA256 != UA1MailBindingSHA256 || input.Identity != expected || input.Identity.FixtureID != r.fixture {
		return fixtureError("IdentityConflict")
	}
	if input.Selector.Kind != "invitation_link" {
		return fixtureError("CorrelationConflict")
	}
	bindings := make(map[UA1LinkSelector]ua1LinkBinding, len(input.Jobs))
	for _, job := range input.Jobs {
		selector := UA1LinkSelector{JobID: job.JobID, InvitationID: job.Anchor.InvitationID}
		if job.Kind != "invitation_link" || job.Anchor.ChallengeID != "" || job.Anchor.OrgID != input.Selector.OrgID || job.Anchor.InvitationID != input.Selector.InvitationID || input.Selector.JobID != "" && job.JobID != input.Selector.JobID || job.MessageID != "<invitation."+job.JobID+"@umir.invalid>" {
			return fixtureError("CorrelationConflict")
		}
		if _, exists := bindings[selector]; exists {
			return fixtureError("CorrelationConflict")
		}
		bindings[selector] = ua1LinkBinding{owner: input.Identity, org: job.Anchor.OrgID, revision: input.AggregateRevision, requestID: input.RequestID, digest: SHA256(raw), messageID: job.MessageID, expiresAt: job.ExpiresAt}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fixtureError("NotReady")
	}
	if r.retired[expected.InstanceID] {
		return fixtureError("ClockConflict")
	}
	newOwner := r.owner.InstanceID != "" && r.owner.InstanceID != expected.InstanceID
	if newOwner {
		if len(r.retired) >= 256 {
			return fixtureError("CapacityExceeded")
		}
	}
	if r.owner.InstanceID != "" {
		before, err := Decimal(r.owner.ClockGeneration)
		after, nextErr := Decimal(expected.ClockGeneration)
		previousRevision, revisionErr := Decimal(r.revision)
		nextRevision, nextRevisionErr := Decimal(input.AggregateRevision)
		previousInstant, timeErr := time.Parse(time.RFC3339Nano, r.instant)
		nextInstant, nextTimeErr := time.Parse(time.RFC3339Nano, input.Instant)
		if err != nil || nextErr != nil || revisionErr != nil || nextRevisionErr != nil || timeErr != nil || nextTimeErr != nil || !newOwner && after < before || nextRevision < previousRevision || nextInstant.Before(previousInstant) || !newOwner && after == before && input.Instant != r.instant || expected.BinarySHA256 != r.owner.BinarySHA256 || expected.ConfigSHA256 != r.owner.ConfigSHA256 {
			return fixtureError("ClockConflict")
		}
	}
	count := len(r.bindings)
	for selector, binding := range bindings {
		if old, found := r.bindings[selector]; found {
			if old.org != binding.org || old.messageID != binding.messageID || old.expiresAt != binding.expiresAt {
				return fixtureError("CorrelationConflict")
			}
		} else {
			count++
		}
	}
	if count > 256 {
		return fixtureError("CapacityExceeded")
	}
	if r.owner.InstanceID != "" && r.owner != expected {
		if newOwner {
			r.retired[r.owner.InstanceID] = true
		}
		r.epochStart = r.sequence
		for _, fault := range r.faults {
			if fault.observed.State == "armed" {
				fault.observed.State = "cleared"
			}
		}
	}
	r.owner, r.instant, r.revision = expected, input.Instant, input.AggregateRevision
	for selector, binding := range bindings {
		r.bindings[selector] = binding
		for index := range r.captures {
			view := &r.captures[index].view
			if view.Selector == selector && view.Sequence >= r.epochStart && view.DiscoverySHA256 == "" {
				freezeUA1Provenance(view, binding)
			}
		}
	}
	return nil
}

func (r *UA1MailReceiver) Accept(ctx context.Context, correlation map[string]any, raw []byte, synthetic, safe bool) (string, error) {
	if correlation["kind"] != "auth-invitation-link" {
		return r.legacy.Accept(ctx, correlation, raw, synthetic, safe)
	}
	if ctx.Err() != nil {
		return "", fixtureError("WaitExpired")
	}
	if !synthetic || !safe {
		return "", fixtureError("PrivacyViolation")
	}
	actual, valid, err := r.smtpCorrelation(raw)
	if err != nil || !valid || !bytes.Equal(canonical(actual), canonical(correlation)) || validateUA1Mail("LinkCorrelation", canonical(correlation)) != nil {
		return "", fixtureError("CorrelationConflict")
	}
	selector := UA1LinkSelector{JobID: fixtureString(correlation, "jobID"), InvitationID: fixtureString(correlation, "invitationID")}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return "", fixtureError("WaitExpired")
	}
	if r.closed {
		return "", fixtureError("NotReady")
	}
	if len(r.captures) == 256 || r.total+len(raw) > 16<<20 || r.sequence == math.MaxUint64 {
		r.dropped++
		return "", fixtureError("CapacityExceeded")
	}
	reply := "ack"
	for _, fault := range r.faults {
		if fault.observed.State == "armed" && !time.Now().Before(fault.deadline) {
			fault.observed.State = "expired"
		}
		if fault.observed.State == "armed" && fault.selector == selector {
			fault.observed.State, fault.observed.Hits = "hit", 1
			if fault.mode == "permanent-reject" {
				reply = "permanent"
			} else {
				reply = "lost"
			}
		}
	}
	id, err := NewUUID()
	if err != nil {
		return "", fixtureError("NotReady")
	}
	view := UA1LinkCapture{CaptureID: id, Sequence: r.sequence, Selector: selector, MessageID: "<invitation." + selector.JobID + "@umir.invalid>", Accepted: reply != "permanent", Reply: reply, Bytes: len(raw), ContentSHA256: SHA256(raw)}
	if binding, found := r.bindings[selector]; found && binding.owner == r.owner {
		freezeUA1Provenance(&view, binding)
	}
	r.captures = append(r.captures, ua1Captured{view: view})
	r.total += len(raw)
	r.sequence++
	return reply, nil
}

func (r *UA1MailReceiver) Captures(selector UA1LinkSelector, after uint64, limit int) (UA1LinkObservation, error) {
	if !canonicalFixtureID(selector.JobID) || !canonicalFixtureID(selector.InvitationID) || after == 0 || limit < 1 || limit > 64 {
		return UA1LinkObservation{}, fixtureError("InvalidRequest")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.dropped != 0 {
		return UA1LinkObservation{}, fixtureError("NotReady")
	}
	if after < r.epochStart {
		return UA1LinkObservation{}, fixtureError("ClockConflict")
	}
	binding, bound := r.bindings[selector]
	if !bound || binding.owner != r.owner {
		return UA1LinkObservation{}, fixtureError("NotReady")
	}
	result := UA1LinkObservation{Captures: []UA1LinkCapture{}, Complete: true}
	for _, capture := range r.captures {
		if capture.view.Selector != selector || capture.view.Sequence < after {
			continue
		}
		if len(result.Captures) == limit {
			return UA1LinkObservation{}, fixtureError("CapacityExceeded")
		}
		if capture.view.DiscoverySHA256 == "" {
			return UA1LinkObservation{}, fixtureError("NotReady")
		}
		result.Captures = append(result.Captures, capture.view)
	}
	return result, nil
}

func freezeUA1Provenance(view *UA1LinkCapture, binding ua1LinkBinding) {
	view.Owner, view.OrgID, view.AggregateRevision = binding.owner, binding.org, binding.revision
	view.DiscoveryRequestID, view.DiscoverySHA256 = binding.requestID, binding.digest
}

func (r *UA1MailReceiver) ArmFault(id string, selector UA1LinkSelector, mode string, lifetime time.Duration) error {
	if !canonicalFixtureID(id) || !canonicalFixtureID(selector.JobID) || !canonicalFixtureID(selector.InvitationID) || lifetime < 100*time.Millisecond || lifetime > 30*time.Second || mode != "permanent-reject" && mode != "accept-drop-reply" {
		return fixtureError("InvalidRequest")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fixtureError("NotReady")
	}
	if binding, found := r.bindings[selector]; !found || binding.owner != r.owner {
		return fixtureError("NotReady")
	}
	if _, exists := r.faults[id]; exists {
		return fixtureError("FaultConflict")
	}
	if len(r.faults) == 256 {
		return fixtureError("CapacityExceeded")
	}
	for _, fault := range r.faults {
		if fault.selector == selector && fault.observed.State == "armed" && time.Now().Before(fault.deadline) {
			return fixtureError("FaultConflict")
		}
	}
	r.faults[id] = &ua1LinkFault{selector: selector, mode: mode, deadline: time.Now().Add(lifetime), observed: UA1FaultObservation{FaultID: id, State: "armed"}}
	return nil
}

func (r *UA1MailReceiver) Fault(id string) (UA1FaultObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fault, found := r.faults[id]
	if !found || r.closed {
		return UA1FaultObservation{}, fixtureError("NotFound")
	}
	if fault.observed.State == "armed" && !time.Now().Before(fault.deadline) {
		fault.observed.State = "expired"
	}
	return fault.observed, nil
}
