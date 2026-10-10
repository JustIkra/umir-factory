package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	servingcontract "github.com/JustIkra/umir-serving/contract"
	warehousecontract "github.com/JustIkra/umir-warehouse/contract"
)

func TestWholeOrderFlow(t *testing.T) {
	system := newTestSystem(t)
	system.registerProduct(t, "flour", "Flour", warehousecontract.BaseUnitG)
	system.defineDish(t, "loaf", "Loaf", warehousecontract.Ingredient{ProductID: "flour", Quantity: 1_000_000, BaseUnit: warehousecontract.BaseUnitG})
	receipt := system.receive(t, "delivery-flour", "flour", 10_000_000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	lineCheck, err := system.bridge.EvaluateOrder(context.Background(), servingcontract.EvaluateOrderRequest{Positions: []servingcontract.Position{{LineNo: 9, DishID: "loaf", Portions: 6}}})
	if err != nil {
		t.Fatalf("evaluate source line through bridge: %v", err)
	}
	if len(lineCheck.RequestedPositions) != 1 || lineCheck.RequestedPositions[0].LineNo != 9 || len(lineCheck.AcceptedPositions) != 1 || lineCheck.AcceptedPositions[0].LineNo != 9 {
		t.Fatalf("bridge evaluation positions = %#v / %#v, want line number 9 preserved", lineCheck.RequestedPositions, lineCheck.AcceptedPositions)
	}

	proposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{
		Positions: []servingcontract.Position{{LineNo: 9, DishID: "loaf", Portions: 6}},
	})
	if err != nil {
		t.Fatalf("prepare order: %v", err)
	}
	confirmed, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "confirm-6kg", ProposalID: proposal.ProposalID})
	if err != nil {
		t.Fatalf("confirm order: %v", err)
	}
	if confirmed.Kind != servingcontract.OrderConfirmed || confirmed.Order == nil {
		t.Fatalf("confirmation = %#v, want an order confirmation", confirmed)
	}
	if got := confirmed.Order.Positions; len(got) != 1 || got[0].LineNo != 1 || got[0].Portions != 6 {
		t.Fatalf("confirmed positions = %#v, want serving's assigned line 1 with 6 portions", got)
	}
	if len(confirmed.Order.Allocations) != 1 || confirmed.Order.Allocations[0] != (servingcontract.Allocation{ProductID: "flour", BatchID: receipt.BatchIDs[0], Quantity: 6_000_000}) || !confirmed.Order.PlacedAt.Equal(system.clock.Now()) {
		t.Fatalf("confirmed order allocation/time = %#v / %v, want exact 6000000-unit batch consumption", confirmed.Order.Allocations, confirmed.Order.PlacedAt)
	}
	inventory, err := system.warehouse.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("get post-order inventory: %v", err)
	}
	if got := inventory.Products[0].Quantity; got != 4_000_000 {
		t.Fatalf("remaining flour = %d, want 4000000", got)
	}

	system.clock.Set(moscowTime(2026, time.October, 11, 10, 0))
	expired, err := system.warehouse.ProcessDueExpirations(context.Background())
	if err != nil {
		t.Fatalf("process due expirations: %v", err)
	}
	if len(expired) != 1 || expired[0].WrittenOffQuantity != 4_000_000 || expired[0].Reason != "EXPIRATION" {
		t.Fatalf("expiration results = %#v, want the remaining 4000000 units written off", expired)
	}
	inventory, err = system.warehouse.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("get expired inventory: %v", err)
	}
	if got := inventory.Products[0].Quantity; got != 0 {
		t.Fatalf("remaining flour after expiry = %d, want 0", got)
	}
}

type lostCommitResponse struct {
	servingcontract.OrdersWarehouse
	loseOnce  bool
	findCalls int
}

func (w *lostCommitResponse) FindRecordedCommand(ctx context.Context, commandID string) (servingcontract.CommandRecord, bool, error) {
	w.findCalls++
	return w.OrdersWarehouse.FindRecordedCommand(ctx, commandID)
}

func (w *lostCommitResponse) CommitAcceptedOrder(ctx context.Context, request servingcontract.CommitOrderRequest) (servingcontract.CommitResult, error) {
	result, err := w.OrdersWarehouse.CommitAcceptedOrder(ctx, request)
	if err != nil {
		return result, err
	}
	if w.loseOnce {
		w.loseOnce = false
		return servingcontract.CommitResult{}, warehousecontract.ErrUnknownWriteOutcome
	}
	return result, nil
}

func TestLostCommitResponse(t *testing.T) {
	var ports *lostCommitResponse
	system := newTestSystemWithOrders(t, func(orders servingcontract.OrdersWarehouse) servingcontract.OrdersWarehouse {
		ports = &lostCommitResponse{OrdersWarehouse: orders, loseOnce: true}
		return ports
	})
	system.registerProduct(t, "flour", "Flour", warehousecontract.BaseUnitG)
	system.defineDish(t, "loaf", "Loaf", warehousecontract.Ingredient{ProductID: "flour", Quantity: 1_000_000, BaseUnit: warehousecontract.BaseUnitG})
	receipt := system.receive(t, "delivery-flour", "flour", 10_000_000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	proposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{Positions: []servingcontract.Position{{LineNo: 3, DishID: "loaf", Portions: 6}}})
	if err != nil {
		t.Fatalf("prepare order: %v", err)
	}

	request := servingcontract.ConfirmOrderRequest{CommandID: "confirm-c1", ProposalID: proposal.ProposalID}
	if _, err := system.serving.ConfirmOrder(context.Background(), request); !errors.Is(err, warehousecontract.ErrUnknownWriteOutcome) {
		t.Fatalf("first confirmation error = %v, want unknown write outcome", err)
	}
	record, found, err := system.bridge.FindRecordedCommand(context.Background(), request.CommandID)
	if err != nil || !found {
		t.Fatalf("read recorded C1 before retry: found=%v error=%v", found, err)
	}
	wantFingerprint := confirmOrderFingerprint(proposal.ProposalID)
	if record.CommandType != "ConfirmOrder" || record.CommandFingerprint != wantFingerprint {
		t.Fatalf("recorded command identity = (%q, %q), want (ConfirmOrder, %s)", record.CommandType, record.CommandFingerprint, wantFingerprint)
	}
	result, err := system.serving.ConfirmOrder(context.Background(), request)
	if err != nil {
		t.Fatalf("retry same command after lost response (record lookups=%d): %v", ports.findCalls, err)
	}
	if result.Kind != servingcontract.OrderConfirmed || result.Order == nil {
		t.Fatalf("confirmation = %#v, want recovered recorded order", result)
	}
	if len(result.Order.Positions) != 1 || result.Order.Positions[0].LineNo != 1 || len(result.Order.Allocations) != 1 || result.Order.Allocations[0] != (servingcontract.Allocation{ProductID: "flour", BatchID: receipt.BatchIDs[0], Quantity: 6_000_000}) || !result.Order.PlacedAt.Equal(system.clock.Now()) {
		t.Fatalf("recovered order fields = %#v, want assigned line and original exact allocation", result.Order)
	}
	order, found, err := system.bridge.FindOrderByProposal(context.Background(), proposal.ProposalID)
	if err != nil || !found {
		t.Fatalf("find order by proposal: found=%v error=%v", found, err)
	}
	if order.OrderID != result.Order.OrderID {
		t.Fatalf("recovered order ID = %q, confirmation order ID = %q", order.OrderID, result.Order.OrderID)
	}
	history, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	orderEvents := 0
	for _, event := range history.Events {
		if event.Type == warehousecontract.EventOrderPlaced {
			orderEvents++
		}
	}
	if orderEvents != 1 {
		t.Fatalf("recorded order events = %d, want 1", orderEvents)
	}
}

func confirmOrderFingerprint(proposalID string) string {
	input := fmt.Sprintf("ConfirmOrder\x00{\"proposalID\":%q}", proposalID)
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

type unknownBeforeCommit struct {
	servingcontract.OrdersWarehouse
	commitCalls int
	findCalls   int
}

func (w *unknownBeforeCommit) FindRecordedCommand(ctx context.Context, commandID string) (servingcontract.CommandRecord, bool, error) {
	w.findCalls++
	return w.OrdersWarehouse.FindRecordedCommand(ctx, commandID)
}

func (w *unknownBeforeCommit) CommitAcceptedOrder(_ context.Context, _ servingcontract.CommitOrderRequest) (servingcontract.CommitResult, error) {
	w.commitCalls++
	return servingcontract.CommitResult{}, warehousecontract.ErrUnknownWriteOutcome
}

func TestUnknownCommitStaysPendingWithoutLateWriteOrSecondCommand(t *testing.T) {
	var ports *unknownBeforeCommit
	system := newTestSystemWithOrders(t, func(orders servingcontract.OrdersWarehouse) servingcontract.OrdersWarehouse {
		ports = &unknownBeforeCommit{OrdersWarehouse: orders}
		return ports
	})
	system.registerProduct(t, "flour", "Flour", warehousecontract.BaseUnitG)
	system.defineDish(t, "loaf", "Loaf", warehousecontract.Ingredient{ProductID: "flour", Quantity: 1_000_000, BaseUnit: warehousecontract.BaseUnitG})
	system.receive(t, "delivery-flour", "flour", 10_000_000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	proposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{Positions: []servingcontract.Position{{LineNo: 1, DishID: "loaf", Portions: 6}}})
	if err != nil {
		t.Fatalf("prepare order: %v", err)
	}
	request := servingcontract.ConfirmOrderRequest{CommandID: "confirm-c1", ProposalID: proposal.ProposalID}
	if _, err := system.serving.ConfirmOrder(context.Background(), request); !errors.Is(err, warehousecontract.ErrUnknownWriteOutcome) {
		t.Fatalf("first confirmation error = %v, want unknown write outcome", err)
	}
	otherCommand := servingcontract.ConfirmOrderRequest{CommandID: "confirm-c2", ProposalID: proposal.ProposalID}
	first, err := system.serving.ConfirmOrder(context.Background(), otherCommand)
	if err != nil {
		t.Fatalf("different command while first is pending: %v", err)
	}
	if first.Kind != servingcontract.ConfirmationPending {
		t.Fatalf("first result = %#v, want ConfirmationPending", first)
	}
	if ports.commitCalls != 1 {
		t.Fatalf("warehouse commit calls = %d, want the one uncertain attempt", ports.commitCalls)
	}
	if _, found, err := system.bridge.FindOrderByProposal(context.Background(), proposal.ProposalID); err != nil || found {
		t.Fatalf("unexpected recorded order: found=%v error=%v", found, err)
	}
	history, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("get history after pending result: %v", err)
	}
	for _, event := range history.Events {
		if event.Type == warehousecontract.EventOrderPlaced {
			t.Fatalf("late order write observed after pending result: %#v", event)
		}
	}
}

func TestReducedReplacementRequiresExplicitConfirmation(t *testing.T) {
	system := newTestSystem(t)
	system.registerProduct(t, "flour", "Flour", warehousecontract.BaseUnitG)
	system.defineDish(t, "loaf", "Loaf", warehousecontract.Ingredient{ProductID: "flour", Quantity: 1_000_000, BaseUnit: warehousecontract.BaseUnitG})
	system.receive(t, "delivery-flour", "flour", 10_000_000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	proposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{Positions: []servingcontract.Position{{LineNo: 7, DishID: "loaf", Portions: 8}}})
	if err != nil {
		t.Fatalf("prepare order: %v", err)
	}
	if len(proposal.Changes) != 0 {
		t.Fatalf("initial changes = %#v, want none", proposal.Changes)
	}
	_, err = system.warehouse.CommitAcceptedOrder(context.Background(), warehousecontract.CommitOrderRequest{
		CommandID:         "competing-order",
		ProposalID:        "proposal-competing",
		AcceptedPositions: []warehousecontract.AcceptedPosition{{LineNo: 2, DishID: "loaf", Portions: 4}},
	})
	if err != nil {
		t.Fatalf("commit competing warehouse order: %v", err)
	}

	replacement, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "confirm-replacement", ProposalID: proposal.ProposalID})
	if err != nil {
		t.Fatalf("confirm stale proposal: %v", err)
	}
	if replacement.Kind != servingcontract.RequiresConfirmation || replacement.Proposal == nil {
		t.Fatalf("stale proposal result = %#v, want replacement requiring confirmation", replacement)
	}
	if replacement.Proposal.ProposalID == proposal.ProposalID || len(replacement.Proposal.AcceptedPositions) != 1 || replacement.Proposal.AcceptedPositions[0].Portions != 6 {
		t.Fatalf("replacement proposal = %#v, want a new proposal for 6 portions", replacement.Proposal)
	}
	if replacement.Proposal.AcceptedPositions[0].LineNo != 1 || replacement.Proposal.RequestedPositions[0].LineNo != 1 {
		t.Fatalf("replacement line numbers = requested %#v accepted %#v, want original serving line 1", replacement.Proposal.RequestedPositions, replacement.Proposal.AcceptedPositions)
	}
	old, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "confirm-old-again", ProposalID: proposal.ProposalID})
	if err != nil {
		t.Fatalf("confirm superseded proposal: %v", err)
	}
	if old.Kind != servingcontract.ProposalSuperseded {
		t.Fatalf("old proposal result = %#v, want ProposalSuperseded", old)
	}

	approved, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "confirm-replacement-approved", ProposalID: replacement.Proposal.ProposalID})
	if err != nil {
		t.Fatalf("confirm replacement: %v", err)
	}
	if approved.Kind != servingcontract.OrderConfirmed || approved.Order == nil || len(approved.Order.Positions) != 1 || approved.Order.Positions[0].LineNo != 1 || approved.Order.Positions[0].Portions != 6 {
		t.Fatalf("replacement confirmation = %#v, want a confirmed 6-portion order", approved)
	}
}

func TestErrorMappingPreservesSemanticIdentityAndUnknownCause(t *testing.T) {
	if got := mapWarehouseError(warehousecontract.ErrConcurrentModification); !errors.Is(got, servingcontract.ErrConcurrentModification) {
		t.Fatalf("mapped concurrency error %v does not preserve serving sentinel", got)
	}
	unknown := errors.New("storage is unavailable")
	got := mapWarehouseError(unknown)
	if !errors.Is(got, unknown) {
		t.Fatalf("mapped technical error %v does not preserve original cause", got)
	}
}
