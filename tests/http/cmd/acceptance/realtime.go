package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const liveActivityHeader = "X-Umir-Activity"

func (s *suite) openLive() (*liveStream, []liveEvent, error) {
	if !s.cfg.realtime || s.liveClient == nil {
		return nil, nil, errors.New("realtime acceptance was not explicitly enabled")
	}
	stream, err := openLiveStream(context.Background(), s.liveClient, s.cfg.gatewayURL, s.session)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initial, err := stream.initialEvents(ctx)
	if err != nil {
		return nil, nil, errors.Join(err, stream.Close())
	}
	return stream, initial, nil
}

func (s *suite) runRealtime() (resultErr error) {
	s.current = "R01"
	stream, initial, err := s.openLive()
	if err != nil {
		return fmt.Errorf("open authenticated live stream: %w", err)
	}
	firstStream := stream
	defer func() { resultErr = errors.Join(resultErr, firstStream.Close()) }()
	epoch := initial[0].Change.Epoch
	seq := initial[0].Change.Seq
	if epoch == "" || !decimalString(seq) || initial[1].Tick == nil || initial[2].Session == nil || initial[3].Upstream == nil {
		return errors.New("live stream initial events do not contain the required state")
	}
	s.record("R01", map[string]string{"initialEvents": "change,tick,session,upstream", "epoch": epoch, "seq": seq})

	s.current = "R04"
	before := initial[2].Session
	backgroundRequest, err := http.NewRequestWithContext(context.Background(), http.MethodGet, strings.TrimRight(s.cfg.gatewayURL, "/")+"/api/serving/menu", nil)
	if err != nil {
		return err
	}
	s.session.apply(backgroundRequest)
	backgroundRequest.Header.Set(liveActivityHeader, "background")
	background, err := s.send(backgroundRequest)
	if err != nil || background.status != http.StatusOK {
		return errorWithCause(fmt.Sprintf("background serving GET returned %d", background.status), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	err = ensureNoSessionEvent(ctx, stream)
	cancel()
	if err != nil {
		return err
	}
	if err := stream.Close(); err != nil {
		return fmt.Errorf("close stream before deadline snapshot: %w", err)
	}
	stream, reopened, err := s.openLive()
	if err != nil {
		return fmt.Errorf("reopen live stream after background GET: %w", err)
	}
	backgroundStream := stream
	defer func() { resultErr = errors.Join(resultErr, backgroundStream.Close()) }()
	after := reopened[2].Session
	if before.IdleExpiresAt != after.IdleExpiresAt || before.AbsoluteExpiresAt != after.AbsoluteExpiresAt {
		return fmt.Errorf("background GET or stream reopen changed session deadlines: before=%+v after=%+v", before, after)
	}
	s.record("R04", map[string]string{"backgroundGET": "200", "idleExpiresAt": after.IdleExpiresAt, "reopenedStream": "unchanged"})

	s.current = "R02"
	productID := "product-R02"
	if err := s.product("R02", productID); err != nil {
		return err
	}
	baseline, err := waitChangeWithin(stream, epoch, seq, 2*time.Second)
	if err != nil {
		return fmt.Errorf("product setup change signal: %w", err)
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	deliveryID := "delivery-R02"
	if _, err := s.delivery("R02", deliveryID, productID, "1000", now.Add(-time.Hour), now); err != nil {
		return err
	}
	deliveryChange, err := waitChangeWithin(stream, epoch, baseline.Seq, 2*time.Second)
	if err != nil {
		return fmt.Errorf("successful delivery change signal: %w", err)
	}
	s.record("R02", map[string]string{"deliveryID": deliveryID, "epoch": deliveryChange.Epoch, "seq": deliveryChange.Seq, "within": "2s"})

	s.current = "R03"
	duplicate := map[string]any{
		"commandID": "receive-R02-duplicate", "deliveryID": deliveryID, "number": "Delivery R02 duplicate",
		"createdAt": moscow(now), "lines": []any{map[string]any{
			"productID": productID, "manufacturedAt": moscow(now.Add(-time.Hour)),
			"shelfLife": map[string]any{"value": "10", "unit": "day"}, "receivedAt": moscow(now), "quantity": "1000",
		}},
	}
	rejected, err := s.call(context.Background(), http.MethodPost, "/api/warehouse/deliveries", duplicate)
	if err != nil || rejected.status != http.StatusConflict {
		return errorWithCause(fmt.Sprintf("duplicate delivery returned %d, want 409", rejected.status), err)
	}
	if err := ensureNoChangeWithin(stream, 2*time.Second); err != nil {
		return err
	}
	s.record("R03", map[string]string{"duplicateStatus": "409", "change": "none for 2s"})

	s.current = "R05"
	oldEpoch := deliveryChange.Epoch
	if err := stream.Close(); err != nil {
		return err
	}
	restartConfig := restartConfig{factoryDir: s.cfg.factoryDir, project: s.cfg.composeProject, overrides: s.cfg.composeOverrides}
	if err := restart(context.Background(), restartConfig, "gateway"); err != nil {
		return fmt.Errorf("restart owned gateway: %w", err)
	}
	if err := waitHealth(s.client, s.cfg.gatewayURL+"/api/serving/healthz", 20*time.Second); err != nil {
		return fmt.Errorf("gateway health after owned restart: %w", err)
	}
	if err := s.restoreSessionIfNeeded(); err != nil {
		return err
	}
	stream, afterRestart, err := s.openLive()
	if err != nil {
		return fmt.Errorf("reconnect after owned gateway restart: %w", err)
	}
	restartStream := stream
	defer func() { resultErr = errors.Join(resultErr, restartStream.Close()) }()
	if afterRestart[0].Change.Epoch == oldEpoch {
		return fmt.Errorf("gateway restart reused live epoch %q", oldEpoch)
	}
	epoch = afterRestart[0].Change.Epoch
	seq = afterRestart[0].Change.Seq
	s.record("R05", map[string]string{"oldEpoch": oldEpoch, "newEpoch": epoch, "gatewayRestart": "owned project only"})

	s.current = "R06"
	route, err := s.call(context.Background(), http.MethodPost, "/api/warehouse/expiration-runs", nil)
	if err != nil || route.status != http.StatusNotFound {
		return errorWithCause(fmt.Sprintf("browser expiration-run route returned %d, want 404", route.status), err)
	}
	if err := s.observeQuietExpiration(stream, epoch, seq); err != nil {
		return err
	}
	if err := s.logoutAndObserveEnd(stream); err != nil {
		return err
	}
	s.record("R06", map[string]string{"browserExpirationRun": "404", "expiry": "passive history has exactly one BatchExpired", "logout": "session-ended signed_out then EOF"})
	return nil
}

func (s *suite) observeQuietExpiration(stream *liveStream, epoch, seq string) error {
	productID := "product-R06-expiration"
	if err := s.product("R06", productID); err != nil {
		return err
	}
	baseline, err := waitChangeWithin(stream, epoch, seq, 2*time.Second)
	if err != nil {
		return fmt.Errorf("expiration product setup change signal: %w", err)
	}
	warehouseNow, err := s.now()
	if err != nil {
		return err
	}
	const lead = 8 * time.Second
	deliveryID := "delivery-R06-expiration"
	manufacturedAt := warehouseNow.Add(-24*time.Hour + lead)
	receive := map[string]any{
		"commandID": "receive-R06-expiration", "deliveryID": deliveryID, "number": "Delivery R06 expiration",
		"createdAt": moscow(warehouseNow), "lines": []any{map[string]any{
			"productID": productID, "manufacturedAt": moscow(manufacturedAt),
			"shelfLife":  map[string]any{"value": "1", "unit": "day"},
			"receivedAt": moscow(warehouseNow), "quantity": "1000",
		}},
	}
	accepted, err := s.call(context.Background(), http.MethodPost, "/api/warehouse/deliveries", receive)
	if err != nil || accepted.status != http.StatusOK {
		return errorWithCause(fmt.Sprintf("short-shelf-life delivery returned %d", accepted.status), err)
	}
	batchIDs := array(accepted.body["batchIDs"])
	if len(batchIDs) != 1 {
		return fmt.Errorf("short-shelf-life delivery returned %d batches", len(batchIDs))
	}
	batchID := text(batchIDs[0])
	historyBefore, err := s.history()
	if err != nil {
		return err
	}
	expiresAt, err := deliveryBatchExpiry(historyBefore, deliveryID, batchID)
	if err != nil {
		return err
	}
	if !expiresAt.After(time.Now().Add(3 * time.Second)) {
		return fmt.Errorf("batch expiresAt %s is not safely in the future before the quiet period", expiresAt.Format(time.RFC3339Nano))
	}
	deliveryChange, err := waitChangeWithin(stream, epoch, baseline.Seq, 2*time.Second)
	if err != nil {
		return fmt.Errorf("short-shelf-life delivery change signal: %w", err)
	}
	quietFor := time.Until(expiresAt) + 5*time.Second
	if quietFor <= 0 {
		return fmt.Errorf("expiration deadline %s passed before the quiet period began", expiresAt.Format(time.RFC3339Nano))
	}
	change, err := waitChangeWithin(stream, epoch, deliveryChange.Seq, quietFor)
	if err != nil {
		return fmt.Errorf("expiration did not create a live change during the quiet period: %w", err)
	}
	if !changeGreater(change.Seq, deliveryChange.Seq) {
		return errors.New("expiration change sequence did not advance")
	}
	historyAfter, err := s.history()
	if err != nil {
		return err
	}
	if got := batchExpiredCount(historyAfter, batchID); got != 1 {
		return fmt.Errorf("passive history has %d BatchExpired entries for %s, want exactly one", got, batchID)
	}
	if err := ensureNoChangeWithin(stream, 2*time.Second); err != nil {
		return fmt.Errorf("duplicate expiration change after subsequent scheduler ticks: %w", err)
	}
	historyLater, err := s.history()
	if err != nil {
		return err
	}
	if got := batchExpiredCount(historyLater, batchID); got != 1 {
		return fmt.Errorf("passive history has %d BatchExpired entries after later ticks for %s, want one", got, batchID)
	}
	return nil
}

func (s *suite) logoutAndObserveEnd(stream *liveStream) error {
	response, err := s.call(context.Background(), http.MethodDelete, "/api/operator/session", nil)
	if err != nil || response.status != http.StatusNoContent {
		return errorWithCause(fmt.Sprintf("logout returned %d, want 204", response.status), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	ended, err := waitEvent(ctx, stream, "session-ended")
	cancel()
	if err != nil {
		return fmt.Errorf("observe session-ended after logout: %w", err)
	}
	if ended.SessionEnded == nil || ended.SessionEnded.Reason != "signed_out" {
		return fmt.Errorf("logout session-ended payload = %+v", ended.SessionEnded)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	frame, err := stream.Next(ctx)
	if err == nil {
		return fmt.Errorf("live stream after session-ended emitted frame %#v, want EOF", frame)
	}
	if !errors.Is(err, io.EOF) {
		return fmt.Errorf("live stream after session-ended did not reach EOF: %w", err)
	}
	return nil
}

func (s *suite) realtimeUpstreamFault() (resultErr error) {
	s.current = "R07"
	if s.publicWarehouse == nil {
		return errors.New("realtime public warehouse fault fixture is unavailable")
	}
	stream, initial, err := s.openLive()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, stream.Close()) }()
	if initial[3].Upstream == nil || !initial[3].Upstream.Available {
		return errors.New("public warehouse was unavailable before the fault scenario")
	}
	if err := s.publicWarehouse.resetStats(context.Background()); err != nil {
		return err
	}
	failureIDs := []string{"R07-drop-expiration-1", "R07-drop-expiration-2", "R07-drop-expiration-3"}
	for _, id := range failureIDs {
		if err := s.publicWarehouse.arm(context.Background(), faultRule{
			ID: id, Method: http.MethodPost, Path: "/expiration-runs", Action: "drop-response", MaxUses: 1,
		}); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	unavailableEvent, err := waitUpstreamAvailability(ctx, stream, false)
	if err != nil {
		return fmt.Errorf("three failed expiration runs did not report upstream:false: %w", err)
	}
	failureBaseline := 0
	failureEvents := make([]r07FailureEvent, 0, len(failureIDs))
	for _, id := range failureIDs {
		event, err := s.publicWarehouse.wait(context.Background(), id, "dropped", 0)
		if err != nil {
			return fmt.Errorf("upstream:false was emitted before observing fault event %s: %w", id, err)
		}
		failure, err := parseR07FailureEvent(id, event)
		if err != nil {
			return err
		}
		failureEvents = append(failureEvents, failure)
	}
	if err := validateR07Unavailable(unavailableEvent, failureBaseline, failureEvents); err != nil {
		return err
	}
	if err := s.publicWarehouse.arm(context.Background(), faultRule{
		ID: "R07-recovery", Method: http.MethodPost, Path: "/expiration-runs", Action: "hold-before-forward", MaxUses: 1, Timeout: int64(10 * time.Second),
	}); err != nil {
		return err
	}
	captureCtx, cancelCapture := context.WithTimeout(context.Background(), 8*time.Second)
	recoveryCaptured, err := waitFaultEventBeforeUpstreamTrue(captureCtx, stream, s.publicWarehouse, "R07-recovery", "captured", 8*time.Second)
	cancelCapture()
	if err != nil {
		return fmt.Errorf("capture a post-failure recovery run: %w", err)
	}
	if err := ensureNoUpstreamTrueWithin(stream, 100*time.Millisecond); err != nil {
		return err
	}
	if err := s.publicWarehouse.release(context.Background(), "R07-recovery"); err != nil {
		return err
	}
	recoveryResult, err := s.publicWarehouse.wait(context.Background(), "R07-recovery", "upstream-result", 8*time.Second)
	if err != nil {
		return fmt.Errorf("post-failure recovery run did not return to the public warehouse fixture: %w", err)
	}
	recoveryStatus, ok := recoveryResult["statusCode"].(float64)
	if !ok || int(recoveryStatus) != http.StatusOK {
		return fmt.Errorf("post-failure recovery HTTP status = %v, want 200", recoveryResult["statusCode"])
	}
	recoveredCtx, cancelRecovered := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancelRecovered()
	recoveredEvent, err := waitUpstreamAvailability(recoveredCtx, stream, true)
	if err != nil {
		return fmt.Errorf("successful expiration run did not report upstream:true: %w", err)
	}
	recoveryEvidence, err := parseR07RecoveryEvent(recoveryResult)
	if err != nil {
		return err
	}
	if err := validateR07Recovery(recoveredEvent, recoveryEvidence); err != nil {
		return err
	}
	traffic, err := s.publicWarehouse.stats(context.Background(), http.MethodPost, "/expiration-runs")
	if err != nil || traffic.Requests < 4 || traffic.Forwarded < 4 {
		return errorWithCause(fmt.Sprintf("public warehouse expiration fixture saw %d/%d requests, want at least four forwarded calls", traffic.Requests, traffic.Forwarded), err)
	}
	s.record("R07", map[string]string{"unavailableAfterFailures": strconv.Itoa(len(failureEvents)), "recoveredAfterSuccess": strconv.Itoa(int(recoveryStatus)), "recoveryRule": text(recoveryCaptured["ruleID"]), "publicWarehouseRequests": strconv.Itoa(traffic.Requests), "forwarded": strconv.Itoa(traffic.Forwarded)})
	return nil
}

type r07FailureEvent struct {
	RuleID     string
	StatusCode int
	At         time.Time
}

type r07RecoveryEvent struct {
	StatusCode int
	At         time.Time
}

func parseR07FailureEvent(ruleID string, event map[string]any) (r07FailureEvent, error) {
	statusCode, ok := event["statusCode"].(float64)
	if !ok || statusCode != float64(int(statusCode)) {
		return r07FailureEvent{}, fmt.Errorf("fault event %s omitted a valid warehouse status", ruleID)
	}
	at, err := parseR07EventTime(event["at"])
	if err != nil {
		return r07FailureEvent{}, fmt.Errorf("fault event %s has invalid at time: %w", ruleID, err)
	}
	return r07FailureEvent{RuleID: ruleID, StatusCode: int(statusCode), At: at}, nil
}

func parseR07RecoveryEvent(event map[string]any) (r07RecoveryEvent, error) {
	statusCode, ok := event["statusCode"].(float64)
	if !ok || statusCode != float64(int(statusCode)) {
		return r07RecoveryEvent{}, errors.New("recovery fixture event omitted a valid warehouse status")
	}
	at, err := parseR07EventTime(event["at"])
	if err != nil {
		return r07RecoveryEvent{}, fmt.Errorf("recovery fixture event has invalid at time: %w", err)
	}
	return r07RecoveryEvent{StatusCode: int(statusCode), At: at}, nil
}

func parseR07EventTime(value any) (time.Time, error) {
	raw, ok := value.(string)
	if !ok || raw == "" {
		return time.Time{}, errors.New("timestamp is missing")
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || parsed.IsZero() {
		return time.Time{}, errors.New("timestamp is malformed or zero")
	}
	return parsed, nil
}

func validateR07Unavailable(event liveEvent, baseline int, failures []r07FailureEvent) error {
	if event.Upstream == nil || event.Upstream.Available {
		return errors.New("upstream availability event does not report false")
	}
	if event.ReceivedAt.IsZero() {
		return errors.New("upstream:false frame has no receive time")
	}
	if len(failures)-baseline < 3 {
		return fmt.Errorf("upstream unavailable after %d faulted expiration runs from baseline %d, want at least 3", len(failures)-baseline, baseline)
	}
	seen := make(map[string]bool, len(failures))
	for _, failure := range failures {
		if failure.RuleID == "" || seen[failure.RuleID] {
			return errors.New("upstream failure evidence must use distinct fault rules")
		}
		seen[failure.RuleID] = true
		if failure.StatusCode != http.StatusOK {
			return fmt.Errorf("fault rule %s dropped warehouse status %d, want 200", failure.RuleID, failure.StatusCode)
		}
		if failure.At.IsZero() || failure.At.After(event.ReceivedAt) {
			return fmt.Errorf("fault rule %s event at %s did not precede upstream:false receive time %s", failure.RuleID, moscow(failure.At), moscow(event.ReceivedAt))
		}
	}
	return nil
}

func validateR07Recovery(event liveEvent, recovery r07RecoveryEvent) error {
	if event.Upstream == nil || !event.Upstream.Available || recovery.StatusCode != http.StatusOK {
		available := event.Upstream != nil && event.Upstream.Available
		return fmt.Errorf("upstream recovery event=%t warehouse status=%d, want true after HTTP 200", available, recovery.StatusCode)
	}
	if event.ReceivedAt.IsZero() || recovery.At.IsZero() || recovery.At.After(event.ReceivedAt) {
		return fmt.Errorf("recovery HTTP %d at %s did not precede upstream:true receive time %s", recovery.StatusCode, moscow(recovery.At), moscow(event.ReceivedAt))
	}
	return nil
}

func waitFaultEventBeforeUpstreamTrue(parent context.Context, stream *liveStream, fixture *faultControl, id, name string, timeout time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	type eventResult struct {
		event map[string]any
		err   error
	}
	result := make(chan eventResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		event, err := fixture.wait(ctx, id, name, timeout)
		result <- eventResult{event: event, err: err}
	}()
	defer func() {
		cancel()
		<-done
	}()
	for {
		select {
		case received := <-result:
			return received.event, received.err
		default:
		}
		poll, stopPoll := context.WithTimeout(ctx, 100*time.Millisecond)
		frame, err := stream.Next(poll)
		stopPoll()
		if errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if frame.Event != nil && frame.Event.Upstream != nil && frame.Event.Upstream.Available {
			return nil, fmt.Errorf("upstream:true arrived before fixture event %s for rule %s", name, id)
		}
	}
}

func ensureNoUpstreamTrueWithin(stream *liveStream, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		frame, err := stream.Next(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		if err != nil {
			return err
		}
		if frame.Event != nil && frame.Event.Upstream != nil && frame.Event.Upstream.Available {
			return errors.New("upstream:true arrived before the recorded recovery call")
		}
	}
}

func (s *suite) restoreSessionIfNeeded() error {
	result, err := s.call(context.Background(), http.MethodGet, "/api/operator/me", nil)
	if err != nil {
		return err
	}
	if result.status == http.StatusOK {
		return nil
	}
	if result.status != http.StatusUnauthorized {
		return fmt.Errorf("operator session check returned %d after gateway restart", result.status)
	}
	password, err := readPassword(s.cfg.operatorPasswordFile)
	if err != nil {
		return err
	}
	session, _, err := s.signIn(password)
	if err != nil {
		return err
	}
	s.session = session
	return nil
}

func waitChange(ctx context.Context, stream *liveStream, epoch, previous string) (liveChange, error) {
	for {
		frame, err := stream.Next(ctx)
		if err != nil {
			return liveChange{}, err
		}
		if frame.Event == nil || frame.Event.Change == nil {
			continue
		}
		change := *frame.Event.Change
		if change.Epoch != epoch {
			return liveChange{}, fmt.Errorf("live change epoch %q does not match %q", change.Epoch, epoch)
		}
		if changeGreater(change.Seq, previous) {
			return change, nil
		}
	}
}

func waitEvent(ctx context.Context, stream *liveStream, name string) (liveEvent, error) {
	for {
		frame, err := stream.Next(ctx)
		if err != nil {
			return liveEvent{}, err
		}
		if frame.Event != nil && frame.Event.Name == name {
			return *frame.Event, nil
		}
	}
}

func waitUpstreamAvailability(ctx context.Context, stream *liveStream, available bool) (liveEvent, error) {
	for {
		event, err := waitEvent(ctx, stream, "upstream")
		if err != nil {
			return liveEvent{}, err
		}
		if event.Upstream != nil && event.Upstream.Available == available {
			return event, nil
		}
	}
}

func ensureNoChange(ctx context.Context, stream *liveStream) error {
	for {
		frame, err := stream.Next(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		if err != nil {
			return err
		}
		if frame.Event != nil && frame.Event.Change != nil {
			return fmt.Errorf("unexpected change event %s:%s", frame.Event.Change.Epoch, frame.Event.Change.Seq)
		}
	}
}

func ensureNoSessionEvent(ctx context.Context, stream *liveStream) error {
	for {
		frame, err := stream.Next(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		if err != nil {
			return err
		}
		if frame.Event != nil && frame.Event.Name == "session" {
			return errors.New("background GET emitted a session deadline event")
		}
	}
}

func deliveryBatchExpiry(history map[string]any, deliveryID, batchID string) (time.Time, error) {
	for _, value := range array(history["events"]) {
		event := object(value)
		if text(event["type"]) != "DeliveryReceived" || text(object(object(event["payload"])["delivery"])["deliveryID"]) != deliveryID {
			continue
		}
		for _, batch := range array(object(event["payload"])["batches"]) {
			if text(object(batch)["batchID"]) == batchID {
				expiresAt, err := time.Parse(time.RFC3339Nano, text(object(batch)["expiresAt"]))
				if err != nil {
					return time.Time{}, fmt.Errorf("parse batch expiresAt: %w", err)
				}
				return expiresAt, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("history omitted batch %s for delivery %s", batchID, deliveryID)
}

func batchExpiredCount(history map[string]any, batchID string) int {
	count := 0
	for _, value := range array(history["events"]) {
		event := object(value)
		if text(event["type"]) == "BatchExpired" && text(object(object(event["payload"])["expiry"])["batchID"]) == batchID {
			count++
		}
	}
	return count
}

func changeGreater(candidate, baseline string) bool {
	if len(candidate) != len(baseline) {
		return len(candidate) > len(baseline)
	}
	return candidate > baseline
}

func waitChangeWithin(stream *liveStream, epoch, previous string, timeout time.Duration) (liveChange, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return waitChange(ctx, stream, epoch, previous)
}

func ensureNoChangeWithin(stream *liveStream, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return ensureNoChange(ctx, stream)
}
