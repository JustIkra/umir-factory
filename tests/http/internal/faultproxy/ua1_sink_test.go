package faultproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const ua1Binding = "c468cb825a3b9bfe01577485f47267f4765c95286ac08de367002e3e25f3df45"
const ua1Fixture = "33333333-3333-4333-8333-333333333333"
const ua1Owner = "44444444-4444-4444-8444-444444444444"
const ua1Org = "55555555-5555-4555-8555-555555555555"

type ua1LateCancellation struct {
	context.Context
	cancel context.CancelFunc
	reads  atomic.Int32
}

func (c *ua1LateCancellation) Err() error {
	if c.reads.Add(1) == 2 {
		c.cancel()
	}
	return c.Context.Err()
}

func ua1Discovery(t *testing.T, identity UA1Identity) []byte {
	t.Helper()
	value := map[string]any{
		"version": 1, "supplement": "umir-ua1-mail-fixtures/0.1.0-unpublished", "bindingSHA256": ua1Binding, "project": fixtureProject, "requestID": "66666666-6666-4666-8666-666666666666", "owner": "auth", "identity": identity, "instant": "2026-10-14T12:00:00+03:00", "aggregateRevision": "2", "selector": map[string]any{"kind": "invitation_link", "orgID": ua1Org, "invitationID": fixtureChallenge}, "complete": true,
		"jobs": []any{map[string]any{"kind": "invitation_link", "jobID": fixtureJob, "state": "queued", "attempts": 0, "acceptedAt": "2026-10-14T12:00:00+03:00", "expiresAt": "2026-10-21T12:00:00+03:00", "lastAttemptAt": nil, "nextAttemptAt": nil, "enqueueOrder": "1", "messageID": "<invitation." + fixtureJob + "@umir.invalid>", "anchor": map[string]any{"orgID": ua1Org, "invitationID": fixtureChallenge}, "command": map[string]any{"operationID": "createInvitation", "commandID": "ua1-fixture-create", "actorID": ua1Org, "aggregateRevision": "2"}}},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func ua1RawLink() []byte {
	return []byte("From: sender@umir.invalid\r\nTo: recipient@umir.invalid\r\nMessage-ID: <invitation." + fixtureJob + "@umir.invalid>\r\nX-Umir-Fixture-Job-ID: " + fixtureJob + "\r\nX-Umir-Fixture-Invitation-ID: " + fixtureChallenge + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nOpen invitation https://app.umir.invalid/invite/" + fixtureChallenge + "\r\n")
}

func newUA1Receiver(t *testing.T) (*UA1MailReceiver, UA1Identity) {
	t.Helper()
	sink := newSinkForTest(t, "smtp")
	receiver, err := NewUA1MailReceiver(sink, ua1Fixture, ua1Binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(receiver.Close)
	identity := UA1Identity{FixtureID: ua1Fixture, InstanceID: ua1Owner, ClockGeneration: "1", BinarySHA256: strings.Repeat("b", 64), ConfigSHA256: strings.Repeat("c", 64)}
	return receiver, identity
}

func TestUA1ReceiverRequiresRealDiscoveryJoinAndRetainsPrivateMIME(t *testing.T) {
	receiver, identity := newUA1Receiver(t)
	selector := UA1LinkSelector{JobID: fixtureJob, InvitationID: fixtureChallenge}
	raw := ua1RawLink()
	correlation, safe, err := receiver.smtpCorrelation(raw)
	if err != nil || !safe {
		t.Fatal("valid link MIME rejected", err)
	}
	if reply, err := receiver.Accept(context.Background(), correlation, raw, true, true); err != nil || reply != "ack" {
		t.Fatal("actual SMTP data was not accepted", err)
	}
	if _, err := receiver.Captures(selector, 1, 64); err == nil {
		t.Fatal("unbound capture became qualified evidence")
	}
	if err := receiver.BindDiscovery(identity, ua1Discovery(t, identity)); err != nil {
		t.Fatal(err)
	}
	observed, err := receiver.Captures(selector, 1, 64)
	if err != nil || !observed.Complete || len(observed.Captures) != 1 || !observed.Captures[0].Accepted || observed.Captures[0].MessageID != "<invitation."+fixtureJob+"@umir.invalid>" || observed.Captures[0].ContentSHA256 != SHA256(raw) || observed.Captures[0].Owner != identity {
		t.Fatal("qualified MIME/job/owner join lost", err)
	}
	raw[0] = 'X'
	retained, err := receiver.Captures(selector, 1, 64)
	if err != nil || retained.Captures[0].ContentSHA256 != observed.Captures[0].ContentSHA256 {
		t.Fatal("caller changed retained source facts")
	}
	encoded, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"recipient@", "/invite/", "Open invitation", "https://"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("public capture exposed private MIME")
		}
	}
}

func TestUA1ReceiverRejectsMixedHeadersAndWrongMessageID(t *testing.T) {
	receiver, _ := newUA1Receiver(t)
	for _, raw := range [][]byte{
		bytes.Replace(ua1RawLink(), []byte("MIME-Version:"), []byte("X-Umir-Fixture-Challenge-ID: "+fixtureChallenge+"\r\nMIME-Version:"), 1),
		bytes.Replace(ua1RawLink(), []byte("MIME-Version:"), []byte("X-Umir-Fixture-Invitation-ID: "+fixtureChallenge+"\r\nMIME-Version:"), 1),
		bytes.Replace(ua1RawLink(), []byte("MIME-Version:"), []byte("Message-ID: <invitation."+fixtureJob+"@umir.invalid>\r\nMIME-Version:"), 1),
		bytes.Replace(ua1RawLink(), []byte("Message-ID: <invitation."+fixtureJob), []byte("Message-ID: <invitation."+fixtureChallenge), 1),
		bytes.Replace(ua1RawLink(), []byte("recipient@umir.invalid"), []byte("recipient@example.com"), 1),
	} {
		if _, _, err := receiver.smtpCorrelation(raw); err == nil {
			t.Fatal("ambiguous or private transport correlation accepted")
		}
	}
	if _, _, err := smtpCorrelation(ua1RawLink()); err == nil {
		t.Fatal("ordinary sink silently admitted the UA1 supplement")
	}
}

func TestUA1ReceiverParsesGeneratedAuthMIMEAndRejectsEmptyPayload(t *testing.T) {
	receiver, _ := newUA1Receiver(t)
	raw, err := os.ReadFile("testdata/ua1-link.eml")
	if err != nil {
		t.Fatal(err)
	}
	if _, safe, err := receiver.smtpCorrelation(raw); err != nil || !safe {
		t.Fatal("actual Auth multipart MIME rejected", err)
	}
	headers, _, found := bytes.Cut(ua1RawLink(), []byte("\r\n\r\n"))
	if !found {
		t.Fatal("invalid test MIME")
	}
	for _, invalid := range [][]byte{
		append(bytes.Clone(headers), []byte("\r\n\r\n")...),
		bytes.Replace(ua1RawLink(), []byte("text/plain; charset=utf-8"), []byte("application/json"), 1),
		bytes.Replace(raw, []byte("multipart/related"), []byte("multipart/malformed"), 1),
		bytes.Replace(ua1RawLink(), []byte("/invite/"+fixtureChallenge), []byte("/invite/"+fixtureJob), 1),
	} {
		if _, _, err := receiver.smtpCorrelation(invalid); err == nil {
			t.Fatal("unparsed or wrong-link payload became capture proof")
		}
	}
}

func TestUA1ReceiverEmptyInvitationHeaderDoesNotDowngradeToOTP(t *testing.T) {
	receiver, _ := newUA1Receiver(t)
	raw := []byte("From: sender@umir.invalid\r\nTo: recipient@umir.invalid\r\nX-Umir-Fixture-Job-ID: " + fixtureJob + "\r\nX-Umir-Fixture-Challenge-ID: " + fixtureChallenge + "\r\nX-Umir-Fixture-Invitation-ID:\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nVerification code:\r\n654321\r\n")
	if _, _, err := receiver.smtpCorrelation(raw); err == nil {
		t.Fatal("empty invitation header downgraded to legacy OTP")
	}
}

func TestUA1ReceiverRebindingDoesNotRewriteCaptureProvenance(t *testing.T) {
	receiver, identity := newUA1Receiver(t)
	first := ua1Discovery(t, identity)
	if err := receiver.BindDiscovery(identity, first); err != nil {
		t.Fatal(err)
	}
	correlation, safe, err := receiver.smtpCorrelation(ua1RawLink())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Accept(context.Background(), correlation, ua1RawLink(), safe, true); err != nil {
		t.Fatal(err)
	}
	selector := UA1LinkSelector{JobID: fixtureJob, InvitationID: fixtureChallenge}
	updated := bytes.Replace(first, []byte(`"aggregateRevision":"2"`), []byte(`"aggregateRevision":"3"`), 1)
	if err := receiver.BindDiscovery(identity, updated); err != nil {
		t.Fatal(err)
	}
	observed, err := receiver.Captures(selector, 1, 64)
	if err != nil || len(observed.Captures) != 1 || observed.Captures[0].DiscoverySHA256 != SHA256(first) || observed.Captures[0].AggregateRevision != "2" {
		t.Fatal("new binding rewrote historical capture provenance")
	}
	if err := receiver.ArmFault(ua1Org, selector, "permanent-reject", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	next := identity
	next.ClockGeneration = "2"
	if err := receiver.BindDiscovery(next, bytes.Replace(ua1Discovery(t, next), []byte(`"aggregateRevision":"2"`), []byte(`"aggregateRevision":"3"`), 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Captures(selector, 1, 64); err == nil {
		t.Fatal("generation advance left old observation window open")
	}
	if reply, err := receiver.Accept(context.Background(), correlation, ua1RawLink(), safe, true); err != nil || reply != "ack" {
		t.Fatal("stale generation fault remained armed")
	}
}

func TestUA1ReceiverPermanentAndLostReplyAreActualOneShotEffects(t *testing.T) {
	for _, mode := range []string{"permanent-reject", "accept-drop-reply"} {
		t.Run(mode, func(t *testing.T) {
			receiver, identity := newUA1Receiver(t)
			if err := receiver.BindDiscovery(identity, ua1Discovery(t, identity)); err != nil {
				t.Fatal(err)
			}
			selector := UA1LinkSelector{JobID: fixtureJob, InvitationID: fixtureChallenge}
			if err := receiver.ArmFault(ua1Org, selector, mode, 30*time.Second); err != nil {
				t.Fatal(err)
			}
			correlation, safe, err := receiver.smtpCorrelation(ua1RawLink())
			if err != nil {
				t.Fatal(err)
			}
			reply, err := receiver.Accept(context.Background(), correlation, ua1RawLink(), safe, true)
			if err != nil || mode == "permanent-reject" && reply != "permanent" || mode == "accept-drop-reply" && reply != "lost" {
				t.Fatal("transport fault did not act at actual data acceptance", err)
			}
			fault, err := receiver.Fault(ua1Org)
			if err != nil || fault.State != "hit" || fault.Hits != 1 {
				t.Fatal("fault was not actually hit")
			}
			if reply, err := receiver.Accept(context.Background(), correlation, ua1RawLink(), safe, true); err != nil || reply != "ack" {
				t.Fatal("one-shot fault repeated")
			}
			observed, err := receiver.Captures(selector, 1, 64)
			if err != nil || len(observed.Captures) != 2 || observed.Captures[0].Accepted != (mode == "accept-drop-reply") {
				t.Fatal("capture invented accepted versus rejected outcome")
			}
		})
	}
}

func TestUA1ReceiverRestartClosesOldCaptureWindowAndFault(t *testing.T) {
	receiver, identity := newUA1Receiver(t)
	if err := receiver.BindDiscovery(identity, ua1Discovery(t, identity)); err != nil {
		t.Fatal(err)
	}
	selector := UA1LinkSelector{JobID: fixtureJob, InvitationID: fixtureChallenge}
	correlation, safe, err := receiver.smtpCorrelation(ua1RawLink())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Accept(context.Background(), correlation, ua1RawLink(), safe, true); err != nil {
		t.Fatal(err)
	}
	if err := receiver.ArmFault(ua1Org, selector, "permanent-reject", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	newIdentity := identity
	newIdentity.InstanceID = "77777777-7777-4777-8777-777777777777"
	if err := receiver.BindDiscovery(newIdentity, ua1Discovery(t, newIdentity)); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Captures(selector, 1, 64); err == nil {
		t.Fatal("old capture window was relabelled as the new owner")
	}
	if reply, err := receiver.Accept(context.Background(), correlation, ua1RawLink(), safe, true); err != nil || reply != "ack" {
		t.Fatal("old-epoch fault affected the new owner")
	}
	current, err := receiver.Captures(selector, 2, 64)
	if err != nil || len(current.Captures) != 1 || current.Captures[0].Owner != newIdentity {
		t.Fatal("fresh epoch capture missing")
	}
	if err := receiver.BindDiscovery(identity, ua1Discovery(t, identity)); err == nil {
		t.Fatal("retired owner instance returned")
	}
}

func TestUA1ReceiverInvalidBindingLeavesExistingCaptureQualified(t *testing.T) {
	receiver, identity := newUA1Receiver(t)
	raw := ua1Discovery(t, identity)
	if err := receiver.BindDiscovery(identity, raw); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{
		bytes.Replace(raw, []byte(`"kind":"invitation_link"`), []byte(`"kind":"invitation_link","unknown":true`), 1),
		bytes.Replace(raw, []byte(`"clockGeneration":"1"`), []byte(`"clockGeneration":"1","clockGeneration":"1"`), 1),
		append(bytes.Clone(raw), []byte(` {}`)...),
		bytes.Replace(raw, []byte(`"aggregateRevision":"2"`), []byte(`"aggregateRevision":"1"`), 1),
		bytes.Replace(raw, []byte(`"orgID":"`+ua1Org+`"`), []byte(`"orgID":"`+ua1Owner+`"`), 1),
	} {
		if err := receiver.BindDiscovery(identity, invalid); err == nil {
			t.Fatal("corrupt or stale discovery was bound")
		}
	}
	if _, err := receiver.Captures(UA1LinkSelector{JobID: fixtureJob, InvitationID: fixtureChallenge}, 1, 64); err != nil {
		t.Fatal("rejected binding destroyed valid state")
	}
}

func TestUA1ReceiverCancellationBeforeCommitDoesNotConsumeFaultOrCapture(t *testing.T) {
	receiver, identity := newUA1Receiver(t)
	if err := receiver.BindDiscovery(identity, ua1Discovery(t, identity)); err != nil {
		t.Fatal(err)
	}
	selector := UA1LinkSelector{JobID: fixtureJob, InvitationID: fixtureChallenge}
	if err := receiver.ArmFault(ua1Org, selector, "accept-drop-reply", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	late := &ua1LateCancellation{Context: ctx, cancel: cancel}
	correlation, safe, err := receiver.smtpCorrelation(ua1RawLink())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Accept(late, correlation, ua1RawLink(), safe, true); err == nil {
		t.Fatal("cancelled acceptance changed receiver state")
	}
	fault, err := receiver.Fault(ua1Org)
	if err != nil || fault.State != "armed" || fault.Hits != 0 {
		t.Fatal("cancelled acceptance consumed fault")
	}
	captured, err := receiver.Captures(selector, 1, 64)
	if err != nil || len(captured.Captures) != 0 {
		t.Fatal("cancelled acceptance invented a capture")
	}
}
