package business

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	servingcontract "github.com/JustIkra/umir-serving/contract"
	warehousecontract "github.com/JustIkra/umir-warehouse/contract"
)

func TestAcceptanceEmptyCatalogsAndMenuThroughBridge(t *testing.T) {
	system := newTestSystem(t)
	products, err := system.serving.GetProducts(context.Background())
	if err != nil {
		t.Fatalf("get empty products: %v", err)
	}
	if products.StateVersion != 0 || len(products.Items) != 0 {
		t.Fatalf("empty products = %#v, want empty version-zero catalog", products)
	}
	dishes, err := system.serving.GetDishCatalog(context.Background())
	if err != nil {
		t.Fatalf("get empty dish catalog: %v", err)
	}
	if dishes.StateVersion != 0 || len(dishes.Items) != 0 {
		t.Fatalf("empty dishes = %#v, want empty version-zero catalog", dishes)
	}
	menu, err := system.serving.GetMenu(context.Background())
	if err != nil {
		t.Fatalf("get empty menu: %v", err)
	}
	if len(menu.Dishes) != 0 {
		t.Fatalf("empty menu = %#v, want no available dishes", menu.Dishes)
	}
	stopList, err := system.serving.GetStopList(context.Background())
	if err != nil {
		t.Fatalf("get empty stop list: %v", err)
	}
	if len(stopList.Dishes) != 0 {
		t.Fatalf("empty stop list = %#v, want no unavailable dishes", stopList.Dishes)
	}
	if _, err := system.serving.GetDish(context.Background(), "unknown"); !errors.Is(err, servingcontract.ErrDishNotFound) {
		t.Fatalf("unknown dish error = %v, want ErrDishNotFound", err)
	}
}

func TestAcceptancePrepareReducesThreeToOneWithoutDebit(t *testing.T) {
	system := newTestSystem(t)
	system.registerProduct(t, "salt", "Salt", warehousecontract.BaseUnitG)
	system.defineDish(t, "soup", "Soup", warehousecontract.Ingredient{ProductID: "salt", Quantity: 1000, BaseUnit: warehousecontract.BaseUnitG})
	system.receive(t, "salt-once", "salt", 1000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	before, err := system.warehouse.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("inventory before prepare: %v", err)
	}
	historyBefore, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("history before prepare: %v", err)
	}

	proposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{
		Positions: []servingcontract.Position{{LineNo: 88, DishID: "soup", Portions: 3}},
	})
	if err != nil {
		t.Fatalf("prepare three portions with one available: %v", err)
	}
	if len(proposal.RequestedPositions) != 1 || proposal.RequestedPositions[0] != (servingcontract.Position{LineNo: 1, DishID: "soup", Portions: 3}) {
		t.Fatalf("requested positions = %#v, want assigned line 1 for 3 portions", proposal.RequestedPositions)
	}
	if len(proposal.AcceptedPositions) != 1 || proposal.AcceptedPositions[0] != (servingcontract.AcceptedPosition{LineNo: 1, DishID: "soup", Portions: 1}) {
		t.Fatalf("accepted positions = %#v, want one accepted portion", proposal.AcceptedPositions)
	}
	if len(proposal.Changes) != 1 || proposal.Changes[0].LineNo != 1 || proposal.Changes[0].RequestedPortions != 3 || proposal.Changes[0].AcceptedPortions != 1 || len(proposal.Changes[0].Deficits) == 0 {
		t.Fatalf("reduction change = %#v, want the one-to-three portion reason", proposal.Changes)
	}
	if deficit := proposal.Changes[0].Deficits[0]; deficit.ProductID != "salt" || deficit.RequiredQuantity != 3000 || deficit.AvailableQuantity != 1000 {
		t.Fatalf("reduction deficit = %#v, want 3000 required and 1000 available", deficit)
	}
	after, err := system.warehouse.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("inventory after prepare: %v", err)
	}
	historyAfter, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("history after prepare: %v", err)
	}
	if before.StateVersion != after.StateVersion || before.Products[0].Quantity != after.Products[0].Quantity || historyBefore.StateVersion != historyAfter.StateVersion || len(historyBefore.Events) != len(historyAfter.Events) {
		t.Fatalf("prepare changed inventory facts: before=%#v after=%#v history versions %d/%d event counts %d/%d", before, after, historyBefore.StateVersion, historyAfter.StateVersion, len(historyBefore.Events), len(historyAfter.Events))
	}
	assertNoOrderPlaced(t, historyAfter.Events)
}

func TestAcceptanceStockIncreaseDoesNotRestoreRemovedPosition(t *testing.T) {
	system := newTestSystem(t)
	system.registerProduct(t, "flour", "Flour", warehousecontract.BaseUnitG)
	system.registerProduct(t, "sugar", "Sugar", warehousecontract.BaseUnitG)
	system.defineDish(t, "loaf", "Loaf", warehousecontract.Ingredient{ProductID: "flour", Quantity: 1000, BaseUnit: warehousecontract.BaseUnitG})
	system.defineDish(t, "cake", "Cake", warehousecontract.Ingredient{ProductID: "sugar", Quantity: 1000, BaseUnit: warehousecontract.BaseUnitG})
	system.receive(t, "flour-one", "flour", 1000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	system.receive(t, "sugar-one", "sugar", 1000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	proposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{Positions: []servingcontract.Position{
		{LineNo: 10, DishID: "loaf", Portions: 1},
		{LineNo: 20, DishID: "cake", Portions: 1},
	}})
	if err != nil {
		t.Fatalf("prepare fully available order: %v", err)
	}
	if len(proposal.RequestedPositions) != 2 || proposal.RequestedPositions[0].LineNo != 1 || proposal.RequestedPositions[1].LineNo != 2 || len(proposal.AcceptedPositions) != 2 || len(proposal.Changes) != 0 {
		t.Fatalf("initial proposal = %#v, want both assigned lines fully accepted", proposal)
	}
	_, err = system.warehouse.CommitAcceptedOrder(context.Background(), warehousecontract.CommitOrderRequest{
		CommandID:         "competing-sugar-order",
		ProposalID:        "competing-sugar-proposal",
		AcceptedPositions: []warehousecontract.AcceptedPosition{{LineNo: 1, DishID: "cake", Portions: 1}},
	})
	if err != nil {
		t.Fatalf("commit competing sugar consumption: %v", err)
	}
	replacement, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "confirm-stale", ProposalID: proposal.ProposalID})
	if err != nil {
		t.Fatalf("confirm after competing consumption: %v", err)
	}
	if replacement.Kind != servingcontract.RequiresConfirmation || replacement.Proposal == nil || replacement.Proposal.ProposalID == proposal.ProposalID {
		t.Fatalf("replacement result = %#v, want a new proposal", replacement)
	}
	if len(replacement.Proposal.RequestedPositions) != 2 || replacement.Proposal.RequestedPositions[0].LineNo != 1 || replacement.Proposal.RequestedPositions[1].LineNo != 2 || len(replacement.Proposal.AcceptedPositions) != 1 || replacement.Proposal.AcceptedPositions[0] != (servingcontract.AcceptedPosition{LineNo: 1, DishID: "loaf", Portions: 1}) {
		t.Fatalf("replacement proposal = %#v, want line 2 removed and line 1 preserved", replacement.Proposal)
	}
	if len(replacement.Proposal.Changes) != 1 || replacement.Proposal.Changes[0].LineNo != 2 || replacement.Proposal.Changes[0].AcceptedPortions != 0 {
		t.Fatalf("replacement changes = %#v, want line 2 removed", replacement.Proposal.Changes)
	}
	old, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "confirm-superseded", ProposalID: proposal.ProposalID})
	if err != nil || old.Kind != servingcontract.ProposalSuperseded || old.SupersededBy != replacement.Proposal.ProposalID {
		t.Fatalf("old proposal response = (%#v, %v), want superseded by replacement", old, err)
	}
	system.receive(t, "sugar-restocked", "sugar", 1000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})

	result, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "confirm-replacement", ProposalID: replacement.Proposal.ProposalID})
	if err != nil {
		t.Fatalf("confirm replacement after restock: %v", err)
	}
	if result.Kind != servingcontract.OrderConfirmed || result.Order == nil || result.Order.ProposalID != replacement.Proposal.ProposalID || len(result.Order.Positions) != 1 || result.Order.Positions[0] != replacement.Proposal.AcceptedPositions[0] {
		t.Fatalf("confirmed result = %#v, want only the originally retained loaf line", result)
	}
	history, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("history after confirmation: %v", err)
	}
	orderEvents := 0
	for _, event := range history.Events {
		if event.Type == warehousecontract.EventOrderPlaced {
			orderEvents++
			if event.Payload.Order != nil && event.Payload.Order.ProposalID == replacement.Proposal.ProposalID {
				accepted := event.Payload.Order.AcceptedPositions
				expected := replacement.Proposal.AcceptedPositions[0]
				if len(accepted) != 1 || accepted[0].LineNo != int64(expected.LineNo) || accepted[0].DishID != expected.DishID || accepted[0].Portions != expected.Portions {
					t.Fatalf("replacement order payload = %#v, want no restored position", event.Payload.Order)
				}
			}
		}
	}
	if orderEvents != 2 {
		t.Fatalf("recorded order events = %d, want competitor and replacement order", orderEvents)
	}
	inventory, err := system.warehouse.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("inventory after replacement confirmation: %v", err)
	}
	if inventory.Products[0].Quantity != 0 || inventory.Products[1].Quantity != 1000 {
		t.Fatalf("inventory after replacement = %#v, want flour consumed and restocked sugar still present", inventory.Products)
	}
}

type acceptanceCommitGate struct {
	servingcontract.OrdersWarehouse
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func (w *acceptanceCommitGate) CommitAcceptedOrder(ctx context.Context, request servingcontract.CommitOrderRequest) (servingcontract.CommitResult, error) {
	w.enterOnce.Do(func() {
		close(w.entered)
		<-w.release
	})
	return w.OrdersWarehouse.CommitAcceptedOrder(ctx, request)
}

func (w *acceptanceCommitGate) unblock() {
	w.releaseOnce.Do(func() { close(w.release) })
}

func TestAcceptanceConcurrentConfirmCrossesExpiryAsOneReplacement(t *testing.T) {
	var gate *acceptanceCommitGate
	system := newTestSystemWithOrders(t, func(orders servingcontract.OrdersWarehouse) servingcontract.OrdersWarehouse {
		gate = &acceptanceCommitGate{OrdersWarehouse: orders, entered: make(chan struct{}), release: make(chan struct{})}
		return gate
	})
	defer gate.unblock()
	system.registerProduct(t, "flour", "Flour", warehousecontract.BaseUnitG)
	system.defineDish(t, "loaf", "Loaf", warehousecontract.Ingredient{ProductID: "flour", Quantity: 1000, BaseUnit: warehousecontract.BaseUnitG})
	receipt := system.receive(t, "flour-due", "flour", 1000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	expiresAt := system.clock.Now().Add(24 * time.Hour)
	proposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{Positions: []servingcontract.Position{{LineNo: 42, DishID: "loaf", Portions: 1}}})
	if err != nil || len(proposal.Changes) != 0 {
		t.Fatalf("prepare before expiry = (%#v, %v), want fully accepted proposal", proposal, err)
	}
	system.clock.Set(expiresAt.Add(-time.Nanosecond))

	type confirmOutcome struct {
		result servingcontract.ConfirmResult
		err    error
	}
	firstDone := make(chan confirmOutcome, 1)
	go func() {
		result, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "confirm-before-expiry", ProposalID: proposal.ProposalID})
		firstDone <- confirmOutcome{result: result, err: err}
	}()
	<-gate.entered
	secondStarted := make(chan struct{})
	secondDone := make(chan confirmOutcome, 1)
	go func() {
		close(secondStarted)
		result, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "confirm-while-first-pending", ProposalID: proposal.ProposalID})
		secondDone <- confirmOutcome{result: result, err: err}
	}()
	<-secondStarted
	system.clock.Set(expiresAt)
	gate.unblock()
	first := <-firstDone
	second := <-secondDone
	if first.err != nil || first.result.Kind != servingcontract.RequiresConfirmation || first.result.Proposal == nil {
		t.Fatalf("first boundary confirmation = (%#v, %v), want replacement after expiry", first.result, first.err)
	}
	if len(first.result.Proposal.AcceptedPositions) != 0 {
		t.Fatalf("expiry replacement accepted positions = %#v, want empty", first.result.Proposal.AcceptedPositions)
	}
	if second.err != nil || second.result.Kind != servingcontract.ProposalSuperseded || second.result.SupersededBy != first.result.Proposal.ProposalID {
		t.Fatalf("second concurrent confirmation = (%#v, %v), want superseded by the single replacement", second.result, second.err)
	}
	history, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("history after concurrent expiry boundary: %v", err)
	}
	if got := countAcceptanceEvents(history.Events, warehousecontract.EventBatchExpired); got != 1 {
		t.Fatalf("BatchExpired events = %d, want one", got)
	}
	assertAcceptanceExpiryEvent(t, history.Events, expiresAt, receipt.BatchIDs[0], 1000)
	assertNoOrderPlaced(t, history.Events)
	if len(history.Events) == 0 || history.Events[len(history.Events)-1].Type != warehousecontract.EventBatchExpired {
		t.Fatalf("last event = %#v, want expiry before a replacement with no sale", history.Events[len(history.Events)-1])
	}
}

type acceptanceCommitBarrier struct {
	servingcontract.OrdersWarehouse
	mu       sync.Mutex
	arrivals int
	release  chan struct{}
}

func (w *acceptanceCommitBarrier) CommitAcceptedOrder(ctx context.Context, request servingcontract.CommitOrderRequest) (servingcontract.CommitResult, error) {
	w.mu.Lock()
	w.arrivals++
	if w.arrivals == 2 {
		close(w.release)
	}
	w.mu.Unlock()
	select {
	case <-w.release:
	case <-ctx.Done():
		return servingcontract.CommitResult{}, ctx.Err()
	}
	return w.OrdersWarehouse.CommitAcceptedOrder(ctx, request)
}

func TestAcceptanceDifferentProposalsCompeteForLimitedStock(t *testing.T) {
	var barrier *acceptanceCommitBarrier
	system := newTestSystemWithOrders(t, func(orders servingcontract.OrdersWarehouse) servingcontract.OrdersWarehouse {
		barrier = &acceptanceCommitBarrier{OrdersWarehouse: orders, release: make(chan struct{})}
		return barrier
	})
	system.registerProduct(t, "flour", "Flour", warehousecontract.BaseUnitG)
	system.defineDish(t, "loaf", "Loaf", warehousecontract.Ingredient{ProductID: "flour", Quantity: 1000, BaseUnit: warehousecontract.BaseUnitG})
	system.receive(t, "single-flour", "flour", 1000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	firstProposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{Positions: []servingcontract.Position{{LineNo: 70, DishID: "loaf", Portions: 1}}})
	if err != nil {
		t.Fatalf("prepare first competing proposal: %v", err)
	}
	secondProposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{Positions: []servingcontract.Position{{LineNo: 80, DishID: "loaf", Portions: 1}}})
	if err != nil {
		t.Fatalf("prepare second competing proposal: %v", err)
	}
	if firstProposal.ProposalID == secondProposal.ProposalID || len(firstProposal.AcceptedPositions) != 1 || len(secondProposal.AcceptedPositions) != 1 {
		t.Fatalf("competing proposals = %#v / %#v, want two distinct proposals each prepared from the same stock", firstProposal, secondProposal)
	}
	type confirmOutcome struct {
		proposalID string
		result     servingcontract.ConfirmResult
		err        error
	}
	results := make(chan confirmOutcome, 2)
	go func() {
		result, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "compete-first", ProposalID: firstProposal.ProposalID})
		results <- confirmOutcome{proposalID: firstProposal.ProposalID, result: result, err: err}
	}()
	go func() {
		result, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "compete-second", ProposalID: secondProposal.ProposalID})
		results <- confirmOutcome{proposalID: secondProposal.ProposalID, result: result, err: err}
	}()
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("competing confirmations errors = %v / %v", first.err, second.err)
	}
	confirmed := 0
	rechecked := 0
	winnerID := ""
	for _, outcome := range []confirmOutcome{first, second} {
		switch outcome.result.Kind {
		case servingcontract.OrderConfirmed:
			confirmed++
			winnerID = outcome.proposalID
			if outcome.result.Order == nil || len(outcome.result.Order.Positions) != 1 || outcome.result.Order.Positions[0].Portions != 1 {
				t.Fatalf("winner result = %#v, want one confirmed portion", outcome.result)
			}
		case servingcontract.RequiresConfirmation:
			rechecked++
			if outcome.result.Proposal == nil || len(outcome.result.Proposal.AcceptedPositions) != 0 {
				t.Fatalf("loser result = %#v, want empty rechecked proposal", outcome.result)
			}
		default:
			t.Fatalf("competing result = %#v, want order or recheck", outcome.result)
		}
	}
	if confirmed != 1 || rechecked != 1 {
		t.Fatalf("confirmed=%d rechecked=%d, want one each", confirmed, rechecked)
	}
	if _, found, err := system.bridge.FindOrderByProposal(context.Background(), winnerID); err != nil || !found {
		t.Fatalf("winner order lookup: found=%v error=%v", found, err)
	}
	history, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("history after competing confirmations: %v", err)
	}
	if got := countAcceptanceEvents(history.Events, warehousecontract.EventOrderPlaced); got != 1 {
		t.Fatalf("OrderPlaced events = %d, want exactly one", got)
	}
	inventory, err := system.warehouse.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("inventory after competing confirmations: %v", err)
	}
	if inventory.Products[0].Quantity != 0 {
		t.Fatalf("remaining flour = %d, want 0 with no oversell", inventory.Products[0].Quantity)
	}
}

type unavailableCommitOnce struct {
	servingcontract.OrdersWarehouse
	calls int
}

func (w *unavailableCommitOnce) CommitAcceptedOrder(ctx context.Context, request servingcontract.CommitOrderRequest) (servingcontract.CommitResult, error) {
	w.calls++
	if w.calls == 1 {
		return servingcontract.CommitResult{}, servingcontract.ErrInventoryUnavailable
	}
	return w.OrdersWarehouse.CommitAcceptedOrder(ctx, request)
}

func TestAcceptanceDefiniteUnavailableKeepsProposalRetryable(t *testing.T) {
	var ports *unavailableCommitOnce
	system := newTestSystemWithOrders(t, func(orders servingcontract.OrdersWarehouse) servingcontract.OrdersWarehouse {
		ports = &unavailableCommitOnce{OrdersWarehouse: orders}
		return ports
	})
	system.registerProduct(t, "flour", "Flour", warehousecontract.BaseUnitG)
	system.defineDish(t, "loaf", "Loaf", warehousecontract.Ingredient{ProductID: "flour", Quantity: 1000, BaseUnit: warehousecontract.BaseUnitG})
	system.receive(t, "flour-stock", "flour", 1000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	proposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{Positions: []servingcontract.Position{{LineNo: 50, DishID: "loaf", Portions: 1}}})
	if err != nil {
		t.Fatalf("prepare retryable order: %v", err)
	}
	request := servingcontract.ConfirmOrderRequest{CommandID: "retry-same-command", ProposalID: proposal.ProposalID}
	if _, err := system.serving.ConfirmOrder(context.Background(), request); !errors.Is(err, servingcontract.ErrInventoryUnavailable) {
		t.Fatalf("first confirmation error = %v, want definite InventoryUnavailable", err)
	}
	history, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("history after definite failure: %v", err)
	}
	assertNoOrderPlaced(t, history.Events)

	replayed, err := system.serving.ConfirmOrder(context.Background(), request)
	if !errors.Is(err, servingcontract.ErrInventoryUnavailable) || replayed.Kind != "" {
		t.Fatalf("same-command retry = (%#v, %v), want saved definite refusal", replayed, err)
	}
	if ports.calls != 1 {
		t.Fatalf("commit attempts after exact refusal replay = %d, want 1", ports.calls)
	}
	history, err = system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("history after exact refusal replay: %v", err)
	}
	assertNoOrderPlaced(t, history.Events)

	allowed, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "retry-new-command", ProposalID: proposal.ProposalID})
	if err != nil || allowed.Kind != servingcontract.OrderConfirmed || allowed.Order == nil {
		t.Fatalf("new-command retry = (%#v, %v), want successful order", allowed, err)
	}
	if ports.calls != 2 {
		t.Fatalf("commit attempts after new command = %d, want 2", ports.calls)
	}
	history, err = system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("history after new-command retry: %v", err)
	}
	if countAcceptanceEvents(history.Events, warehousecontract.EventOrderPlaced) != 1 {
		t.Fatalf("history after new-command retry has %d order events, want one", countAcceptanceEvents(history.Events, warehousecontract.EventOrderPlaced))
	}
	inventory, err := system.warehouse.GetInventory(context.Background())
	if err != nil || len(inventory.Products) != 1 || inventory.Products[0].Quantity != 0 {
		t.Fatalf("inventory after allowed retry = %#v, error=%v; want one stock decrement", inventory, err)
	}
}

func TestAcceptanceMissedTickConfirmExpiresBeforeRecheck(t *testing.T) {
	system := newTestSystem(t)
	system.registerProduct(t, "flour", "Flour", warehousecontract.BaseUnitG)
	system.defineDish(t, "loaf", "Loaf", warehousecontract.Ingredient{ProductID: "flour", Quantity: 1000, BaseUnit: warehousecontract.BaseUnitG})
	receipt := system.receive(t, "flour-due", "flour", 1000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	expiresAt := system.clock.Now().Add(24 * time.Hour)
	proposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{Positions: []servingcontract.Position{{LineNo: 12, DishID: "loaf", Portions: 1}}})
	if err != nil {
		t.Fatalf("prepare before deadline: %v", err)
	}
	system.clock.Set(expiresAt)

	result, err := system.serving.ConfirmOrder(context.Background(), servingcontract.ConfirmOrderRequest{CommandID: "confirm-after-missed-tick", ProposalID: proposal.ProposalID})
	if err != nil {
		t.Fatalf("confirm after missed tick: %v", err)
	}
	if result.Kind != servingcontract.RequiresConfirmation || result.Proposal == nil || len(result.Proposal.AcceptedPositions) != 0 {
		t.Fatalf("confirmation after missed tick = %#v, want an empty replacement after expiry", result)
	}
	history, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("history after missed tick confirmation: %v", err)
	}
	if countAcceptanceEvents(history.Events, warehousecontract.EventBatchExpired) != 1 {
		t.Fatalf("missed-tick expiry events = %d, want one", countAcceptanceEvents(history.Events, warehousecontract.EventBatchExpired))
	}
	assertAcceptanceExpiryEvent(t, history.Events, expiresAt, receipt.BatchIDs[0], 1000)
	assertNoOrderPlaced(t, history.Events)
	if len(history.Events) == 0 || history.Events[len(history.Events)-1].Type != warehousecontract.EventBatchExpired {
		t.Fatal("expiry was not the final warehouse event before the replacement response")
	}
}

func TestAcceptanceExpiredReadAndPrepareWriteOnlyExpiry(t *testing.T) {
	system := newTestSystem(t)
	system.registerProduct(t, "flour", "Flour", warehousecontract.BaseUnitG)
	system.defineDish(t, "loaf", "Loaf", warehousecontract.Ingredient{ProductID: "flour", Quantity: 1000, BaseUnit: warehousecontract.BaseUnitG})
	receipt := system.receive(t, "expired-flour", "flour", 1000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	expiresAt := system.clock.Now().Add(24 * time.Hour)
	system.clock.Set(expiresAt)

	menu, err := system.serving.GetMenu(context.Background())
	if err != nil {
		t.Fatalf("read menu on expired stock: %v", err)
	}
	if len(menu.Dishes) != 0 {
		t.Fatalf("menu on expired stock = %#v, want empty", menu.Dishes)
	}
	proposal, err := system.serving.PrepareOrder(context.Background(), servingcontract.PrepareOrderRequest{Positions: []servingcontract.Position{{LineNo: 1, DishID: "loaf", Portions: 1}}})
	if err != nil {
		t.Fatalf("prepare against expired stock: %v", err)
	}
	if len(proposal.AcceptedPositions) != 0 || len(proposal.Changes) != 1 {
		t.Fatalf("proposal against expired stock = %#v, want no accepted portion and one reason", proposal)
	}
	history, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("history after expired read and prepare: %v", err)
	}
	if countAcceptanceEvents(history.Events, warehousecontract.EventBatchExpired) != 1 {
		t.Fatalf("read and prepare expiry events = %d, want a single system expiry", countAcceptanceEvents(history.Events, warehousecontract.EventBatchExpired))
	}
	assertAcceptanceExpiryEvent(t, history.Events, expiresAt, receipt.BatchIDs[0], 1000)
	assertNoOrderPlaced(t, history.Events)
	inventory, err := system.warehouse.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("inventory after expired read and prepare: %v", err)
	}
	if inventory.Products[0].Quantity != 0 {
		t.Fatalf("expired flour balance = %d, want 0", inventory.Products[0].Quantity)
	}
}

func TestAcceptanceRecommendationsUseFEFOAndRecalculateAfterExpiry(t *testing.T) {
	system := newTestSystem(t)
	system.registerProduct(t, "cheese", "Cheese", warehousecontract.BaseUnitG)
	system.defineDish(t, "toast", "Toast", warehousecontract.Ingredient{ProductID: "cheese", Quantity: 100_000, BaseUnit: warehousecontract.BaseUnitG})
	now := system.clock.Now()
	expiresEarly := now.Add(2 * time.Hour)
	expiresLate := now.Add(24 * time.Hour)
	early := receiveAcceptanceDelivery(t, system, "cheese-early", "cheese", 40_000, now.Add(-22*time.Hour))
	late := receiveAcceptanceDelivery(t, system, "cheese-late", "cheese", 100_000, now)

	first, err := system.serving.GetRecommendations(context.Background())
	if err != nil {
		t.Fatalf("initial recommendations: %v", err)
	}
	if !first.EvaluatedAt.Equal(now) || len(first.Items) != 1 || !first.Items[0].PriorityExpiresAt.Equal(expiresEarly) || len(first.Items[0].Reasons) != 1 {
		t.Fatalf("initial recommendation = %#v, want earliest FEFO reason", first)
	}
	firstReason := first.Items[0].Reasons[0]
	if firstReason.ProductID != "cheese" || firstReason.BatchID != early.BatchIDs[0] || firstReason.QuantityPerPortion != 40_000 || !firstReason.ExpiresAt.Equal(expiresEarly) {
		t.Fatalf("initial reason = %#v, want only the actual 40000-unit FEFO allocation", firstReason)
	}

	system.clock.Set(expiresEarly)
	second, err := system.serving.GetRecommendations(context.Background())
	if err != nil {
		t.Fatalf("recommendations after due time: %v", err)
	}
	if !second.EvaluatedAt.Equal(expiresEarly) || second.StateVersion != first.StateVersion+1 || len(second.Items) != 1 || !second.Items[0].PriorityExpiresAt.Equal(expiresLate) || len(second.Items[0].Reasons) != 1 {
		t.Fatalf("recommendation after expiry = %#v, want a fresh later-batch result", second)
	}
	secondReason := second.Items[0].Reasons[0]
	if secondReason.BatchID != late.BatchIDs[0] || secondReason.QuantityPerPortion != 100_000 || !secondReason.ExpiresAt.Equal(expiresLate) {
		t.Fatalf("recalculated reason = %#v, want all 100000 units from the later batch", secondReason)
	}
	history, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("history after recommendation refresh: %v", err)
	}
	if countAcceptanceEvents(history.Events, warehousecontract.EventBatchExpired) != 1 {
		t.Fatalf("recommendation-triggered expiry events = %d, want one", countAcceptanceEvents(history.Events, warehousecontract.EventBatchExpired))
	}
	assertAcceptanceExpiryEvent(t, history.Events, expiresEarly, early.BatchIDs[0], 40_000)
	assertNoOrderPlaced(t, history.Events)
	inventory, err := system.warehouse.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("inventory after recommendation refresh: %v", err)
	}
	if inventory.Products[0].Quantity != 100_000 {
		t.Fatalf("remaining cheese = %d, want unexpired 100000-unit batch and no recommendation sale", inventory.Products[0].Quantity)
	}
}

func receiveAcceptanceDelivery(t *testing.T, system testSystem, deliveryID, productID string, quantity warehousecontract.Quantity, manufacturedAt time.Time) warehousecontract.DeliveryReceipt {
	t.Helper()
	now := system.clock.Now()
	receipt, err := system.warehouse.ReceiveDelivery(context.Background(), warehousecontract.ReceiveDeliveryRequest{
		CommandID:  "receive-" + deliveryID,
		DeliveryID: deliveryID,
		Number:     "delivery-number-" + deliveryID,
		CreatedAt:  now,
		Lines: []warehousecontract.DeliveryLine{{
			ProductID:      productID,
			ManufacturedAt: manufacturedAt,
			ShelfLife:      warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay},
			ReceivedAt:     now,
			Quantity:       quantity,
		}},
	})
	if err != nil {
		t.Fatalf("receive delivery %q: %v", deliveryID, err)
	}
	return receipt
}

func countAcceptanceEvents(events []warehousecontract.Event, eventType warehousecontract.EventType) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func assertNoOrderPlaced(t *testing.T, events []warehousecontract.Event) {
	t.Helper()
	if count := countAcceptanceEvents(events, warehousecontract.EventOrderPlaced); count != 0 {
		t.Fatalf("OrderPlaced events = %d, want none", count)
	}
}

func assertAcceptanceExpiryEvent(t *testing.T, events []warehousecontract.Event, at time.Time, batchID string, quantity warehousecontract.Quantity) {
	t.Helper()
	for _, event := range events {
		if event.Type != warehousecontract.EventBatchExpired {
			continue
		}
		if event.Payload.Expiry == nil || event.Payload.Expiry.BatchID != batchID || event.Payload.Expiry.WrittenOffQuantity != quantity || event.Payload.Expiry.Reason != "EXPIRATION" || !event.Payload.Expiry.ExpiresAt.Equal(at) || !event.Payload.Expiry.ProcessedAt.Equal(at) {
			t.Fatalf("BatchExpired payload = %#v, want batch %q, quantity %d at %s", event.Payload.Expiry, batchID, quantity, at)
		}
		return
	}
	t.Fatalf("BatchExpired event for batch %q was absent", batchID)
}
