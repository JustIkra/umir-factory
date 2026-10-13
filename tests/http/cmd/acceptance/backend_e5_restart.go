package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"slices"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

type e5BusinessContext struct {
	OrgID, TenantID, RuntimeEpoch, WarehouseEpoch, EvaluatedAt, StateVersion string
	Settings                                                                 struct{ TimeZone, SettingsVersion string }
}

type e5BusinessCapture struct {
	Scope   backendScope
	Replies map[string]json.RawMessage
}

func e5BusinessRead(ctx context.Context, input backendScenarioInput, scope backendScope) (e5BusinessCapture, error) {
	capture := e5BusinessCapture{Scope: scope, Replies: map[string]json.RawMessage{}}
	for _, op := range []string{"products", "dishCatalog", "inventory", "history", "pendingWriteoffs", "menu"} {
		data, err := input.Session.call(ctx, op, map[string]string{"restaurantID": scope.RestaurantID}, nil, nil, 200)
		if err != nil {
			return capture, err
		}
		var reply struct{ Context e5BusinessContext }
		if e5Decode(data, &reply) != nil || reply.Context.OrgID != scope.OrgID || reply.Context.TenantID != scope.RestaurantID || !faultproxy.UUID(reply.Context.RuntimeEpoch) {
			return capture, errors.New("E5 business observation scope differs")
		}
		capture.Replies[op] = input.Session.publicReply().Body
	}
	return capture, nil
}

func e5BusinessEmpty(capture e5BusinessCapture, warehouseEpoch, servingEpoch string) error {
	if !faultproxy.UUID(warehouseEpoch) || !faultproxy.UUID(servingEpoch) {
		return errors.New("E5 observed owner epochs unavailable")
	}
	for _, op := range []string{"products", "dishCatalog", "inventory", "history", "pendingWriteoffs", "menu"} {
		raw, found := capture.Replies[op]
		if !found {
			return errors.New("E5 empty-state observation missing")
		}
		var reply struct {
			Context e5BusinessContext
			Result  map[string]json.RawMessage
			Items   json.RawMessage
		}
		if json.Unmarshal(raw, &reply) != nil || reply.Context.OrgID != capture.Scope.OrgID || reply.Context.TenantID != capture.Scope.RestaurantID || !faultproxy.UUID(reply.Context.RuntimeEpoch) || reply.Context.StateVersion != "0" {
			return errors.New("E5 empty-state context differs")
		}
		if op == "menu" {
			if reply.Context.RuntimeEpoch != servingEpoch || reply.Context.WarehouseEpoch != warehouseEpoch {
				return errors.New("E5 empty-state Serving epochs differ from observed owners")
			}
		} else if reply.Context.RuntimeEpoch != warehouseEpoch {
			return errors.New("E5 empty-state Warehouse epoch differs from observed owner")
		}
		key := "items"
		if op == "history" {
			key = "events"
		}
		if op == "menu" {
			key = "dishes"
		}
		rows := reply.Result[key]
		if op == "pendingWriteoffs" {
			rows = reply.Items
		}
		var items []json.RawMessage
		if len(rows) == 0 || string(rows) == "null" || json.Unmarshal(rows, &items) != nil || len(items) != 0 {
			return errors.New("E5 business state was retained or unavailable")
		}
	}
	return nil
}

type e5ResetSeed struct {
	Scope                                                                            backendScope
	WarehouseEpoch, ServingEpoch, ProposalID, OrderCommand, WriteoffCommand, BatchID string
	Before                                                                           e5BusinessCapture
}

func e5BusinessSeed(ctx context.Context, input backendScenarioInput, scope backendScope) (e5ResetSeed, error) {
	b := input.Session
	seed := e5ResetSeed{Scope: scope}
	params := map[string]string{"restaurantID": scope.RestaurantID}
	stock, err := b.call(ctx, "inventory", params, nil, nil, 200)
	if err != nil {
		return seed, err
	}
	var initial struct{ Context e5BusinessContext }
	if e5Decode(stock, &initial) != nil || initial.Context.OrgID != scope.OrgID || initial.Context.TenantID != scope.RestaurantID || !faultproxy.UUID(initial.Context.RuntimeEpoch) {
		return seed, faultproxy.ErrProtocol
	}
	seed.WarehouseEpoch = initial.Context.RuntimeEpoch
	headers := map[string]string{"X-Umir-Runtime-Epoch": seed.WarehouseEpoch}
	id, err := faultproxy.NewUUID()
	if err != nil {
		return seed, err
	}
	product, dish := "e5-product-"+id, "e5-dish-"+id
	if _, err := b.call(ctx, "registerProduct", params, map[string]any{"commandID": "e5-product-" + id, "productID": product, "name": "Restart product", "baseUnit": "g"}, headers, 200); err != nil {
		return seed, err
	}
	if _, err := b.call(ctx, "defineDish", params, map[string]any{"commandID": "e5-dish-" + id, "dishID": dish, "name": "Restart dish", "ingredients": []any{map[string]any{"productID": product, "quantity": "100", "baseUnit": "g"}}}, headers, 200); err != nil {
		return seed, err
	}
	clock, err := input.Client.clock(ctx, "warehouse")
	if err != nil {
		return seed, err
	}
	instant, err := time.Parse(time.RFC3339Nano, clock.Instant)
	if err != nil {
		return seed, err
	}
	zone, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return seed, err
	}
	now := instant.In(zone).Format(time.RFC3339Nano)
	expired := instant.Add(-48 * time.Hour).In(zone).Format(time.RFC3339Nano)
	lines := []any{}
	for _, manufacture := range []string{now, expired, expired} {
		lines = append(lines, map[string]any{"productID": product, "manufacturedAt": map[string]any{"instant": manufacture}, "receivedAt": map[string]any{"instant": now}, "shelfLife": map[string]any{"value": "1", "unit": "day"}, "quantity": "1000"})
	}
	delivery, err := b.call(ctx, "receiveDelivery", params, map[string]any{"commandID": "e5-delivery-" + id, "deliveryID": "e5-delivery-" + id, "number": "E5 restart", "createdAt": map[string]any{"instant": now}, "expectedSettingsVersion": initial.Context.Settings.SettingsVersion, "lines": lines}, headers, 200)
	if err != nil {
		return seed, err
	}
	var received struct{ Result struct{ BatchIDs []string } }
	if e5Decode(delivery, &received) != nil || len(received.Result.BatchIDs) != 3 {
		return seed, errors.New("E5 real delivery batch observations missing")
	}
	seed.BatchID = received.Result.BatchIDs[1]
	seed.WriteoffCommand = "e5-writeoff-" + id
	if _, err := b.call(ctx, "confirmWriteoff", params, map[string]any{"commandID": seed.WriteoffCommand, "batchID": seed.BatchID}, headers, 200); err != nil {
		return seed, err
	}
	menu, err := b.call(ctx, "menu", params, nil, nil, 200)
	if err != nil {
		return seed, err
	}
	var serving struct{ Context e5BusinessContext }
	if e5Decode(menu, &serving) != nil || serving.Context.OrgID != scope.OrgID || serving.Context.TenantID != scope.RestaurantID || !faultproxy.UUID(serving.Context.RuntimeEpoch) || serving.Context.WarehouseEpoch != seed.WarehouseEpoch {
		return seed, errors.New("E5 real Serving epochs unavailable")
	}
	seed.ServingEpoch = serving.Context.RuntimeEpoch
	proposal, err := b.call(ctx, "prepareOrder", params, map[string]any{"positions": []any{map[string]any{"dishID": dish, "portions": "1"}}}, map[string]string{"X-Umir-Runtime-Epoch": seed.ServingEpoch}, 200)
	if err != nil {
		return seed, err
	}
	var prepared struct {
		Result struct {
			ProposalID        string
			AcceptedPositions []json.RawMessage
		}
	}
	if e5Decode(proposal, &prepared) != nil || prepared.Result.ProposalID == "" || len(prepared.Result.AcceptedPositions) != 1 {
		return seed, errors.New("E5 order proposal did not contain real stock")
	}
	seed.ProposalID, seed.OrderCommand = prepared.Result.ProposalID, "e5-order-"+id
	confirmed, err := b.call(ctx, "confirmOrder", params, map[string]any{"commandID": seed.OrderCommand, "proposalID": seed.ProposalID}, map[string]string{"X-Umir-Runtime-Epoch": seed.ServingEpoch}, 200)
	if err != nil {
		return seed, err
	}
	var outcome struct{ Result struct{ Kind string } }
	if e5Decode(confirmed, &outcome) != nil || outcome.Result.Kind != "OrderConfirmed" {
		return seed, errors.New("E5 order attempt was not really committed")
	}
	var order struct {
		Result struct{ Order struct{ ProposalID string } }
	}
	if e5Decode(confirmed, &order) != nil || order.Result.Order.ProposalID != seed.ProposalID {
		return seed, errors.New("E5 committed order refers to another proposal")
	}
	seed.Before, err = e5BusinessRead(ctx, input, scope)
	if err != nil {
		return seed, err
	}
	var inventory struct {
		Result struct {
			Items []struct{ ProductID, AvailableQuantity, PendingWriteoffQuantity, OnHandQuantity string }
		}
	}
	if json.Unmarshal(seed.Before.Replies["inventory"], &inventory) != nil || len(inventory.Result.Items) != 1 || inventory.Result.Items[0].ProductID != product || inventory.Result.Items[0].AvailableQuantity != "900" || inventory.Result.Items[0].PendingWriteoffQuantity != "1000" || inventory.Result.Items[0].OnHandQuantity != "1900" {
		return seed, errors.New("E5 seeded stock and pending state differ")
	}
	for _, op := range []string{"products", "dishCatalog"} {
		var catalog struct {
			Result struct{ Items []json.RawMessage }
		}
		if json.Unmarshal(seed.Before.Replies[op], &catalog) != nil || len(catalog.Result.Items) != 1 {
			return seed, errors.New("E5 catalog seed unavailable")
		}
	}
	var pending struct {
		Items []struct {
			Batch  struct{ BatchID string }
			Status string
		}
	}
	if json.Unmarshal(seed.Before.Replies["pendingWriteoffs"], &pending) != nil || len(pending.Items) != 1 || pending.Items[0].Batch.BatchID != received.Result.BatchIDs[2] || pending.Items[0].Status != "pending_writeoff" {
		return seed, errors.New("E5 actual pending batch observation differs")
	}
	lookup, err := b.callQuery(ctx, "resolveWriteoffCommand", params, url.Values{"commandID": {seed.WriteoffCommand}, "batchID": {seed.BatchID}}, nil, headers, 200)
	if err != nil {
		return seed, err
	}
	var writeoff struct {
		Observation struct{ State, Outcome string }
	}
	if e5Decode(lookup, &writeoff) != nil || writeoff.Observation.State != "committed" || writeoff.Observation.Outcome != "committed" {
		return seed, errors.New("E5 writeoff command seed unavailable")
	}
	return seed, nil
}

type e5AuthUser struct {
	UserID, Login, DisplayName, CredentialVersion string
	Email                                         *string
}

type e5AuthCapture struct {
	User    e5AuthUser
	Session struct {
		ExpiresAt, IdleExpiresAt, AbsoluteExpiresAt string
		RememberMe                                  bool
	}
	Organizations []json.RawMessage
}

func e5AuthRead(ctx context.Context, input backendScenarioInput) (e5AuthCapture, error) {
	var capture e5AuthCapture
	data, err := input.Session.call(ctx, "me", nil, nil, nil, 200)
	if err != nil {
		return capture, err
	}
	var me struct {
		User    e5AuthUser
		Session struct {
			ExpiresAt, IdleExpiresAt, AbsoluteExpiresAt string
			RememberMe                                  bool
		}
	}
	if e5Decode(data, &me) != nil || !faultproxy.UUID(me.User.UserID) || me.User.CredentialVersion == "" {
		return capture, errors.New("E5 Auth session observation unavailable")
	}
	capture.User, capture.Session = me.User, me.Session
	for _, instant := range []string{capture.Session.ExpiresAt, capture.Session.IdleExpiresAt, capture.Session.AbsoluteExpiresAt} {
		if _, err := time.Parse(time.RFC3339Nano, instant); err != nil {
			return capture, errors.New("E5 persisted session deadline unavailable")
		}
	}
	for _, scope := range input.Scopes {
		org, err := input.Session.call(ctx, "organization", map[string]string{"orgID": scope.OrgID}, nil, nil, 200)
		if err != nil {
			return capture, err
		}
		if org["orgID"] != scope.OrgID || org["lifecycle"] != "active" {
			return capture, errors.New("E5 preserved Auth organization differs")
		}
		raw, err := json.Marshal(org)
		if err != nil {
			return capture, err
		}
		capture.Organizations = append(capture.Organizations, raw)
	}
	return capture, nil
}

type e5RegistryEntry struct {
	Restaurant struct{ RestaurantID, OrgID, Lifecycle, LifecycleVersion string }
	Services   []struct{ RestaurantID, Service, Epoch, State, LifecycleVersion string }
}

func e5RegistryReady(ctx context.Context, client *backendClient, seeds []e5ResetSeed, warehouseEpoch, servingEpoch string) (json.RawMessage, error) {
	for {
		reply, err := client.privateCall(ctx, "auth", "registry", nil, nil, nil, backendPrivateCredential{Name: "gateway-auth"}, 200)
		if err != nil {
			return nil, err
		}
		var registry struct {
			Complete bool
			Entries  []e5RegistryEntry
		}
		if json.Unmarshal(reply.Body, &registry) != nil || !registry.Complete {
			return nil, errors.New("E5 registry observation incomplete")
		}
		ready := true
		for _, seed := range seeds {
			matches := 0
			for _, entry := range registry.Entries {
				if entry.Restaurant.RestaurantID != seed.Scope.RestaurantID {
					continue
				}
				matches++
				if entry.Restaurant.OrgID != seed.Scope.OrgID || entry.Restaurant.Lifecycle != "ready" || len(entry.Services) != 3 {
					ready = false
				}
				services := map[string]bool{}
				for _, peer := range entry.Services {
					if services[peer.Service] || !slices.Contains([]string{"warehouse", "serving", "gateway"}, peer.Service) || peer.RestaurantID != seed.Scope.RestaurantID || peer.LifecycleVersion != entry.Restaurant.LifecycleVersion || peer.State != "ready" || !faultproxy.UUID(peer.Epoch) {
						ready = false
					}
					services[peer.Service] = true
					if peer.Service == "warehouse" && peer.Epoch != warehouseEpoch || peer.Service == "serving" && peer.Epoch != servingEpoch {
						ready = false
					}
				}
			}
			if matches != 1 {
				ready = false
			}
		}
		if ready {
			return reply.Body, nil
		}
		if err := backendPause(ctx); err != nil {
			return nil, err
		}
	}
}

func e5RuntimeEpoch(ctx context.Context, client *backendClient, owner string) (string, error) {
	reply, err := client.privateCall(ctx, owner, owner+"Runtime", nil, nil, nil, backendPrivateCredential{Name: "gateway-" + owner}, 200)
	if err != nil {
		return "", err
	}
	var runtime struct {
		Service, Epoch, TZDataVersion string
		Ready                         bool
	}
	if json.Unmarshal(reply.Body, &runtime) != nil || runtime.Service != owner || !runtime.Ready || !faultproxy.UUID(runtime.Epoch) || runtime.TZDataVersion == "" {
		return "", errors.New("E5 real owner runtime unavailable")
	}
	return runtime.Epoch, nil
}

func e5OldAttempts(ctx context.Context, input backendScenarioInput, seeds []e5ResetSeed) ([]json.RawMessage, error) {
	facts := []json.RawMessage{}
	for _, seed := range seeds {
		params := map[string]string{"restaurantID": seed.Scope.RestaurantID}
		for _, op := range []string{"resolveOrder", "resolveWriteoffCommand"} {
			query := url.Values{"commandID": {seed.OrderCommand}, "proposalID": {seed.ProposalID}, "runtimeEpoch": {seed.ServingEpoch}}
			var headers map[string]string
			if op == "resolveWriteoffCommand" {
				query = url.Values{"commandID": {seed.WriteoffCommand}, "batchID": {seed.BatchID}}
				headers = map[string]string{"X-Umir-Runtime-Epoch": seed.WarehouseEpoch}
			}
			data, err := input.Session.callQuery(ctx, op, params, query, nil, headers, 409)
			if err != nil {
				return nil, err
			}
			if data["code"] != "RuntimeReset" || data["outcome"] != "indeterminate" {
				return nil, errors.New("E5 old attempt was reclassified or reconstructed")
			}
			facts = append(facts, input.Session.publicReply().Body)
		}
	}
	return facts, nil
}

func e5ProbeReconstruction(ctx context.Context, input backendScenarioInput, seeds []e5ResetSeed, warehouse, serving string) ([]json.RawMessage, error) {
	facts := []json.RawMessage{}
	for _, seed := range seeds {
		params := map[string]string{"restaurantID": seed.Scope.RestaurantID}
		missing, err := input.Session.callQuery(ctx, "resolveOrder", params, url.Values{"commandID": {seed.OrderCommand}, "proposalID": {seed.ProposalID}, "runtimeEpoch": {serving}}, nil, nil, 404)
		if err != nil {
			return nil, err
		}
		if missing["code"] != "ProposalNotFound" || missing["outcome"] != "unknown" {
			return nil, errors.New("E5 original Serving attempt reappeared in new epoch")
		}
		facts = append(facts, input.Session.publicReply().Body)
		lookup, err := input.Session.callQuery(ctx, "resolveWriteoffCommand", params, url.Values{"commandID": {seed.WriteoffCommand}, "batchID": {seed.BatchID}}, nil, map[string]string{"X-Umir-Runtime-Epoch": warehouse}, 200)
		if err != nil {
			return nil, err
		}
		var row struct {
			Context            e5BusinessContext
			CommandID, BatchID string
			Observation        struct{ State, Outcome string }
		}
		if e5Decode(lookup, &row) != nil || row.Context.OrgID != seed.Scope.OrgID || row.Context.TenantID != seed.Scope.RestaurantID || row.Context.RuntimeEpoch != warehouse || row.CommandID != seed.WriteoffCommand || row.BatchID != seed.BatchID || row.Observation.State != "unseen" || row.Observation.Outcome != "unknown" {
			return nil, errors.New("E5 original Warehouse command reappeared in new epoch")
		}
		facts = append(facts, input.Session.publicReply().Body)
	}
	return facts, nil
}

func e5BusinessRestart(ctx context.Context, input backendScenarioInput) error {
	sessionBefore, err := e5SessionCookie(input.Session)
	if err != nil {
		return err
	}
	authBefore, err := e5AuthRead(ctx, input)
	if err != nil {
		return err
	}
	seeds := []e5ResetSeed{}
	for _, scope := range input.Scopes[:2] {
		seed, err := e5BusinessSeed(ctx, input, scope)
		if err != nil {
			return err
		}
		seeds = append(seeds, seed)
	}
	warehouseBefore, err := e5RuntimeEpoch(ctx, input.Client, "warehouse")
	if err != nil {
		return err
	}
	servingBefore, err := e5RuntimeEpoch(ctx, input.Client, "serving")
	if err != nil {
		return err
	}
	for _, seed := range seeds {
		if seed.WarehouseEpoch != warehouseBefore || seed.ServingEpoch != servingBefore {
			return errors.New("E5 seed epochs differ from real owners")
		}
	}
	if _, err := input.Client.restart(ctx, "warehouse"); err != nil {
		return err
	}
	warehouseAfter, err := e5RuntimeEpoch(ctx, input.Client, "warehouse")
	if err != nil {
		return err
	}
	if warehouseBefore == warehouseAfter {
		return errors.New("E5 Warehouse restart reused epoch")
	}
	registryAfterWarehouse, err := e5RegistryReady(ctx, input.Client, seeds, warehouseAfter, servingBefore)
	if err != nil {
		return err
	}
	warehouseEmpty := []e5BusinessCapture{}
	for _, seed := range seeds {
		capture, err := e5BusinessRead(ctx, input, seed.Scope)
		if err != nil {
			return err
		}
		if err := e5BusinessEmpty(capture, warehouseAfter, servingBefore); err != nil {
			return err
		}
		warehouseEmpty = append(warehouseEmpty, capture)
	}
	if _, err := input.Client.restart(ctx, "serving"); err != nil {
		return err
	}
	servingAfter, err := e5RuntimeEpoch(ctx, input.Client, "serving")
	if err != nil {
		return err
	}
	if servingBefore == servingAfter {
		return errors.New("E5 Serving restart reused epoch")
	}
	registryAfterServing, err := e5RegistryReady(ctx, input.Client, seeds, warehouseAfter, servingAfter)
	if err != nil {
		return err
	}
	oldAttempts, err := e5OldAttempts(ctx, input, seeds)
	if err != nil {
		return err
	}
	currentAttempts, err := e5ProbeReconstruction(ctx, input, seeds, warehouseAfter, servingAfter)
	if err != nil {
		return err
	}
	final := []e5BusinessCapture{}
	for _, seed := range seeds {
		capture, err := e5BusinessRead(ctx, input, seed.Scope)
		if err != nil {
			return err
		}
		if err := e5BusinessEmpty(capture, warehouseAfter, servingAfter); err != nil {
			return err
		}
		final = append(final, capture)
	}
	authAfter, err := e5AuthRead(ctx, input)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(authBefore, authAfter) {
		return errors.New("E5 business restart changed Auth entities or session")
	}
	sessionAfter, err := e5SessionCookie(input.Session)
	if err != nil {
		return err
	}
	if sessionBefore != sessionAfter {
		return errors.New("E5 business restart replaced the global session cookie")
	}
	return e5StoreFacts(input.Client, "BE-E5-08", struct {
		Seeds                                        []e5ResetSeed
		AuthBefore, AuthAfter                        e5AuthCapture
		RegistryAfterWarehouse, RegistryAfterServing json.RawMessage
		WarehouseEmpty, Final                        []e5BusinessCapture
		OldAttempts                                  []json.RawMessage
		CurrentAttempts                              []json.RawMessage
	}{seeds, authBefore, authAfter, registryAfterWarehouse, registryAfterServing, warehouseEmpty, final, oldAttempts, currentAttempts})
}

func e5SessionCookie(session *backendSession) (string, error) {
	if session == nil || session.client == nil || session.client.Jar == nil {
		return "", errors.New("E5 session cookie unavailable")
	}
	endpoint, err := url.Parse(session.base)
	if err != nil {
		return "", faultproxy.ErrProtocol
	}
	for _, cookie := range session.client.Jar.Cookies(endpoint) {
		if cookie.Name == "umir_operator_session" && cookie.Value != "" {
			return cookie.Value, nil
		}
	}
	return "", errors.New("E5 session cookie unavailable")
}
