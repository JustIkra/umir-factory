package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *suite) h08() error {
	if err := s.warehouse.resetStats(context.Background()); err != nil {
		return err
	}
	emptyOne, err := s.proposal("", "")
	if err != nil {
		return err
	}
	emptyTwo, err := s.proposal("", "")
	if err != nil {
		return err
	}
	emptyOneID, err := id(emptyOne, "proposalID")
	if err != nil {
		return err
	}
	emptyTwoID, err := id(emptyTwo, "proposalID")
	if err != nil {
		return err
	}
	empty, err := s.confirm(context.Background(), "empty-command-H08", emptyOneID)
	if err != nil || empty.status != http.StatusOK || text(empty.body["kind"]) != "EmptyOrder" {
		return errorWithCause("initial EmptyOrder", err)
	}
	emptyConflict, err := s.confirm(context.Background(), "empty-command-H08", emptyTwoID)
	if err != nil || !isDefiniteCommandIdentityConflict(emptyConflict) {
		return errorWithCause("EmptyOrder cross-proposal reuse was not rejected", err)
	}

	seqProduct, seqDish := "product-H08-sequential", "dish-H08-sequential"
	if err := seedOrderScenario(s, "H08-sequential", seqProduct, seqDish, "3000"); err != nil {
		return err
	}
	seqOne, err := s.proposal(seqDish, "1")
	if err != nil {
		return err
	}
	seqTwo, err := s.proposal(seqDish, "1")
	if err != nil {
		return err
	}
	seqOneID, err := id(seqOne, "proposalID")
	if err != nil {
		return err
	}
	seqTwoID, err := id(seqTwo, "proposalID")
	if err != nil {
		return err
	}
	seqFirst, err := s.confirm(context.Background(), "seq-command-H08", seqOneID)
	if err != nil || seqFirst.status != http.StatusOK || text(seqFirst.body["kind"]) != "OrderConfirmed" {
		return errorWithCause("sequential nonempty command winner failed", err)
	}
	seqLoser, err := s.confirm(context.Background(), "seq-command-H08", seqTwoID)
	if err != nil || !isDefiniteCommandIdentityConflict(seqLoser) {
		return errorWithCause("sequential cross-proposal reuse was not a definite conflict", err)
	}
	seqReplay, err := s.confirm(context.Background(), "seq-command-H08", seqOneID)
	if err != nil || seqReplay.status != http.StatusOK || text(object(seqReplay.body["order"])["orderID"]) != text(object(seqFirst.body["order"])["orderID"]) {
		return errorWithCause("sequential original command did not replay", err)
	}

	conProduct, conDish := "product-H08-concurrent", "dish-H08-concurrent"
	if err := seedOrderScenario(s, "H08-concurrent", conProduct, conDish, "3000"); err != nil {
		return err
	}
	conOne, err := s.proposal(conDish, "1")
	if err != nil {
		return err
	}
	conTwo, err := s.proposal(conDish, "1")
	if err != nil {
		return err
	}
	conOneID, err := id(conOne, "proposalID")
	if err != nil {
		return err
	}
	conTwoID, err := id(conTwo, "proposalID")
	if err != nil {
		return err
	}
	type attempt struct {
		proposal string
		result   response
		err      error
	}
	results := make(chan attempt, 2)
	for _, proposalID := range []string{conOneID, conTwoID} {
		go func(proposalID string) {
			result, err := s.confirm(context.Background(), "concurrent-command-H08", proposalID)
			results <- attempt{proposal: proposalID, result: result, err: err}
		}(proposalID)
	}
	var winner string
	var accepted int
	var conflicts int
	for range 2 {
		attempt := <-results
		if attempt.err != nil {
			return attempt.err
		}
		if attempt.result.status == http.StatusOK && text(attempt.result.body["kind"]) == "OrderConfirmed" {
			winner = attempt.proposal
			accepted++
			continue
		}
		if isPendingConfirmationResult(attempt.result) {
			winner = attempt.proposal
			accepted++
			continue
		}
		if isDefiniteCommandIdentityConflict(attempt.result) {
			conflicts++
			continue
		}
		return fmt.Errorf("concurrent C1 result=%d %s", attempt.result.status, attempt.result.bytes)
	}
	if accepted != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent cross-proposal reuse accepted=%d conflicts=%d", accepted, conflicts)
	}
	resolved, err := s.resolve("concurrent-command-H08", winner)
	if err != nil || resolved.status != http.StatusOK || text(resolved.body["kind"]) != "OrderConfirmed" {
		return errorWithCause("concurrent winner did not resolve to one sale", err)
	}

	refusalProduct, refusalDish := "product-H08-refusal", "dish-H08-refusal"
	if err := seedOrderScenario(s, "H08-refusal", refusalProduct, refusalDish, "1000"); err != nil {
		return err
	}
	refusalProposal, err := s.proposal(refusalDish, "1")
	if err != nil {
		return err
	}
	refusalProposalID, err := id(refusalProposal, "proposalID")
	if err != nil {
		return err
	}
	refusal, err := s.confirm(context.Background(), "foreign-command-H17", refusalProposalID)
	if err != nil || !isDefiniteCommandIdentityConflict(refusal) {
		return errorWithCause("cross-proposal reuse did not replay a prior definite refusal", err)
	}
	refusalReplay, err := s.confirm(context.Background(), "foreign-command-H17", refusalProposalID)
	if err != nil || !isDefiniteCommandIdentityConflict(refusalReplay) || refusalReplay.status != refusal.status || text(refusalReplay.body["code"]) != text(refusal.body["code"]) {
		return errorWithCause("definite refusal changed on exact retry", err)
	}
	allowed, err := s.confirm(context.Background(), "refusal-recovery-H08", refusalProposalID)
	if err != nil || allowed.status != http.StatusOK || text(allowed.body["kind"]) != "OrderConfirmed" {
		return errorWithCause("new command after definite refusal failed", err)
	}

	history, err := s.history()
	if err != nil {
		return err
	}
	for _, command := range []string{"seq-command-H08", "concurrent-command-H08", "refusal-recovery-H08"} {
		if eventCountForCommand(history, "OrderPlaced", command) != 1 {
			return fmt.Errorf("%s did not create exactly one OrderPlaced event", command)
		}
	}
	for _, productID := range []string{seqProduct, conProduct, refusalProduct} {
		stock, err := s.inventory()
		if err != nil || s.quantity(stock, productID) != "2000" && productID != refusalProduct {
			return errorWithCause(fmt.Sprintf("stock after C1 outcomes for %s=%s", productID, s.quantity(stock, productID)), err)
		}
		if productID == refusalProduct && s.quantity(stock, productID) != "0" {
			return fmt.Errorf("stock after refusal recovery=%s, want 0", s.quantity(stock, productID))
		}
	}
	traffic, err := s.warehouse.stats(context.Background(), http.MethodPost, "/internal/v1/order-commits")
	if err != nil || traffic.Requests != traffic.Forwarded || traffic.Requests != 3 {
		return errorWithCause(fmt.Sprintf("H08 warehouse forwarding=%d/%d, want three owned attempts", traffic.Requests, traffic.Forwarded), err)
	}
	s.record("H08", map[string]string{"emptyOrderCrossProposal": "definite conflict", "sequentialNonempty": "one winner; definite conflict and exact replay", "concurrentNonempty": "one winner; one definite conflict", "definiteRefusalReuse": "prior saved refusal replayed; fresh command confirmed", "stockAfterWinner": "one portion consumed per nonempty case", "orderPlacedPerWinningCommand": "1", "warehouseCommitRequests": strconv.Itoa(traffic.Requests), "warehouseCommitForwarded": strconv.Itoa(traffic.Forwarded)})
	return nil
}

func isDefiniteCommandIdentityConflict(result response) bool {
	return result.status == http.StatusConflict && text(result.body["code"]) == "CommandIdentityConflict" && text(result.body["outcome"]) == "not_committed"
}

func isPendingConfirmationResult(result response) bool {
	return result.status == http.StatusAccepted && text(result.body["kind"]) == "ConfirmationPending"
}

func seedOrderScenario(s *suite, label, productID, dishID, stock string) error {
	if err := s.product(label, productID); err != nil {
		return err
	}
	if err := s.dish(label, dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	_, err = s.delivery(label, "delivery-"+label, productID, stock, now.Add(-time.Hour), now)
	return err
}

func (s *suite) h20() error {
	started := time.Now()
	status, body, requestErr := rawGatewayRequest(s.cfg.gatewayURL, "POST", "/api/serving/products", s.session.rawHeaders(http.MethodPost)+"Content-Type: application/json\r\nContent-Length: 96\r\n", `{"commandID":"upload-H20","productID":"partial-H20"`, 3*time.Second)
	if time.Since(started) > 4*time.Second {
		return errors.New("stalled upload did not reach a finite connection outcome")
	}
	if requestErr == nil && status >= 200 && status < 300 {
		return fmt.Errorf("incomplete upload returned success: %s", body)
	}
	if strings.Contains(string(body), `"outcome":"not_committed"`) {
		return errors.New("incomplete upload reported not_committed after an unknown result")
	}
	products, err := s.expect(http.MethodGet, "/api/serving/products", nil, http.StatusOK)
	if err != nil || contains(array(products.body["items"]), "productID", "partial-H20") {
		return errorWithCause("incomplete upload mutated the product catalog", err)
	}

	productID, dishID := "product-H20-timeout", "dish-H20-timeout"
	if err := seedOrderScenario(s, "H20-timeout", productID, dishID, "1000"); err != nil {
		return err
	}
	proposal, err := s.proposal(dishID, "1")
	if err != nil {
		return err
	}
	proposalID, err := id(proposal, "proposalID")
	if err != nil {
		return err
	}
	if err := s.warehouse.arm(context.Background(), faultRule{ID: "H20-held-commit", Method: http.MethodPost, Path: "/internal/v1/order-commits", Action: "hold-before-forward", MaxUses: 1, Timeout: int64(12 * time.Second), DetachOnRelease: true, DetachedTimeout: int64(8 * time.Second)}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	resultCh := make(chan attemptResult, 1)
	go func() {
		result, callErr := s.confirm(ctx, "timeout-H20", proposalID)
		resultCh <- attemptResult{result: result, err: callErr}
	}()
	if _, err := s.warehouse.wait(context.Background(), "H20-held-commit", "captured", 3*time.Second); err != nil {
		cancel()
		return err
	}
	first := <-resultCh
	cancel()
	if first.err == nil && text(first.result.body["outcome"]) == "not_committed" {
		return errors.New("timed-out held commit was falsely reported as not_committed")
	}
	pending, err := s.resolve("timeout-H20", proposalID)
	if err != nil || pending.status != http.StatusAccepted || text(pending.body["kind"]) != "ConfirmationPending" || text(pending.body["outcome"]) == "not_committed" {
		return errorWithCause("timed-out commit did not remain pending/unknown", err)
	}
	if err := s.warehouse.release(context.Background(), "H20-held-commit"); err != nil {
		return err
	}
	if _, err := s.warehouse.wait(context.Background(), "H20-held-commit", "upstream-result", 8*time.Second); err != nil {
		return err
	}
	resolved, err := s.resolve("timeout-H20", proposalID)
	if err != nil || resolved.status != http.StatusOK || text(resolved.body["kind"]) != "OrderConfirmed" {
		return errorWithCause("timed-out commit did not resolve after release", err)
	}
	history, err := s.history()
	if err != nil || eventCountForCommand(history, "OrderPlaced", "timeout-H20") != 1 {
		return errorWithCause("timeout recovery produced other than one sale", err)
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "0" {
		return errorWithCause("timeout recovery stock is inconsistent", err)
	}
	traffic, err := s.warehouse.stats(context.Background(), http.MethodPost, "/internal/v1/order-commits")
	if err != nil || traffic.Requests != 1 || traffic.Forwarded != 1 {
		return errorWithCause(fmt.Sprintf("timeout commit forwarding was %d/%d", traffic.Requests, traffic.Forwarded), err)
	}
	s.record("H20", map[string]string{"stalledContentLengthUpload": "finite connection outcome; no catalog write", "heldCommitTimeout": "pending/unknown", "resolveAfterRelease": "OrderConfirmed", "orderPlaced": "1", "warehouseCommitRequests": strconv.Itoa(traffic.Requests), "warehouseCommitForwarded": strconv.Itoa(traffic.Forwarded)})
	return nil
}

type attemptResult struct {
	result response
	err    error
}

func rawGatewayRequest(base, method, path, headers, body string, timeout time.Duration) (status int, result []byte, resultErr error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return 0, nil, err
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, timeout)
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, closeErr)
		}
	}()
	if _, err := fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\n%sConnection: close\r\n\r\n%s", method, path, parsed.Host, headers, body); err != nil {
		return 0, nil, err
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return 0, nil, err
	}
	request := &http.Request{Method: method}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, closeErr)
		}
	}()
	result, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, result, err
}

func (s *suite) h13() error {
	productID, dishID := "product-H13", "dish-H13"
	if err := s.product("H13", productID); err != nil {
		return err
	}
	if err := s.dish("H13", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	if _, err := s.delivery("H13", "delivery-H13", productID, "1000", now.Add(-time.Hour), now); err != nil {
		return err
	}
	proposal, err := s.proposal(dishID, "1")
	if err != nil {
		return err
	}
	proposalID, err := id(proposal, "proposalID")
	if err != nil {
		return err
	}
	if err := s.warehouse.arm(context.Background(), faultRule{ID: "H13-drop", Method: http.MethodPost, Path: "/internal/v1/order-commits", Action: "drop-response", MaxUses: 1}); err != nil {
		return err
	}
	first, callErr := s.confirm(context.Background(), "confirm-H13", proposalID)
	if callErr == nil && first.status == http.StatusOK && text(first.body["kind"]) == "OrderConfirmed" {
		return errors.New("dropped warehouse response was reported as confirmed")
	}
	if _, err := s.warehouse.wait(context.Background(), "H13-drop", "upstream-result", 3*time.Second); err != nil {
		return err
	}
	resolved, err := s.resolve("confirm-H13", proposalID)
	if err != nil || resolved.status != http.StatusOK || text(resolved.body["kind"]) != "OrderConfirmed" {
		return errorWithCause("passive resolve did not recover saved order", err)
	}
	history, err := s.history()
	if err != nil || eventCountForCommand(history, "OrderPlaced", "confirm-H13") != 1 {
		return errorWithCause("lost response caused duplicate sale", err)
	}
	traffic, err := s.warehouse.stats(context.Background(), http.MethodPost, "/internal/v1/order-commits")
	if err != nil || traffic.Requests != 1 || traffic.Forwarded != 1 {
		return errorWithCause(fmt.Sprintf("H13 internal commit forwarding was %d/%d", traffic.Requests, traffic.Forwarded), err)
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "0" {
		return errorWithCause("H13 final stock is inconsistent", err)
	}
	s.record("H13", map[string]string{"fault": "warehouse committed; response dropped", "resolve": "OrderConfirmed", "orderPlaced": "1", "stockBefore": "1000", "stockAfter": s.quantity(stock, productID), "commitRequests": strconv.Itoa(traffic.Requests), "commitForwarded": strconv.Itoa(traffic.Forwarded)})
	return nil
}

func (s *suite) h14() error {
	productID, dishID := "product-H14", "dish-H14"
	if err := s.product("H14", productID); err != nil {
		return err
	}
	if err := s.dish("H14", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	if _, err := s.delivery("H14", "delivery-H14", productID, "1000", now.Add(-time.Hour), now); err != nil {
		return err
	}
	one, err := s.proposal(dishID, "1")
	if err != nil {
		return err
	}
	two, err := s.proposal(dishID, "1")
	if err != nil {
		return err
	}
	oneID, err := id(one, "proposalID")
	if err != nil {
		return err
	}
	twoID, err := id(two, "proposalID")
	if err != nil {
		return err
	}
	if result, err := s.confirm(context.Background(), "consume-H14", twoID); err != nil || result.status != http.StatusOK {
		return errorWithCause("consume stock before old proposal", err)
	}
	if err := s.warehouse.arm(context.Background(), faultRule{ID: "H14-drop", Method: http.MethodPost, Path: "/internal/v1/order-commits", Action: "drop-response", MaxUses: 1}); err != nil {
		return err
	}
	first, callErr := s.confirm(context.Background(), "confirm-H14", oneID)
	if callErr == nil && first.status == http.StatusOK && text(first.body["kind"]) == "RequiresConfirmation" {
		return errors.New("lost replacement response was reported as completed")
	}
	if _, err := s.warehouse.wait(context.Background(), "H14-drop", "upstream-result", 3*time.Second); err != nil {
		return err
	}
	replay, err := s.confirm(context.Background(), "confirm-H14", oneID)
	if err != nil || replay.status != http.StatusOK || text(replay.body["kind"]) != "RequiresConfirmation" {
		return errorWithCause("exact retry did not replay replacement", err)
	}
	replacement, err := id(object(replay.body["proposal"]), "proposalID")
	if err != nil {
		return err
	}
	stockAfterOrder, err := s.inventory()
	if err != nil || s.quantity(stockAfterOrder, productID) != "0" {
		return errorWithCause("H14 recheck source stock is inconsistent", err)
	}
	if _, err := s.delivery("H14-restock", "delivery-H14-restock", productID, "1000", now.Add(-time.Hour), now); err != nil {
		return err
	}
	again, err := s.resolve("confirm-H14", oneID)
	if err != nil || again.status != http.StatusOK || text(again.body["kind"]) != "RequiresConfirmation" || text(object(again.body["proposal"])["proposalID"]) != replacement {
		return errorWithCause("replacement changed after restock", err)
	}
	history, err := s.history()
	if err != nil || eventCountForProposal(history, "OrderPlaced", twoID) != 1 {
		return errorWithCause("replacement retry created another order", err)
	}
	traffic, err := s.warehouse.stats(context.Background(), http.MethodPost, "/internal/v1/order-commits")
	if err != nil || traffic.Requests != 2 || traffic.Forwarded != 2 {
		return errorWithCause(fmt.Sprintf("H14 internal commit forwarding was %d/%d", traffic.Requests, traffic.Forwarded), err)
	}
	stockAfterRestock, err := s.inventory()
	if err != nil || s.quantity(stockAfterRestock, productID) != "1000" {
		return errorWithCause("H14 restock balance is inconsistent", err)
	}
	s.record("H14", map[string]string{"replacement": replacement, "retry": "same RequiresConfirmation", "stockBefore": "1000", "stockAfterSale": s.quantity(stockAfterOrder, productID), "stockAfterRestock": s.quantity(stockAfterRestock, productID), "commitRequests": strconv.Itoa(traffic.Requests), "commitForwarded": strconv.Itoa(traffic.Forwarded), "orderPlaced": "1"})
	return nil
}

func (s *suite) h15() error {
	productID, dishID := "product-H15", "dish-H15"
	if err := s.product("H15", productID); err != nil {
		return err
	}
	if err := s.dish("H15", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	if _, err := s.delivery("H15", "delivery-H15", productID, "1000", now.Add(-time.Hour), now); err != nil {
		return err
	}
	one, err := s.proposal(dishID, "1")
	if err != nil {
		return err
	}
	two, err := s.proposal(dishID, "1")
	if err != nil {
		return err
	}
	oneID, err := id(one, "proposalID")
	if err != nil {
		return err
	}
	twoID, err := id(two, "proposalID")
	if err != nil {
		return err
	}
	if err := s.warehouse.arm(context.Background(), faultRule{ID: "H15-hold", Method: http.MethodPost, Path: "/internal/v1/order-commits", Action: "hold-before-forward", MaxUses: 1, Timeout: int64(20 * time.Second), DetachOnRelease: true, DetachedTimeout: int64(10 * time.Second)}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	resultCh := make(chan error, 1)
	go func() {
		result, err := s.confirm(ctx, "confirm-H15", oneID)
		if err == nil && result.status == http.StatusOK {
			err = errors.New("cancelled attempt returned a completed result")
		}
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		resultCh <- err
	}()
	if _, err := s.warehouse.wait(context.Background(), "H15-hold", "captured", 3*time.Second); err != nil {
		cancel()
		return err
	}
	initialErr := <-resultCh
	cancel()
	if initialErr == nil {
		return errors.New("caller timeout did not cancel held request")
	}
	if result, err := s.confirm(context.Background(), "consume-H15", twoID); err != nil || result.status != http.StatusOK {
		return errorWithCause("competing order did not consume stock", err)
	}
	retry, err := s.confirm(context.Background(), "confirm-H15", oneID)
	if err != nil || retry.status != http.StatusOK || text(retry.body["kind"]) != "RequiresConfirmation" {
		return errorWithCause("retry did not complete the recheck", err)
	}
	replacement, err := id(object(retry.body["proposal"]), "proposalID")
	if err != nil {
		return err
	}
	if _, err := s.delivery("H15-restock", "delivery-H15-restock", productID, "1000", now.Add(-time.Hour), now); err != nil {
		return err
	}
	if err := s.warehouse.release(context.Background(), "H15-hold"); err != nil {
		return err
	}
	if _, err := s.warehouse.wait(context.Background(), "H15-hold", "upstream-result", 10*time.Second); err != nil {
		return err
	}
	replay, err := s.confirm(context.Background(), "confirm-H15", oneID)
	if err != nil || replay.status != http.StatusOK || text(replay.body["kind"]) != "RequiresConfirmation" || text(object(replay.body["proposal"])["proposalID"]) != replacement {
		return errorWithCause("late original changed saved replacement", err)
	}
	history, err := s.history()
	if err != nil {
		return fmt.Errorf("read H15 history: %w", err)
	}
	inventory, err := s.inventory()
	if err != nil {
		return fmt.Errorf("read H15 inventory: %w", err)
	}
	traffic, err := s.warehouse.stats(context.Background(), http.MethodPost, "/internal/v1/order-commits")
	if err != nil {
		return fmt.Errorf("read H15 warehouse forwarding stats: %w", err)
	}
	originalOrders := eventCountForCommand(history, "OrderPlaced", "confirm-H15")
	competitorOrders := eventCountForCommand(history, "OrderPlaced", "consume-H15")
	totalOrders := eventCount(history, "OrderPlaced")
	if originalOrders != 0 || competitorOrders != 1 || totalOrders != 1 {
		return fmt.Errorf("H15 order accounting mismatch originalProposal=%s competitorProposal=%s originalCommandOrders=%d competitorCommandOrders=%d totalOrderPlaced=%d events=%s inventory=%s warehouseCommitRequests=%d warehouseCommitForwarded=%d",
			oneID, twoID, originalOrders, competitorOrders, totalOrders, evidenceJSON(history["events"]), evidenceJSON(inventory), traffic.Requests, traffic.Forwarded)
	}
	if err != nil || traffic.Requests != 3 || traffic.Forwarded != 3 {
		return errorWithCause(fmt.Sprintf("H15 delayed commit forwarding was %d/%d", traffic.Requests, traffic.Forwarded), err)
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "1000" {
		return errorWithCause("H15 late retry changed the restocked balance", err)
	}
	s.record("H15", map[string]string{"original": oneID, "replacement": replacement, "competitor": twoID, "lateOriginal": "replayed without a sale", "warehouseRPCs": "held original 1; competitor 1; exact C1 retry 1; cached public replay 0", "stockBefore": "1000", "stockAfterCompetitor": "0", "stockAfterRestock": s.quantity(stock, productID), "originalOrderPlaced": "0", "competitorOrderPlaced": "1", "totalOrderPlaced": strconv.Itoa(eventCount(history, "OrderPlaced")), "commitRequests": strconv.Itoa(traffic.Requests), "commitForwarded": strconv.Itoa(traffic.Forwarded)})
	return nil
}

func evidenceJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("<encode evidence: %v>", err)
	}
	return string(data)
}

func errorText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func (s *suite) h17() error {
	productID, dishID := "product-H17", "dish-H17"
	if err := s.product("H17", productID); err != nil {
		return err
	}
	if err := s.dish("H17", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	if _, err := s.delivery("H17", "delivery-H17", productID, "1000", now.Add(-time.Hour), now); err != nil {
		return err
	}
	proposal, err := s.proposal(dishID, "1")
	if err != nil {
		return err
	}
	proposalID, err := id(proposal, "proposalID")
	if err != nil {
		return err
	}
	commandID := "foreign-command-H17"
	if err := s.warehouse.arm(context.Background(), faultRule{ID: "H17-hold", Method: http.MethodPost, Path: "/internal/v1/order-commits", Action: "hold-before-forward", MaxUses: 1, Timeout: int64(20 * time.Second), DetachOnRelease: true, DetachedTimeout: int64(10 * time.Second)}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	resultCh := make(chan error, 1)
	go func() {
		result, err := s.confirm(ctx, commandID, proposalID)
		if err == nil && result.status == http.StatusOK {
			err = errors.New("held original unexpectedly succeeded")
		}
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		resultCh <- err
	}()
	if _, err := s.warehouse.wait(context.Background(), "H17-hold", "captured", 3*time.Second); err != nil {
		cancel()
		return err
	}
	if err := <-resultCh; err == nil {
		cancel()
		return errors.New("caller timeout did not cancel held original")
	}
	cancel()
	_, registerErr := s.expect(http.MethodPost, "/api/serving/products", map[string]any{"commandID": commandID, "productID": "product-H17-collision", "name": "Collision", "baseUnit": "g"}, http.StatusOK)
	if registerErr != nil {
		return fmt.Errorf("record colliding command before warehouse admission: %w", registerErr)
	}
	if err := s.warehouse.release(context.Background(), "H17-hold"); err != nil {
		return err
	}
	if _, err := s.warehouse.wait(context.Background(), "H17-hold", "upstream-result", 10*time.Second); err != nil {
		return err
	}
	refusal, err := s.confirm(context.Background(), commandID, proposalID)
	if err != nil || refusal.status != http.StatusConflict || text(refusal.body["code"]) != "CommandIdentityConflict" || text(refusal.body["outcome"]) != "not_committed" {
		return fmt.Errorf("H17 late collision was not a definite refusal command=%s proposal=%s status=%d body=%s error=%s", commandID, proposalID, refusal.status, refusal.bytes, errorText(err))
	}
	replay, err := s.confirm(context.Background(), commandID, proposalID)
	if err != nil || !isDefiniteCommandIdentityConflict(replay) || replay.status != refusal.status || text(replay.body["code"]) != text(refusal.body["code"]) {
		return fmt.Errorf("H17 terminal refusal changed on exact retry command=%s proposal=%s status=%d body=%s error=%s", commandID, proposalID, replay.status, replay.bytes, errorText(err))
	}
	allowed, err := s.confirm(context.Background(), "confirm-H17-C2", proposalID)
	initialAllowed := allowed
	resolvedAllowed := response{}
	var resolveErr error
	if err == nil && isPendingConfirmationResult(allowed) {
		resolvedAllowed, resolveErr = s.resolve("confirm-H17-C2", proposalID)
		if resolveErr == nil {
			allowed = resolvedAllowed
		}
	}
	if err != nil || allowed.status != http.StatusOK || text(allowed.body["kind"]) != "OrderConfirmed" {
		history, historyErr := s.history()
		inventory, inventoryErr := s.inventory()
		traffic, trafficErr := s.warehouse.stats(context.Background(), http.MethodPost, "/internal/v1/order-commits")
		return fmt.Errorf("H17 new command after refusal was rejected command=%s proposal=%s initialStatus=%d initialBody=%s resolveStatus=%d resolveBody=%s confirmError=%s resolveError=%s refusal=%d/%s replay=%d/%s historyError=%s inventoryError=%s trafficError=%s ordersC1=%d ordersC2=%d totalOrders=%d events=%s inventory=%s commitRequests=%d commitForwarded=%d",
			"confirm-H17-C2", proposalID, initialAllowed.status, initialAllowed.bytes, resolvedAllowed.status, resolvedAllowed.bytes, errorText(err), errorText(resolveErr), refusal.status, refusal.bytes, replay.status, replay.bytes, errorText(historyErr), errorText(inventoryErr), errorText(trafficErr),
			eventCountForCommand(history, "OrderPlaced", commandID), eventCountForCommand(history, "OrderPlaced", "confirm-H17-C2"), eventCount(history, "OrderPlaced"), evidenceJSON(history["events"]), evidenceJSON(inventory), traffic.Requests, traffic.Forwarded)
	}
	history, err := s.history()
	if err != nil || eventCountForCommand(history, "OrderPlaced", commandID) != 0 || eventCountForCommand(history, "OrderPlaced", "confirm-H17-C2") != 1 || eventCount(history, "OrderPlaced") != 1 {
		return errorWithCause("saved refusal recorded an order", err)
	}
	traffic, err := s.warehouse.stats(context.Background(), http.MethodPost, "/internal/v1/order-commits")
	if err != nil || traffic.Requests != 2 || traffic.Forwarded != 2 {
		return errorWithCause(fmt.Sprintf("H17 commit forwarding was %d/%d", traffic.Requests, traffic.Forwarded), err)
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "0" {
		return errorWithCause("H17 saved refusal changed stock", err)
	}
	s.record("H17", map[string]string{"refusal": text(refusal.body["code"]), "exactRetry": "same refusal; both C1 reads make 0 order-commit calls", "warehouseRPCs": "held original 1; saved refusal retry/replay 0; fresh C2 1", "newCommand": "OrderConfirmed", "originalOrderPlaced": "0", "newCommandOrderPlaced": "1", "totalOrderPlaced": strconv.Itoa(eventCount(history, "OrderPlaced")), "stockBefore": "1000", "stockAfter": s.quantity(stock, productID), "commitRequests": strconv.Itoa(traffic.Requests), "commitForwarded": strconv.Itoa(traffic.Forwarded)})
	return nil
}

func (s *suite) h18() error {
	productID, dishID := "product-H18", "dish-H18"
	if err := s.product("H18", productID); err != nil {
		return err
	}
	if err := s.dish("H18", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	if _, err := s.delivery("H18", "delivery-H18", productID, "1000", now.Add(-time.Hour), now); err != nil {
		return err
	}
	proposal, err := s.proposal(dishID, "1")
	if err != nil {
		return err
	}
	proposalID, err := id(proposal, "proposalID")
	if err != nil {
		return err
	}
	unseen, err := s.resolve("unseen-H18", proposalID)
	if err != nil || unseen.status != http.StatusNotFound || text(unseen.body["outcome"]) != "unknown" {
		return errorWithCause("Unseen resolve claimed a terminal result", err)
	}
	if err := s.warehouse.arm(context.Background(), faultRule{ID: "H18-pending", Method: http.MethodPost, Path: "/internal/v1/order-commits", Action: "hold-before-forward", MaxUses: 1, Timeout: int64(10 * time.Second), DetachOnRelease: true, DetachedTimeout: int64(5 * time.Second)}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	resultCh := make(chan error, 1)
	go func() {
		_, callErr := s.confirm(ctx, "pending-H18", proposalID)
		if callErr == nil && ctx.Err() != nil {
			callErr = ctx.Err()
		}
		resultCh <- callErr
	}()
	if _, err := s.warehouse.wait(context.Background(), "H18-pending", "captured", 3*time.Second); err != nil {
		cancel()
		return err
	}
	if err := <-resultCh; err == nil {
		cancel()
		return errors.New("pending request was not cancelled")
	}
	cancel()
	pending, err := s.resolve("pending-H18", proposalID)
	if err != nil || pending.status != http.StatusAccepted || text(pending.body["kind"]) != "ConfirmationPending" {
		return errorWithCause("Pending resolve changed the attempt", err)
	}
	if err := s.warehouse.release(context.Background(), "H18-pending"); err != nil {
		return err
	}
	if _, err := s.warehouse.wait(context.Background(), "H18-pending", "upstream-result", 8*time.Second); err != nil {
		return err
	}
	resolved, err := s.resolve("pending-H18", proposalID)
	if err != nil || resolved.status != http.StatusOK || text(resolved.body["kind"]) != "OrderConfirmed" {
		return errorWithCause("completed held attempt did not resolve", err)
	}
	history, err := s.history()
	if err != nil || eventCountForCommand(history, "OrderPlaced", "pending-H18") != 1 {
		return errorWithCause("passive Resolve started a second commit", err)
	}
	traffic, err := s.warehouse.stats(context.Background(), http.MethodPost, "/internal/v1/order-commits")
	if err != nil || traffic.Requests != 1 || traffic.Forwarded != 1 {
		return errorWithCause(fmt.Sprintf("H18 Resolve forwarded another commit (%d/%d)", traffic.Requests, traffic.Forwarded), err)
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "0" {
		return errorWithCause("H18 passive resolve changed stock unexpectedly", err)
	}
	s.record("H18", map[string]string{"Unseen": text(unseen.body["outcome"]), "Pending": text(pending.body["kind"]), "resolveCommitCalls": "0", "Completed": text(resolved.body["kind"]), "stockBefore": "1000", "stockAfter": s.quantity(stock, productID), "commitRequests": strconv.Itoa(traffic.Requests), "commitForwarded": strconv.Itoa(traffic.Forwarded), "orderPlaced": "1"})
	return nil
}

func (s *suite) h19() error {
	productID, dishID := "product-H19", "dish-H19"
	if err := s.product("H19", productID); err != nil {
		return err
	}
	if err := s.dish("H19", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	if _, err := s.delivery("H19", "delivery-H19", productID, "1000", now.Add(-time.Hour), now); err != nil {
		return err
	}
	proposal, err := s.proposal(dishID, "1")
	if err != nil {
		return err
	}
	proposalID, err := id(proposal, "proposalID")
	if err != nil {
		return err
	}
	if err := s.warehouse.arm(context.Background(), faultRule{ID: "H19-corrupt", Method: http.MethodPost, Path: "/internal/v1/order-commits", Action: "corrupt-kind", MaxUses: 1}); err != nil {
		return err
	}
	first, callErr := s.confirm(context.Background(), "confirm-H19", proposalID)
	if callErr == nil && first.status == http.StatusOK && text(first.body["kind"]) == "OrderConfirmed" {
		return errors.New("corrupted warehouse result was accepted")
	}
	if _, err := s.warehouse.wait(context.Background(), "H19-corrupt", "upstream-result", 3*time.Second); err != nil {
		return err
	}
	resolved, err := s.resolve("confirm-H19", proposalID)
	if err != nil || resolved.status != http.StatusOK || text(resolved.body["kind"]) != "OrderConfirmed" {
		return errorWithCause("malformed result did not resolve", err)
	}
	history, err := s.history()
	if err != nil || eventCountForCommand(history, "OrderPlaced", "confirm-H19") != 1 {
		return errorWithCause("corrupt response caused duplicate write", err)
	}
	traffic, err := s.warehouse.stats(context.Background(), http.MethodPost, "/internal/v1/order-commits")
	if err != nil || traffic.Requests != 1 || traffic.Forwarded != 1 {
		return errorWithCause(fmt.Sprintf("H19 commit forwarding was %d/%d", traffic.Requests, traffic.Forwarded), err)
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "0" {
		return errorWithCause("H19 malformed response changed saved stock", err)
	}
	s.record("H19", map[string]string{"fault": "unknown kind after warehouse result", "resolve": "OrderConfirmed", "stockBefore": "1000", "stockAfter": s.quantity(stock, productID), "commitRequests": strconv.Itoa(traffic.Requests), "commitForwarded": strconv.Itoa(traffic.Forwarded), "orderPlaced": "1"})
	return nil
}
