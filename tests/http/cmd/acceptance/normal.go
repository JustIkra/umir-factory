package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *suite) h01() error {
	products, err := s.expect(http.MethodGet, "/api/serving/products", nil, http.StatusOK)
	if err != nil || len(array(products.body["items"])) != 0 {
		return errorWithCause("empty products", err)
	}
	dishes, err := s.expect(http.MethodGet, "/api/serving/dish-catalog", nil, http.StatusOK)
	if err != nil || len(array(dishes.body["items"])) != 0 {
		return errorWithCause("empty dish catalog", err)
	}
	menu, err := s.expect(http.MethodGet, "/api/serving/menu", nil, http.StatusOK)
	if err != nil || len(array(menu.body["dishes"])) != 0 {
		return errorWithCause("empty menu", err)
	}
	stock, err := s.inventory()
	if err != nil || len(array(stock["products"])) != 0 {
		return errorWithCause("empty inventory", err)
	}
	history, err := s.history()
	if err != nil || len(array(history["events"])) != 0 || text(history["stateVersion"]) != "0" {
		return errorWithCause("empty history/version", err)
	}
	if !strings.HasSuffix(text(products.body["evaluatedAt"]), "+03:00") || !strings.HasSuffix(text(stock["evaluatedAt"]), "+03:00") {
		return errors.New("empty-start response omitted Moscow timestamps")
	}
	s.record("H01", map[string]string{"products": "0", "dishes": "0", "stock": "0", "history": "0"})
	return nil
}

func (s *suite) h02() error {
	productID, dishID := "сыр/+ &? H02", "паста/+ &? H02"
	if err := s.product("H02", productID); err != nil {
		return err
	}
	if err := s.dish("H02", dishID, productID, "40000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	batchID, err := s.delivery("H02", "delivery-H02", productID, "80000", now.Add(-time.Hour), now)
	if err != nil {
		return err
	}
	query := url.Values{}
	query.Set("dishID", dishID)
	dish, err := s.expect(http.MethodGet, "/api/serving/dish?"+query.Encode(), nil, http.StatusOK)
	if err != nil || text(object(dish.body["dish"])["dishID"]) != dishID {
		return errorWithCause("opaque dish query failed", err)
	}
	menu, err := s.expect(http.MethodGet, "/api/serving/menu", nil, http.StatusOK)
	if err != nil || !contains(array(menu.body["dishes"]), "dishID", dishID) {
		return errorWithCause("menu did not expose registered dish", err)
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "80000" {
		return errorWithCause(fmt.Sprintf("stock after delivery = %s", s.quantity(stock, productID)), err)
	}
	if !strings.HasSuffix(text(menu.body["evaluatedAt"]), "+03:00") {
		return errors.New("menu timestamp is not Moscow time")
	}
	s.record("H02", map[string]string{"productID": productID, "dishID": dishID, "batchID": batchID, "stockBefore": "0", "stockAfter": s.quantity(stock, productID), "menu": "available"})
	return nil
}

func (s *suite) h03() error {
	productID, dishID := "product-H03", "dish-H03"
	if err := s.product("H03", productID); err != nil {
		return err
	}
	if err := s.dish("H03", dishID, productID, "1000"); err != nil {
		return err
	}
	catalog, err := s.expect(http.MethodGet, "/api/serving/dish-catalog", nil, http.StatusOK)
	if err != nil || !contains(array(catalog.body["items"]), "dishID", dishID) {
		return errorWithCause("dish absent without stock", err)
	}
	menu, err := s.expect(http.MethodGet, "/api/serving/menu", nil, http.StatusOK)
	if err != nil || contains(array(menu.body["dishes"]), "dishID", dishID) {
		return errorWithCause("unstocked dish appeared in menu", err)
	}
	stop, err := s.expect(http.MethodGet, "/api/serving/stop-list", nil, http.StatusOK)
	if err != nil || !contains(array(stop.body["dishes"]), "dishID", dishID) {
		return errorWithCause("unstocked dish absent from stop-list", err)
	}
	s.record("H03", map[string]string{"dishID": dishID, "catalog": "defined", "menu": "unavailable", "stopList": "listed"})
	return nil
}

func (s *suite) h04() error {
	productID, dishID := "product-H04", "dish-H04"
	if err := s.product("H04", productID); err != nil {
		return err
	}
	if err := s.dish("H04", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	early, err := s.delivery("H04-early", "delivery-H04-early", productID, "1000", now.Add(-9*24*time.Hour), now.Add(-8*24*time.Hour))
	if err != nil {
		return err
	}
	if _, err := s.delivery("H04-late", "delivery-H04-late", productID, "1000", now.Add(-time.Hour), now); err != nil {
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
	confirmed, err := s.confirm(context.Background(), "confirm-H04", proposalID)
	if err != nil || confirmed.status != http.StatusOK || text(confirmed.body["kind"]) != "OrderConfirmed" {
		return errorWithCause("FEFO confirmation failed", err)
	}
	allocations := array(object(confirmed.body["order"])["allocations"])
	if len(allocations) == 0 || text(object(allocations[0])["batchID"]) != early {
		return errors.New("order did not allocate from earliest-expiring batch")
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "1000" {
		return errorWithCause(fmt.Sprintf("FEFO remaining stock=%s", s.quantity(stock, productID)), err)
	}
	history, err := s.history()
	if err != nil || eventCountForCommand(history, "OrderPlaced", "confirm-H04") != 1 {
		return errorWithCause("FEFO history does not contain one order", err)
	}
	s.record("H04", map[string]string{"earliestBatch": early, "allocatedBatch": text(object(allocations[0])["batchID"]), "stockBefore": "2000", "stockAfter": s.quantity(stock, productID), "orderPlaced": "1"})
	return nil
}

func (s *suite) h05h06() error {
	productID, dishID := "product-H05", "dish-H05"
	if err := s.product("H05", productID); err != nil {
		return err
	}
	if err := s.dish("H05", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	if _, err := s.delivery("H05", "delivery-H05", productID, "1000", now.Add(-time.Hour), now); err != nil {
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
	first, err := s.confirm(context.Background(), "confirm-H05-1", oneID)
	if err != nil || first.status != http.StatusOK {
		return errorWithCause("first proposal confirm", err)
	}
	second, err := s.confirm(context.Background(), "confirm-H05-2", twoID)
	if err != nil || second.status != http.StatusOK || text(second.body["kind"]) != "RequiresConfirmation" {
		return errorWithCause("second proposal did not require confirmation", err)
	}
	replacement := object(second.body["proposal"])
	accepted, requested := array(replacement["acceptedPositions"]), array(replacement["requestedPositions"])
	if len(accepted) != 0 || len(accepted) > len(requested) || len(array(replacement["changes"])) == 0 {
		return errors.New("replacement increased or restored accepted positions")
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "0" {
		return errorWithCause(fmt.Sprintf("stock after competing proposals=%s", s.quantity(stock, productID)), err)
	}
	s.record("H05", map[string]string{"oldProposal": twoID, "replacement": text(replacement["proposalID"]), "acceptedAfterRecheck": strconv.Itoa(len(accepted)), "stockBefore": "1000", "stockAfter": s.quantity(stock, productID)})
	s.record("H06", map[string]string{"requestedPositions": strconv.Itoa(len(requested)), "acceptedPositions": strconv.Itoa(len(accepted)), "stockGrowth": "none", "stockAfter": s.quantity(stock, productID)})
	return nil
}

func (s *suite) h07() error {
	productID, dishID := "product-H07", "dish-H07"
	if err := s.product("H07", productID); err != nil {
		return err
	}
	if err := s.dish("H07", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	deliveryRequest := map[string]any{"commandID": "receive-H07", "deliveryID": "delivery-H07", "number": "Delivery H07", "createdAt": moscow(now), "lines": []any{map[string]any{"productID": productID, "manufacturedAt": moscow(now.Add(-time.Hour)), "shelfLife": map[string]any{"value": "10", "unit": "day"}, "receivedAt": moscow(now), "quantity": "3000"}}}
	one, err := s.expect(http.MethodPost, "/api/warehouse/deliveries", deliveryRequest, http.StatusOK)
	if err != nil {
		return err
	}
	two, err := s.expect(http.MethodPost, "/api/warehouse/deliveries", deliveryRequest, http.StatusOK)
	if err != nil || fmt.Sprint(one.body["batchIDs"]) != fmt.Sprint(two.body["batchIDs"]) {
		return errorWithCause("delivery exact retry changed its receipt", err)
	}
	proposal, err := s.proposal(dishID, "1")
	if err != nil {
		return err
	}
	proposalID, err := id(proposal, "proposalID")
	if err != nil {
		return err
	}
	first, err := s.confirm(context.Background(), "confirm-H07", proposalID)
	if err != nil || first.status != http.StatusOK {
		return errorWithCause("order confirmation", err)
	}
	second, err := s.confirm(context.Background(), "confirm-H07", proposalID)
	if err != nil || second.status != http.StatusOK || text(second.body["kind"]) != "OrderConfirmed" || text(object(first.body["order"])["orderID"]) != text(object(second.body["order"])["orderID"]) {
		return errorWithCause("order exact retry changed result", err)
	}
	history, err := s.history()
	if err != nil || eventCountForCommand(history, "DeliveryReceived", "receive-H07") != 1 || eventCountForCommand(history, "OrderPlaced", "confirm-H07") != 1 {
		return errorWithCause("idempotent history has wrong facts", err)
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "2000" {
		return errorWithCause("idempotent remaining stock", err)
	}
	s.record("H07", map[string]string{"deliveryFacts": strconv.Itoa(eventCountForCommand(history, "DeliveryReceived", "receive-H07")), "orderFacts": strconv.Itoa(eventCountForCommand(history, "OrderPlaced", "confirm-H07")), "stockBefore": "3000", "stockAfter": s.quantity(stock, productID), "deliveryID": "delivery-H07"})
	return nil
}

func (s *suite) h09() error {
	productID, dishID := "product-H09", "dish-H09"
	if err := s.product("H09", productID); err != nil {
		return err
	}
	if err := s.dish("H09", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	if _, err := s.delivery("H09", "delivery-H09", productID, "1000", now.Add(-time.Hour), now); err != nil {
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
	const callers = 8
	responses := make(chan response, callers)
	errorsCh := make(chan error, callers)
	for range callers {
		go func() {
			result, callErr := s.confirm(context.Background(), "confirm-H09", proposalID)
			responses <- result
			errorsCh <- callErr
		}()
	}
	completed := 0
	for range callers {
		result, callErr := <-responses, <-errorsCh
		if callErr != nil {
			return callErr
		}
		if result.status == http.StatusOK && text(result.body["kind"]) == "OrderConfirmed" {
			completed++
		} else if result.status != http.StatusAccepted {
			return fmt.Errorf("parallel replay status=%d body=%s", result.status, result.bytes)
		}
	}
	final, err := s.resolve("confirm-H09", proposalID)
	if err != nil || final.status != http.StatusOK || text(final.body["kind"]) != "OrderConfirmed" {
		return errorWithCause("parallel attempts did not resolve to one order", err)
	}
	history, err := s.history()
	if err != nil || eventCountForCommand(history, "OrderPlaced", "confirm-H09") != 1 {
		return errorWithCause("parallel order count is not one", err)
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "0" {
		return errorWithCause("parallel exact retries changed final stock", err)
	}
	s.record("H09", map[string]string{"parallelCallers": strconv.Itoa(callers), "completedResponses": strconv.Itoa(completed), "orderPlaced": strconv.Itoa(eventCountForCommand(history, "OrderPlaced", "confirm-H09")), "stockBefore": "1000", "stockAfter": s.quantity(stock, productID), "gatewayRequests": strconv.Itoa(callers), "orderID": text(object(final.body["order"])["orderID"])})
	return nil
}

func (s *suite) h10() error {
	productID, dishID := "product-H10", "dish-H10"
	if err := s.product("H10", productID); err != nil {
		return err
	}
	if err := s.dish("H10", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	if _, err := s.delivery("H10", "delivery-H10", productID, "1000", now.Add(-time.Hour), now); err != nil {
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
	results, errorsCh := make(chan response, 2), make(chan error, 2)
	for _, item := range []struct{ command, proposal string }{{"confirm-H10-1", oneID}, {"confirm-H10-2", twoID}} {
		item := item
		go func() {
			result, callErr := s.confirm(context.Background(), item.command, item.proposal)
			results <- result
			errorsCh <- callErr
		}()
	}
	orders, replacements := 0, 0
	for range 2 {
		result, callErr := <-results, <-errorsCh
		if callErr != nil {
			return callErr
		}
		if result.status != http.StatusOK {
			return fmt.Errorf("competing proposal returned %d: %s", result.status, result.bytes)
		}
		switch text(result.body["kind"]) {
		case "OrderConfirmed":
			orders++
		case "RequiresConfirmation":
			replacements++
		default:
			return fmt.Errorf("unexpected result %s", result.bytes)
		}
	}
	history, err := s.history()
	if err != nil || eventCount(history, "OrderPlaced") != 1 || orders != 1 || replacements != 1 {
		return errorWithCause(fmt.Sprintf("competing stock orders=%d replacements=%d", orders, replacements), err)
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "0" {
		return errorWithCause("competing proposals produced an invalid final balance", err)
	}
	s.record("H10", map[string]string{"proposals": "2", "orders": strconv.Itoa(orders), "replacements": strconv.Itoa(replacements), "stockBefore": "1000", "stockAfter": s.quantity(stock, productID), "oversell": "none"})
	return nil
}

func (s *suite) h11() error {
	productID := "product-H11"
	if err := s.product("H11", productID); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	request := map[string]any{"commandID": "receive-H11", "deliveryID": "delivery-H11", "number": "Expired delivery", "createdAt": moscow(now.Add(-72 * time.Hour)), "lines": []any{map[string]any{"productID": productID, "manufacturedAt": moscow(now.Add(-72 * time.Hour)), "shelfLife": map[string]any{"value": "1", "unit": "day"}, "receivedAt": moscow(now.Add(-71 * time.Hour)), "quantity": "5000"}}}
	if _, err := s.expect(http.MethodPost, "/api/warehouse/deliveries", request, http.StatusOK); err != nil {
		return err
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "0" {
		return errorWithCause(fmt.Sprintf("expired arrival balance=%s", s.quantity(stock, productID)), err)
	}
	history, err := s.history()
	if err != nil || eventCount(history, "BatchExpired") != 1 {
		return errorWithCause("expired arrival history", err)
	}
	s.record("H11", map[string]string{"arrivalQuantity": "5000", "arrival": "already expired", "BatchExpired": strconv.Itoa(eventCount(history, "BatchExpired")), "remainingStock": s.quantity(stock, productID)})
	return nil
}

func (s *suite) h12() error {
	productID, dishID := "product-H12", "dish-H12"
	if err := s.product("H12", productID); err != nil {
		return err
	}
	if err := s.dish("H12", dishID, productID, "2000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	if _, err := s.delivery("H12-a", "delivery-H12-a", productID, "1000", now.Add(-time.Hour), now); err != nil {
		return err
	}
	stop, err := s.expect(http.MethodGet, "/api/serving/stop-list", nil, http.StatusOK)
	if err != nil || !contains(array(stop.body["dishes"]), "dishID", dishID) {
		return errorWithCause("short stock absent from stop-list", err)
	}
	if _, err := s.delivery("H12-b", "delivery-H12-b", productID, "1000", now.Add(-time.Hour), now); err != nil {
		return err
	}
	recommendations, err := s.expect(http.MethodGet, "/api/serving/recommendations", nil, http.StatusOK)
	if err != nil || !contains(array(recommendations.body["items"]), "dishID", dishID) {
		return errorWithCause("stocked dish absent from recommendations", err)
	}
	items := array(recommendations.body["items"])
	for index := 1; index < len(items); index++ {
		if text(object(items[index-1])["priorityExpiresAt"]) > text(object(items[index])["priorityExpiresAt"]) {
			return errors.New("recommendations are not ordered by FEFO priority")
		}
	}
	proposal, err := s.proposal(dishID, "1")
	if err != nil {
		return err
	}
	proposalID, err := id(proposal, "proposalID")
	if err != nil {
		return err
	}
	if _, err := s.confirm(context.Background(), "confirm-H12", proposalID); err != nil {
		return err
	}
	updated, err := s.expect(http.MethodGet, "/api/serving/recommendations", nil, http.StatusOK)
	if err != nil || contains(array(updated.body["items"]), "dishID", dishID) {
		return errorWithCause("recommendations did not recalculate after sale", err)
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != "0" {
		return errorWithCause("recommendation read changed final stock unexpectedly", err)
	}
	s.record("H12", map[string]string{"dishID": dishID, "stockBefore": "1000", "stockAfterRestock": "2000", "stockAfterOrder": s.quantity(stock, productID), "stopListBeforeRestock": "listed", "recommendationsAfterRestock": "available", "recommendationsAfterOrder": "unavailable"})
	return nil
}

func (s *suite) h21() error {
	productID := "大数/+ &? H21"
	if err := s.product("H21", productID); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	const amount = "9007199254740993"
	if _, err := s.delivery("H21", "delivery-H21", productID, amount, now.Add(-time.Hour), now); err != nil {
		return err
	}
	stock, err := s.inventory()
	if err != nil || s.quantity(stock, productID) != amount {
		return errorWithCause("large quantity failed exact round-trip", err)
	}
	query := url.Values{}
	query.Set("dishID", productID)
	missing, err := s.call(context.Background(), http.MethodGet, "/api/serving/dish?"+query.Encode(), nil)
	if err != nil || missing.status != http.StatusNotFound {
		return errorWithCause(fmt.Sprintf("reserved identifier query returned %d", missing.status), err)
	}
	if !strings.HasSuffix(text(stock["evaluatedAt"]), "+03:00") {
		return errors.New("inventory time did not use Moscow offset")
	}
	s.record("H21", map[string]string{"productID": productID, "quantity": amount, "timestampOffset": "+03:00", "queryEncoding": "exact"})
	return nil
}

func (s *suite) h22() error {
	before, err := s.expect(http.MethodGet, "/api/serving/products", nil, http.StatusOK)
	if err != nil {
		return err
	}
	beforeHistory, err := s.history()
	if err != nil {
		return err
	}
	cases := []string{
		`{"commandID":"invalid-H22","productID":"bad-null","name":null,"baseUnit":"g"}`,
		`{"commandID":"invalid-H22-baseunit-empty","productID":"missing-H22","name":"bad","baseUnit":""}`,
		`{"commandID":"invalid-H22","productID":"bad-extra","name":"bad","baseUnit":"g","extra":true}`,
		`{"commandID":"invalid-H22","productID":"bad-duplicate","name":"one","name":"two","baseUnit":"g"}`,
		`{"commandID":"invalid-H22","productID":"bad-second","name":"bad","baseUnit":"g"} {"second":true}`,
	}
	for _, raw := range cases {
		if _, err := s.jsonError(http.MethodPost, "/api/serving/products", raw, http.StatusBadRequest); err != nil {
			return err
		}
	}
	for _, path := range []string{
		"/api/serving/dish?dishID=one&dishID=two",
		"/api/serving/dish?dishID=one&extra=value",
	} {
		result, err := s.call(context.Background(), http.MethodGet, path, nil)
		if err != nil || result.status != http.StatusBadRequest {
			return errorWithCause(fmt.Sprintf("strict query %q returned %d", path, result.status), err)
		}
	}
	for _, path := range []string{
		"/api/serving/dish?dishID=one;extra=value",
		"/api/serving/dish?dishID=%ZZ",
	} {
		status, _, err := rawGatewayRequest(s.cfg.gatewayURL, http.MethodGet, path, "", "", 3*time.Second)
		if err != nil || status != http.StatusBadRequest {
			return errorWithCause(fmt.Sprintf("raw strict query %q returned %d", path, status), err)
		}
	}
	after, err := s.expect(http.MethodGet, "/api/serving/products", nil, http.StatusOK)
	if err != nil || len(array(before.body["items"])) != len(array(after.body["items"])) {
		return errorWithCause("invalid JSON changed catalog", err)
	}
	afterHistory, err := s.history()
	if err != nil || text(beforeHistory["stateVersion"]) != text(afterHistory["stateVersion"]) {
		return errorWithCause("invalid JSON or query changed warehouse state", err)
	}
	s.record("H22", map[string]string{"invalidJSONForms": "null,empty-baseUnit,extra,duplicate,second-value", "queryForms": "repeated,extra,semicolon,malformed-escape", "status": "400", "writes": "0"})
	return nil
}

func (s *suite) h23() error {
	for _, path := range []string{"/api/warehouse/internal/v1/products", "/api/warehouse/internal/v1/order-commits", "/api/serving/internal/v1/products"} {
		result, err := s.call(context.Background(), http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		if result.status != http.StatusNotFound && result.status != http.StatusMethodNotAllowed {
			return fmt.Errorf("internal route leaked through gateway: %s returned %d", path, result.status)
		}
	}
	s.record("H23", map[string]string{"internalRoutesThroughGateway": "blocked"})
	return nil
}

func (s *suite) h24() error {
	productID, dishID := "product-H24", "dish-H24"
	if err := s.product("H24", productID); err != nil {
		return err
	}
	if err := s.dish("H24", dishID, productID, "1000"); err != nil {
		return err
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	if _, err := s.delivery("H24", "delivery-H24", productID, "1000", now.Add(-time.Hour), now); err != nil {
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
	emptyProposal, err := s.proposal("", "")
	if err != nil {
		return err
	}
	emptyProposalID, err := id(emptyProposal, "proposalID")
	if err != nil {
		return err
	}
	emptyAttempt, err := s.confirm(context.Background(), "empty-H24", emptyProposalID)
	if err != nil || emptyAttempt.status != http.StatusOK || text(emptyAttempt.body["kind"]) != "EmptyOrder" {
		return errorWithCause("pre-restart EmptyOrder did not complete in serving memory", err)
	}
	beforeProducts, err := s.expect(http.MethodGet, "/api/serving/products", nil, http.StatusOK)
	if err != nil || !contains(array(beforeProducts.body["items"]), "productID", productID) {
		return errorWithCause("warehouse product was not visible before serving restart", err)
	}
	beforeCatalog, err := s.expect(http.MethodGet, "/api/serving/dish-catalog", nil, http.StatusOK)
	if err != nil || !contains(array(beforeCatalog.body["items"]), "dishID", dishID) {
		return errorWithCause("warehouse dish was not visible before serving restart", err)
	}
	beforeInventory, err := s.inventory()
	if err != nil || s.quantity(beforeInventory, productID) != "1000" {
		return errorWithCause("warehouse stock was not visible before serving restart", err)
	}
	beforeHistory, err := s.history()
	if err != nil || eventCountForCommand(beforeHistory, "OrderPlaced", "empty-H24") != 0 {
		return errorWithCause("EmptyOrder created a warehouse sale", err)
	}
	restartCfg := restartConfig{s.cfg.factoryDir, s.cfg.composeProject, s.cfg.composeOverride}
	if err := restart(context.Background(), restartCfg, "serving"); err != nil {
		return err
	}
	if err := waitHealth(s.client, s.cfg.gatewayURL+"/api/serving/healthz", 20*time.Second); err != nil {
		return err
	}
	afterProducts, err := s.expect(http.MethodGet, "/api/serving/products", nil, http.StatusOK)
	if err != nil || !contains(array(afterProducts.body["items"]), "productID", productID) {
		return errorWithCause("serving restart lost warehouse-backed products", err)
	}
	afterCatalog, err := s.expect(http.MethodGet, "/api/serving/dish-catalog", nil, http.StatusOK)
	if err != nil || !contains(array(afterCatalog.body["items"]), "dishID", dishID) {
		return errorWithCause("serving restart lost warehouse-backed dish definitions", err)
	}
	afterInventory, err := s.inventory()
	if err != nil || s.quantity(afterInventory, productID) != s.quantity(beforeInventory, productID) {
		return errorWithCause("serving restart changed warehouse inventory", err)
	}
	afterHistory, err := s.history()
	if err != nil || text(afterHistory["stateVersion"]) != text(beforeHistory["stateVersion"]) || eventCountForCommand(afterHistory, "OrderPlaced", "empty-H24") != 0 {
		return errorWithCause("serving restart changed warehouse history", err)
	}
	result, err := s.confirm(context.Background(), "confirm-H24", proposalID)
	if err != nil || result.status != http.StatusNotFound || text(result.body["code"]) != "ProposalNotFound" || text(result.body["outcome"]) != "unknown" {
		return errorWithCause("serving restart did not lose the old proposal with unknown outcome", err)
	}
	emptyReplay, err := s.confirm(context.Background(), "empty-H24", emptyProposalID)
	if err != nil || emptyReplay.status != http.StatusNotFound || text(emptyReplay.body["code"]) != "ProposalNotFound" || text(emptyReplay.body["outcome"]) != "unknown" {
		return errorWithCause("serving restart retained the completed local EmptyOrder attempt", err)
	}
	emptyResolve, err := s.resolve("empty-H24", emptyProposalID)
	if err != nil || emptyResolve.status != http.StatusNotFound || text(emptyResolve.body["outcome"]) != "unknown" {
		return errorWithCause("serving restart did not forget the EmptyOrder attempt", err)
	}
	if err := restart(context.Background(), restartCfg, "warehouse"); err != nil {
		return err
	}
	if err := waitHealth(s.client, s.cfg.gatewayURL+"/api/warehouse/healthz", 20*time.Second); err != nil {
		return err
	}
	stock, err := s.inventory()
	if err != nil || len(array(stock["products"])) != 0 {
		return errorWithCause("warehouse restart retained volatile inventory", err)
	}
	history, err := s.history()
	if err != nil || len(array(history["events"])) != 0 {
		return errorWithCause("warehouse restart retained volatile history", err)
	}
	products, err := s.expect(http.MethodGet, "/api/serving/products", nil, http.StatusOK)
	if err != nil || len(array(products.body["items"])) != 0 {
		return errorWithCause("warehouse restart retained its product catalog", err)
	}
	catalog, err := s.expect(http.MethodGet, "/api/serving/dish-catalog", nil, http.StatusOK)
	if err != nil || len(array(catalog.body["items"])) != 0 {
		return errorWithCause("warehouse restart retained its dish catalog", err)
	}
	unseen, err := s.resolve("unseen-after-warehouse-restart-H24", proposalID)
	if err != nil || unseen.status != http.StatusNotFound || text(unseen.body["outcome"]) != "unknown" {
		return errorWithCause("warehouse restart absence did not remain unknown", err)
	}
	s.record("H24", map[string]string{"servingRestart": "warehouse catalog/stock/history retained; proposals and local EmptyOrder attempt lost as unknown", "warehouseRestart": "catalog, inventory and history lost; absent attempt remains unknown"})
	return nil
}
