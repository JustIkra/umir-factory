package business

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	servingbootstrap "github.com/JustIkra/umir-serving/bootstrap"
	servingcontract "github.com/JustIkra/umir-serving/contract"
	warehousebootstrap "github.com/JustIkra/umir-warehouse/bootstrap"
	warehousecontract "github.com/JustIkra/umir-warehouse/contract"
)

func TestPublicRuntimeOptions(t *testing.T) {
	fixedNow := func() time.Time { return moscowTime(2026, time.October, 10, 10, 0) }
	newID := func() string { return "fixture-id" }
	warehouseValid := warehousebootstrap.Options{Now: fixedNow, NewID: newID, EventsPerSnapshot: 1}
	warehouseCases := []struct {
		name    string
		options warehousebootstrap.Options
	}{
		{name: "missing clock", options: warehousebootstrap.Options{NewID: warehouseValid.NewID, EventsPerSnapshot: 1}},
		{name: "missing IDs", options: warehousebootstrap.Options{Now: warehouseValid.Now, EventsPerSnapshot: 1}},
		{name: "zero snapshot threshold", options: warehousebootstrap.Options{Now: warehouseValid.Now, NewID: warehouseValid.NewID}},
		{name: "negative snapshot threshold", options: warehousebootstrap.Options{Now: warehouseValid.Now, NewID: warehouseValid.NewID, EventsPerSnapshot: -1}},
	}
	for _, test := range warehouseCases {
		t.Run("warehouse/"+test.name, func(t *testing.T) {
			if _, err := warehousebootstrap.NewInMemory(test.options); !errors.Is(err, warehousecontract.ErrInvalidArgument) {
				t.Fatalf("NewInMemory error = %v, want ErrInvalidArgument", err)
			}
		})
	}
	if _, err := warehousebootstrap.NewInMemory(warehouseValid); err != nil {
		t.Fatalf("valid warehouse options rejected: %v", err)
	}

	port := newBridge(nil)
	servingBase := servingbootstrap.Options{Now: fixedNow, NewID: newID, OrdersWarehouse: port, CatalogWarehouse: port, MenuWarehouse: port}
	servingCases := []struct {
		name    string
		options servingbootstrap.Options
	}{
		{name: "missing clock", options: servingbootstrap.Options{NewID: servingBase.NewID, OrdersWarehouse: port, CatalogWarehouse: port, MenuWarehouse: port}},
		{name: "missing IDs", options: servingbootstrap.Options{Now: servingBase.Now, OrdersWarehouse: port, CatalogWarehouse: port, MenuWarehouse: port}},
		{name: "missing orders port", options: servingbootstrap.Options{Now: servingBase.Now, NewID: servingBase.NewID, CatalogWarehouse: port, MenuWarehouse: port}},
		{name: "missing catalog port", options: servingbootstrap.Options{Now: servingBase.Now, NewID: servingBase.NewID, OrdersWarehouse: port, MenuWarehouse: port}},
		{name: "missing menu port", options: servingbootstrap.Options{Now: servingBase.Now, NewID: servingBase.NewID, OrdersWarehouse: port, CatalogWarehouse: port}},
		{name: "typed nil ports", options: servingbootstrap.Options{Now: servingBase.Now, NewID: servingBase.NewID, OrdersWarehouse: (*bridge)(nil), CatalogWarehouse: (*bridge)(nil), MenuWarehouse: (*bridge)(nil)}},
	}
	for _, test := range servingCases {
		t.Run("serving/"+test.name, func(t *testing.T) {
			if _, err := servingbootstrap.NewInMemory(test.options); !errors.Is(err, servingcontract.ErrInvalidArgument) {
				t.Fatalf("NewInMemory error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestBridgeFieldsAndErrors(t *testing.T) {
	when := moscowTime(2026, time.October, 10, 10, 30)
	gotPosition, err := toServingPosition(warehousecontract.Position{LineNo: 41, DishID: "dish-a", Portions: 6})
	if err != nil {
		t.Fatalf("convert position: %v", err)
	}
	if want := (servingcontract.Position{LineNo: 41, DishID: "dish-a", Portions: 6}); gotPosition != want {
		t.Fatalf("position = %#v, want %#v", gotPosition, want)
	}

	gotEvaluation, err := toServingOrderEvaluation(warehousecontract.OrderEvaluation{
		EvaluatedAt:        when,
		StateVersion:       17,
		RequestedPositions: []warehousecontract.Position{{LineNo: 41, DishID: "dish-a", Portions: 8}},
		AcceptedPositions:  []warehousecontract.AcceptedPosition{{LineNo: 41, DishID: "dish-a", Portions: 6}},
		Changes: []warehousecontract.Change{{
			LineNo:            41,
			DishID:            "dish-a",
			RequestedPortions: 8,
			AcceptedPortions:  6,
			Deficits:          []warehousecontract.Deficit{{ProductID: "cheese", RequiredQuantity: 8000, AvailableQuantity: 6000}},
		}},
	})
	if err != nil {
		t.Fatalf("convert order evaluation: %v", err)
	}
	wantEvaluation := servingcontract.OrderEvaluation{
		EvaluatedAt:        when,
		StateVersion:       17,
		RequestedPositions: []servingcontract.Position{{LineNo: 41, DishID: "dish-a", Portions: 8}},
		AcceptedPositions:  []servingcontract.AcceptedPosition{{LineNo: 41, DishID: "dish-a", Portions: 6}},
		Changes: []servingcontract.Change{{
			LineNo:            41,
			DishID:            "dish-a",
			RequestedPortions: 8,
			AcceptedPortions:  6,
			Deficits:          []servingcontract.Deficit{{ProductID: "cheese", RequiredQuantity: 8000, AvailableQuantity: 6000}},
		}},
	}
	if !equalOrderEvaluation(gotEvaluation, wantEvaluation) {
		t.Fatalf("order evaluation = %#v, want %#v", gotEvaluation, wantEvaluation)
	}

	gotMenu, err := toServingMenuEvaluation(warehousecontract.MenuEvaluation{
		EvaluatedAt:  when,
		StateVersion: 17,
		Dishes: []warehousecontract.DishEvaluation{{
			DishID:      "dish-a",
			DishName:    "Cheese plate",
			IsAvailable: true,
			Allocations: []warehousecontract.MenuAllocation{{ProductID: "cheese", ProductName: "Cheese", BatchID: "batch-cheese", Quantity: 40000, BaseUnit: warehousecontract.BaseUnitG, ExpiresAt: when.Add(6 * time.Hour)}},
		}, {
			DishID:   "dish-b",
			DishName: "Cream soup",
			Deficits: []warehousecontract.Deficit{{ProductID: "cream", RequiredQuantity: 2000, AvailableQuantity: 0}},
		}},
	})
	if err != nil {
		t.Fatalf("convert menu evaluation: %v", err)
	}
	if gotMenu.EvaluatedAt != when || gotMenu.StateVersion != 17 || len(gotMenu.Dishes) != 2 {
		t.Fatalf("menu metadata or evaluations were lost: %#v", gotMenu)
	}
	available := gotMenu.Dishes[0]
	if available.DishID != "dish-a" || available.DishName != "Cheese plate" || !available.IsAvailable || len(available.Deficits) != 0 || len(available.Allocations) != 1 {
		t.Fatalf("available evaluation = %#v", available)
	}
	allocation := available.Allocations[0]
	if allocation.ProductID != "cheese" || allocation.ProductName != "Cheese" || allocation.BatchID != "batch-cheese" || allocation.Quantity != 40000 || allocation.BaseUnit != "g" || allocation.ExpiresAt != when.Add(6*time.Hour) {
		t.Fatalf("menu allocation metadata = %#v", allocation)
	}
	if gotMenu.Dishes[1].IsAvailable || len(gotMenu.Dishes[1].Deficits) != 1 || gotMenu.Dishes[1].Deficits[0].AvailableQuantity != 0 {
		t.Fatalf("unavailable evaluation = %#v", gotMenu.Dishes[1])
	}

	semanticErrors := []struct {
		warehouse error
		serving   error
	}{
		{warehousecontract.ErrInvalidArgument, servingcontract.ErrInvalidArgument},
		{warehousecontract.ErrInventoryUnavailable, servingcontract.ErrInventoryUnavailable},
		{warehousecontract.ErrCommandIdentityConflict, servingcontract.ErrCommandIdentityConflict},
		{warehousecontract.ErrConcurrentModification, servingcontract.ErrConcurrentModification},
		{warehousecontract.ErrDishNotFound, servingcontract.ErrDishNotFound},
		{warehousecontract.ErrProductNotFound, servingcontract.ErrProductNotFound},
	}
	for _, pair := range semanticErrors {
		mapped := mapWarehouseError(pair.warehouse)
		if !errors.Is(mapped, pair.serving) || !errors.Is(mapped, pair.warehouse) {
			t.Errorf("mapped %v = %v; want both serving %v and warehouse cause", pair.warehouse, mapped, pair.serving)
		}
	}
	cause := errors.New("warehouse transport failed")
	if got := mapWarehouseError(cause); !errors.Is(got, cause) {
		t.Fatalf("mapped unknown error %v lost cause %v", got, cause)
	}
}

func TestCanonicalConfirmFingerprintReference(t *testing.T) {
	if got, want := confirmOrderFingerprint("p1"), "67610c5d5f3bb1111e064b8ca1f6c8e020c17f3d3dd9651ebb3a3fc69d9ac3b5"; got != want {
		t.Fatalf("ConfirmOrder p1 fingerprint = %s, want %s", got, want)
	}
}

func TestBridgeRejectsUnrepresentableAndMalformedResults(t *testing.T) {
	if _, err := servingLineNoWithin(math.MaxInt32+1, math.MinInt32, math.MaxInt32); !errors.Is(err, errLineNumberOutOfRange) {
		t.Fatalf("simulated 32-bit overflow error = %v, want %v", err, errLineNumberOutOfRange)
	}
	partial, err := toServingOrderEvaluationWithin(warehousecontract.OrderEvaluation{RequestedPositions: []warehousecontract.Position{{LineNo: 3}, {LineNo: math.MaxInt32 + 1}}}, math.MinInt32, math.MaxInt32)
	if !errors.Is(err, errLineNumberOutOfRange) {
		t.Fatalf("partial evaluation conversion error = %v, want %v", err, errLineNumberOutOfRange)
	}
	if len(partial.RequestedPositions) != 0 {
		t.Fatalf("failed conversion returned partial value %#v", partial)
	}

	_, err = toServingCommandRecord(warehousecontract.CommandRecord{
		CommandID:          "cmd-1",
		CommandType:        "BatchExpired",
		CommandFingerprint: "fingerprint-1",
		Result:             warehousecontract.CommandResult{Kind: warehousecontract.CommandResultKind("future-kind")},
	})
	if err == nil {
		t.Fatal("unknown result tag did not fail conversion")
	}
	_, err = toServingCommandRecord(warehousecontract.CommandRecord{
		CommandID:          "cmd-2",
		CommandType:        "ProductRegistered",
		CommandFingerprint: "fingerprint-2",
		Result:             warehousecontract.CommandResult{Kind: warehousecontract.CommandResultProductRegistered},
	})
	if err == nil {
		t.Fatal("missing tagged payload did not fail conversion")
	}
	product := warehousecontract.Product{ProductID: "salt", Name: "Salt", BaseUnit: warehousecontract.BaseUnitG}
	dish := warehousecontract.Dish{DishID: "soup", Name: "Soup"}
	_, err = toServingCommandRecord(warehousecontract.CommandRecord{Result: warehousecontract.CommandResult{Kind: warehousecontract.CommandResultProductRegistered, Product: &product, Dish: &dish}})
	if err == nil {
		t.Fatal("extra mismatched payload did not fail conversion")
	}
	_, found, err := bridgeCommandRecord(warehousecontract.CommandRecord{Result: warehousecontract.CommandResult{Kind: warehousecontract.CommandResultProductRegistered}}, true, nil)
	if !found || err == nil {
		t.Fatalf("malformed recorded command = found %v, error %v; want found=true and an error", found, err)
	}
}

func TestBridgeCommandRecordsPreserveEachResultTag(t *testing.T) {
	when := moscowTime(2026, time.October, 10, 10, 30)
	product := warehousecontract.Product{ProductID: "salt", Name: "Salt", BaseUnit: warehousecontract.BaseUnitG}
	dish := warehousecontract.Dish{DishID: "soup", Name: "Soup", Ingredients: []warehousecontract.Ingredient{{ProductID: "salt", Quantity: 1000, BaseUnit: warehousecontract.BaseUnitG}}}
	order := warehousecontract.Order{OrderID: "order-1", ProposalID: "proposal-1", AcceptedPositions: []warehousecontract.AcceptedPosition{{LineNo: 4, DishID: "soup", Portions: 2}}, Allocations: []warehousecontract.Allocation{{ProductID: "salt", BatchID: "batch-1", Quantity: 2000}}, PlacedAt: when}
	expiry := warehousecontract.ExpiryResult{BatchID: "batch-1", ProductID: "salt", WrittenOffQuantity: 500, Reason: "EXPIRATION", ExpiresAt: when.Add(-time.Hour), ProcessedAt: when}
	delivery := warehousecontract.DeliveryReceipt{DeliveryID: "delivery-1", BatchIDs: []string{"batch-1", "batch-2"}}
	cases := []struct {
		name   string
		result warehousecontract.CommandResult
		want   servingcontract.CommandResult
	}{
		{name: "product", result: warehousecontract.CommandResult{Kind: warehousecontract.CommandResultProductRegistered, Product: &product}, want: servingcontract.CommandResult{Kind: servingcontract.ProductRegistered, Product: &servingcontract.Product{ProductID: "salt", Name: "Salt", BaseUnit: "g"}}},
		{name: "dish", result: warehousecontract.CommandResult{Kind: warehousecontract.CommandResultDishDefined, Dish: &dish}, want: servingcontract.CommandResult{Kind: servingcontract.DishDefined, Dish: &servingcontract.Dish{DishID: "soup", Name: "Soup", Recipe: []servingcontract.Ingredient{{ProductID: "salt", Quantity: 1000, BaseUnit: "g"}}}}},
		{name: "delivery", result: warehousecontract.CommandResult{Kind: warehousecontract.CommandResultDeliveryReceived, Delivery: &delivery}, want: servingcontract.CommandResult{Kind: servingcontract.DeliveryReceived, DeliveryReceipt: &servingcontract.DeliveryReceipt{DeliveryID: "delivery-1", BatchIDs: []string{"batch-1", "batch-2"}}}},
		{name: "order", result: warehousecontract.CommandResult{Kind: warehousecontract.CommandResultOrderPlaced, Order: &order}, want: servingcontract.CommandResult{Kind: servingcontract.OrderPlaced, Order: &servingcontract.Order{OrderID: "order-1", ProposalID: "proposal-1", Positions: []servingcontract.AcceptedPosition{{LineNo: 4, DishID: "soup", Portions: 2}}, Allocations: []servingcontract.Allocation{{ProductID: "salt", BatchID: "batch-1", Quantity: 2000}}, PlacedAt: when}}},
		{name: "expiry", result: warehousecontract.CommandResult{Kind: warehousecontract.CommandResultBatchExpired, Expiry: &expiry}, want: servingcontract.CommandResult{Kind: servingcontract.BatchExpired, ExpiryResult: &servingcontract.ExpiryResult{BatchID: "batch-1", ProductID: "salt", WrittenOffQuantity: 500, Reason: "EXPIRATION", ExpiresAt: when.Add(-time.Hour), ProcessedAt: when}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := toServingCommandRecord(warehousecontract.CommandRecord{CommandID: "cmd-" + test.name, CommandType: test.name, CommandFingerprint: "fingerprint", Result: test.result})
			if err != nil {
				t.Fatalf("convert command record: %v", err)
			}
			want := servingcontract.CommandRecord{CommandID: "cmd-" + test.name, CommandType: test.name, CommandFingerprint: "fingerprint", Result: test.want}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("command record = %#v, want %#v", got, want)
			}
		})
	}
}

func TestBridgeRejectsUnknownCommitResult(t *testing.T) {
	if _, err := toServingCommitResult(warehousecontract.CommitResult{Kind: warehousecontract.CommitResultKind("future-kind")}); err == nil {
		t.Fatal("unknown commit result kind did not fail conversion")
	}
	if _, err := toServingCommitResult(warehousecontract.CommitResult{Kind: warehousecontract.CommitResultOrderCommitted}); err == nil {
		t.Fatal("committed result with nil order did not fail conversion")
	}
}

func equalOrderEvaluation(got, want servingcontract.OrderEvaluation) bool {
	if got.EvaluatedAt != want.EvaluatedAt || got.StateVersion != want.StateVersion || len(got.RequestedPositions) != len(want.RequestedPositions) || len(got.AcceptedPositions) != len(want.AcceptedPositions) || len(got.Changes) != len(want.Changes) {
		return false
	}
	for i := range got.RequestedPositions {
		if got.RequestedPositions[i] != want.RequestedPositions[i] {
			return false
		}
	}
	for i := range got.AcceptedPositions {
		if got.AcceptedPositions[i] != want.AcceptedPositions[i] {
			return false
		}
	}
	for i := range got.Changes {
		left, right := got.Changes[i], want.Changes[i]
		if left.LineNo != right.LineNo || left.DishID != right.DishID || left.RequestedPortions != right.RequestedPortions || left.AcceptedPortions != right.AcceptedPortions || len(left.Deficits) != len(right.Deficits) {
			return false
		}
		for j := range left.Deficits {
			if left.Deficits[j] != right.Deficits[j] {
				return false
			}
		}
	}
	return true
}

func TestServingLineNumberUsesCheckedTargetRange(t *testing.T) {
	line, err := servingLineNoWithin(41, math.MinInt32, math.MaxInt32)
	if err != nil || line != 41 {
		t.Fatalf("valid line conversion = %d, %v", line, err)
	}
	if _, err := servingLineNoWithin(math.MinInt32-1, math.MinInt32, math.MaxInt32); !errors.Is(err, errLineNumberOutOfRange) {
		t.Fatalf("negative overflow error = %v, want %v", err, errLineNumberOutOfRange)
	}
	if _, err := servingLineNoWithin(math.MaxInt32+1, math.MinInt32, math.MaxInt32); !errors.Is(err, errLineNumberOutOfRange) {
		t.Fatalf("positive overflow error = %v, want %v", err, errLineNumberOutOfRange)
	}
}

var _ servingcontract.OrdersWarehouse = (*bridge)(nil)
var _ servingcontract.CatalogWarehouse = (*bridge)(nil)
var _ servingcontract.MenuWarehouse = (*bridge)(nil)

func TestBridgeMissingRuntimeReturnsError(t *testing.T) {
	var missing *bridge
	if _, err := missing.GetProducts(context.Background()); err == nil {
		t.Fatal("nil bridge runtime returned a successful empty catalog")
	}
}

type bridge struct {
	warehouse *warehousebootstrap.Runtime
}

var errLineNumberOutOfRange = errors.New("warehouse line number is not representable by serving")
var errMalformedWarehouseResult = errors.New("malformed warehouse result")
var errBridgeUnavailable = errors.New("warehouse bridge is unavailable")

func newBridge(warehouse *warehousebootstrap.Runtime) *bridge {
	return &bridge{warehouse: warehouse}
}

func (b *bridge) runtime() (*warehousebootstrap.Runtime, error) {
	if b == nil || b.warehouse == nil {
		return nil, errBridgeUnavailable
	}
	return b.warehouse, nil
}

func (b *bridge) EvaluateOrder(ctx context.Context, request servingcontract.EvaluateOrderRequest) (servingcontract.OrderEvaluation, error) {
	warehouse, err := b.runtime()
	if err != nil {
		return servingcontract.OrderEvaluation{}, err
	}
	positions := make([]warehousecontract.Position, len(request.Positions))
	for index, position := range request.Positions {
		positions[index] = warehousecontract.Position{LineNo: int64(position.LineNo), DishID: position.DishID, Portions: position.Portions}
	}
	evaluation, err := warehouse.EvaluateOrder(ctx, warehousecontract.EvaluateOrderRequest{Positions: positions})
	if err != nil {
		return servingcontract.OrderEvaluation{}, mapWarehouseError(err)
	}
	converted, err := toServingOrderEvaluation(evaluation)
	if err != nil {
		return servingcontract.OrderEvaluation{}, err
	}
	return converted, nil
}

func (b *bridge) CommitAcceptedOrder(ctx context.Context, request servingcontract.CommitOrderRequest) (servingcontract.CommitResult, error) {
	warehouse, err := b.runtime()
	if err != nil {
		return servingcontract.CommitResult{}, err
	}
	positions := make([]warehousecontract.AcceptedPosition, len(request.AcceptedPositions))
	for index, position := range request.AcceptedPositions {
		positions[index] = warehousecontract.AcceptedPosition{LineNo: int64(position.LineNo), DishID: position.DishID, Portions: position.Portions}
	}
	result, err := warehouse.CommitAcceptedOrder(ctx, warehousecontract.CommitOrderRequest{
		CommandID:         request.CommandID,
		ProposalID:        request.ProposalID,
		AcceptedPositions: positions,
	})
	if err != nil {
		return servingcontract.CommitResult{}, mapWarehouseError(err)
	}
	converted, err := toServingCommitResult(result)
	if err != nil {
		return servingcontract.CommitResult{}, err
	}
	return converted, nil
}

func (b *bridge) FindRecordedCommand(ctx context.Context, commandID string) (servingcontract.CommandRecord, bool, error) {
	warehouse, err := b.runtime()
	if err != nil {
		return servingcontract.CommandRecord{}, false, err
	}
	record, found, err := warehouse.FindRecordedCommand(ctx, commandID)
	return bridgeCommandRecord(record, found, err)
}

func (b *bridge) FindOrderByProposal(ctx context.Context, proposalID string) (servingcontract.Order, bool, error) {
	warehouse, err := b.runtime()
	if err != nil {
		return servingcontract.Order{}, false, err
	}
	order, found, err := warehouse.FindOrderByProposal(ctx, proposalID)
	if err != nil {
		return servingcontract.Order{}, found, mapWarehouseError(err)
	}
	if !found {
		return servingcontract.Order{}, false, nil
	}
	converted, err := toServingOrder(order)
	if err != nil {
		return servingcontract.Order{}, true, err
	}
	return converted, true, nil
}

func (b *bridge) GetProducts(ctx context.Context) (servingcontract.Products, error) {
	warehouse, err := b.runtime()
	if err != nil {
		return servingcontract.Products{}, err
	}
	products, err := warehouse.GetProducts(ctx)
	if err != nil {
		return servingcontract.Products{}, mapWarehouseError(err)
	}
	items := make([]servingcontract.Product, len(products.Items))
	for index, product := range products.Items {
		items[index] = toServingProduct(product)
	}
	return servingcontract.Products{EvaluatedAt: products.EvaluatedAt, StateVersion: products.StateVersion, Items: items}, nil
}

func (b *bridge) GetDishCatalog(ctx context.Context) (servingcontract.DishCatalog, error) {
	warehouse, err := b.runtime()
	if err != nil {
		return servingcontract.DishCatalog{}, err
	}
	dishes, err := warehouse.GetDishCatalog(ctx)
	if err != nil {
		return servingcontract.DishCatalog{}, mapWarehouseError(err)
	}
	items := make([]servingcontract.Dish, len(dishes.Items))
	for index, dish := range dishes.Items {
		items[index] = toServingDish(dish)
	}
	return servingcontract.DishCatalog{EvaluatedAt: dishes.EvaluatedAt, StateVersion: dishes.StateVersion, Items: items}, nil
}

func (b *bridge) GetDish(ctx context.Context, dishID string) (servingcontract.Dish, error) {
	warehouse, err := b.runtime()
	if err != nil {
		return servingcontract.Dish{}, err
	}
	dish, err := warehouse.GetDish(ctx, dishID)
	if err != nil {
		return servingcontract.Dish{}, mapWarehouseError(err)
	}
	return toServingDish(dish), nil
}

func (b *bridge) RegisterProduct(ctx context.Context, request servingcontract.RegisterProductRequest) (servingcontract.Product, error) {
	warehouse, err := b.runtime()
	if err != nil {
		return servingcontract.Product{}, err
	}
	product, err := warehouse.RegisterProduct(ctx, warehousecontract.RegisterProductRequest{
		CommandID: request.CommandID,
		ProductID: request.ProductID,
		Name:      request.Name,
		BaseUnit:  warehousecontract.BaseUnit(request.BaseUnit),
	})
	if err != nil {
		return servingcontract.Product{}, mapWarehouseError(err)
	}
	return toServingProduct(product), nil
}

func (b *bridge) DefineDish(ctx context.Context, request servingcontract.DefineDishRequest) (servingcontract.Dish, error) {
	warehouse, err := b.runtime()
	if err != nil {
		return servingcontract.Dish{}, err
	}
	ingredients := make([]warehousecontract.Ingredient, len(request.Recipe))
	for index, ingredient := range request.Recipe {
		ingredients[index] = warehousecontract.Ingredient{ProductID: ingredient.ProductID, Quantity: warehousecontract.Quantity(ingredient.Quantity), BaseUnit: warehousecontract.BaseUnit(ingredient.BaseUnit)}
	}
	dish, err := warehouse.DefineDish(ctx, warehousecontract.DefineDishRequest{
		CommandID:   request.CommandID,
		DishID:      request.DishID,
		Name:        request.Name,
		Ingredients: ingredients,
	})
	if err != nil {
		return servingcontract.Dish{}, mapWarehouseError(err)
	}
	return toServingDish(dish), nil
}

func (b *bridge) EvaluateMenu(ctx context.Context) (servingcontract.MenuEvaluation, error) {
	warehouse, err := b.runtime()
	if err != nil {
		return servingcontract.MenuEvaluation{}, err
	}
	evaluation, err := warehouse.EvaluateMenu(ctx)
	if err != nil {
		return servingcontract.MenuEvaluation{}, mapWarehouseError(err)
	}
	return toServingMenuEvaluation(evaluation)
}

func toServingPosition(position warehousecontract.Position) (servingcontract.Position, error) {
	lineNo, err := servingLineNo(position.LineNo)
	if err != nil {
		return servingcontract.Position{}, err
	}
	return servingcontract.Position{LineNo: lineNo, DishID: position.DishID, Portions: position.Portions}, nil
}

func toServingPositionWithin(position warehousecontract.Position, minInt, maxInt int64) (servingcontract.Position, error) {
	lineNo, err := servingLineNoWithin(position.LineNo, minInt, maxInt)
	if err != nil {
		return servingcontract.Position{}, err
	}
	return servingcontract.Position{LineNo: lineNo, DishID: position.DishID, Portions: position.Portions}, nil
}

func toServingAcceptedPosition(position warehousecontract.AcceptedPosition) (servingcontract.AcceptedPosition, error) {
	lineNo, err := servingLineNo(position.LineNo)
	if err != nil {
		return servingcontract.AcceptedPosition{}, err
	}
	return servingcontract.AcceptedPosition{LineNo: lineNo, DishID: position.DishID, Portions: position.Portions}, nil
}

func servingLineNo(lineNo int64) (int, error) {
	maxInt := int64(^uint(0) >> 1)
	return servingLineNoWithin(lineNo, -maxInt-1, maxInt)
}

func servingLineNoWithin(lineNo, minInt, maxInt int64) (int, error) {
	if lineNo < minInt || lineNo > maxInt {
		return 0, errLineNumberOutOfRange
	}
	return int(lineNo), nil
}

func toServingOrderEvaluation(value warehousecontract.OrderEvaluation) (servingcontract.OrderEvaluation, error) {
	maxInt := int64(^uint(0) >> 1)
	return toServingOrderEvaluationWithin(value, -maxInt-1, maxInt)
}

func toServingOrderEvaluationWithin(value warehousecontract.OrderEvaluation, minInt, maxInt int64) (servingcontract.OrderEvaluation, error) {
	requested := make([]servingcontract.Position, len(value.RequestedPositions))
	for index, position := range value.RequestedPositions {
		converted, err := toServingPositionWithin(position, minInt, maxInt)
		if err != nil {
			return servingcontract.OrderEvaluation{}, err
		}
		requested[index] = converted
	}
	accepted := make([]servingcontract.AcceptedPosition, len(value.AcceptedPositions))
	for index, position := range value.AcceptedPositions {
		lineNo, err := servingLineNoWithin(position.LineNo, minInt, maxInt)
		if err != nil {
			return servingcontract.OrderEvaluation{}, err
		}
		accepted[index] = servingcontract.AcceptedPosition{LineNo: lineNo, DishID: position.DishID, Portions: position.Portions}
	}
	changes := make([]servingcontract.Change, len(value.Changes))
	for index, change := range value.Changes {
		lineNo, err := servingLineNoWithin(change.LineNo, minInt, maxInt)
		if err != nil {
			return servingcontract.OrderEvaluation{}, err
		}
		deficits := make([]servingcontract.Deficit, len(change.Deficits))
		for deficitIndex, deficit := range change.Deficits {
			deficits[deficitIndex] = toServingDeficit(deficit)
		}
		changes[index] = servingcontract.Change{LineNo: lineNo, DishID: change.DishID, RequestedPortions: change.RequestedPortions, AcceptedPortions: change.AcceptedPortions, Deficits: deficits}
	}
	return servingcontract.OrderEvaluation{EvaluatedAt: value.EvaluatedAt, StateVersion: value.StateVersion, RequestedPositions: requested, AcceptedPositions: accepted, Changes: changes}, nil
}

func toServingMenuEvaluation(value warehousecontract.MenuEvaluation) (servingcontract.MenuEvaluation, error) {
	dishes := make([]servingcontract.DishEvaluation, len(value.Dishes))
	for index, dish := range value.Dishes {
		deficits := make([]servingcontract.Deficit, len(dish.Deficits))
		for deficitIndex, deficit := range dish.Deficits {
			deficits[deficitIndex] = toServingDeficit(deficit)
		}
		allocations := make([]servingcontract.MenuAllocation, len(dish.Allocations))
		for allocationIndex, allocation := range dish.Allocations {
			allocations[allocationIndex] = servingcontract.MenuAllocation{
				ProductID:   allocation.ProductID,
				ProductName: allocation.ProductName,
				BatchID:     allocation.BatchID,
				Quantity:    int64(allocation.Quantity),
				BaseUnit:    string(allocation.BaseUnit),
				ExpiresAt:   allocation.ExpiresAt,
			}
		}
		dishes[index] = servingcontract.DishEvaluation{DishID: dish.DishID, DishName: dish.DishName, IsAvailable: dish.IsAvailable, Deficits: deficits, Allocations: allocations}
	}
	return servingcontract.MenuEvaluation{EvaluatedAt: value.EvaluatedAt, StateVersion: value.StateVersion, Dishes: dishes}, nil
}

func toServingDeficit(value warehousecontract.Deficit) servingcontract.Deficit {
	return servingcontract.Deficit{ProductID: value.ProductID, RequiredQuantity: int64(value.RequiredQuantity), AvailableQuantity: int64(value.AvailableQuantity)}
}

func toServingProduct(value warehousecontract.Product) servingcontract.Product {
	return servingcontract.Product{ProductID: value.ProductID, Name: value.Name, BaseUnit: string(value.BaseUnit)}
}

func toServingDish(value warehousecontract.Dish) servingcontract.Dish {
	ingredients := make([]servingcontract.Ingredient, len(value.Ingredients))
	for index, ingredient := range value.Ingredients {
		ingredients[index] = servingcontract.Ingredient{ProductID: ingredient.ProductID, Quantity: int64(ingredient.Quantity), BaseUnit: string(ingredient.BaseUnit)}
	}
	return servingcontract.Dish{DishID: value.DishID, Name: value.Name, Recipe: ingredients}
}

func toServingOrder(value warehousecontract.Order) (servingcontract.Order, error) {
	positions := make([]servingcontract.AcceptedPosition, len(value.AcceptedPositions))
	for index, position := range value.AcceptedPositions {
		converted, err := toServingAcceptedPosition(position)
		if err != nil {
			return servingcontract.Order{}, err
		}
		positions[index] = converted
	}
	allocations := make([]servingcontract.Allocation, len(value.Allocations))
	for index, allocation := range value.Allocations {
		allocations[index] = servingcontract.Allocation{ProductID: allocation.ProductID, BatchID: allocation.BatchID, Quantity: int64(allocation.Quantity)}
	}
	return servingcontract.Order{OrderID: value.OrderID, ProposalID: value.ProposalID, Positions: positions, Allocations: allocations, PlacedAt: value.PlacedAt}, nil
}

func toServingCommitResult(value warehousecontract.CommitResult) (servingcontract.CommitResult, error) {
	switch value.Kind {
	case warehousecontract.CommitResultOrderCommitted:
		if value.Order == nil || value.Evaluation != nil {
			return servingcontract.CommitResult{}, fmt.Errorf("%w: inconsistent committed result payload", errMalformedWarehouseResult)
		}
		order, err := toServingOrder(*value.Order)
		if err != nil {
			return servingcontract.CommitResult{}, err
		}
		return servingcontract.CommitResult{Kind: servingcontract.OrderCommitted, Order: &order}, nil
	case warehousecontract.CommitResultRecheckRequired:
		if value.Evaluation == nil || value.Order != nil {
			return servingcontract.CommitResult{}, fmt.Errorf("%w: inconsistent recheck result payload", errMalformedWarehouseResult)
		}
		evaluation, err := toServingOrderEvaluation(*value.Evaluation)
		if err != nil {
			return servingcontract.CommitResult{}, err
		}
		return servingcontract.CommitResult{Kind: servingcontract.RecheckRequired, OrderEvaluation: &evaluation}, nil
	default:
		return servingcontract.CommitResult{}, fmt.Errorf("%w: unknown commit result kind %q", errMalformedWarehouseResult, value.Kind)
	}
}

func toServingCommandRecord(value warehousecontract.CommandRecord) (servingcontract.CommandRecord, error) {
	result := servingcontract.CommandResult{}
	switch value.Result.Kind {
	case warehousecontract.CommandResultProductRegistered:
		if !hasOnlyResultPayload(value.Result.Product != nil, value.Result.Dish != nil, value.Result.Delivery != nil, value.Result.Order != nil, value.Result.Expiry != nil) {
			return servingcontract.CommandRecord{}, fmt.Errorf("%w: product result payload does not match its tag", errMalformedWarehouseResult)
		}
		product := toServingProduct(*value.Result.Product)
		result.Kind, result.Product = servingcontract.ProductRegistered, &product
	case warehousecontract.CommandResultDishDefined:
		if !hasOnlyResultPayload(value.Result.Dish != nil, value.Result.Product != nil, value.Result.Delivery != nil, value.Result.Order != nil, value.Result.Expiry != nil) {
			return servingcontract.CommandRecord{}, fmt.Errorf("%w: dish result payload does not match its tag", errMalformedWarehouseResult)
		}
		dish := toServingDish(*value.Result.Dish)
		result.Kind, result.Dish = servingcontract.DishDefined, &dish
	case warehousecontract.CommandResultDeliveryReceived:
		if !hasOnlyResultPayload(value.Result.Delivery != nil, value.Result.Product != nil, value.Result.Dish != nil, value.Result.Order != nil, value.Result.Expiry != nil) {
			return servingcontract.CommandRecord{}, fmt.Errorf("%w: delivery result payload does not match its tag", errMalformedWarehouseResult)
		}
		delivery := servingcontract.DeliveryReceipt{DeliveryID: value.Result.Delivery.DeliveryID, BatchIDs: append([]string(nil), value.Result.Delivery.BatchIDs...)}
		result.Kind, result.DeliveryReceipt = servingcontract.DeliveryReceived, &delivery
	case warehousecontract.CommandResultOrderPlaced:
		if !hasOnlyResultPayload(value.Result.Order != nil, value.Result.Product != nil, value.Result.Dish != nil, value.Result.Delivery != nil, value.Result.Expiry != nil) {
			return servingcontract.CommandRecord{}, fmt.Errorf("%w: order result payload does not match its tag", errMalformedWarehouseResult)
		}
		order, err := toServingOrder(*value.Result.Order)
		if err != nil {
			return servingcontract.CommandRecord{}, err
		}
		result.Kind, result.Order = servingcontract.OrderPlaced, &order
	case warehousecontract.CommandResultBatchExpired:
		if !hasOnlyResultPayload(value.Result.Expiry != nil, value.Result.Product != nil, value.Result.Dish != nil, value.Result.Delivery != nil, value.Result.Order != nil) {
			return servingcontract.CommandRecord{}, fmt.Errorf("%w: expiry result payload does not match its tag", errMalformedWarehouseResult)
		}
		expiry := toServingExpiry(*value.Result.Expiry)
		result.Kind, result.ExpiryResult = servingcontract.BatchExpired, &expiry
	default:
		return servingcontract.CommandRecord{}, fmt.Errorf("%w: unknown command result kind %q", errMalformedWarehouseResult, value.Result.Kind)
	}
	return servingcontract.CommandRecord{CommandID: value.CommandID, CommandType: value.CommandType, CommandFingerprint: value.CommandFingerprint, Result: result}, nil
}

func bridgeCommandRecord(value warehousecontract.CommandRecord, found bool, err error) (servingcontract.CommandRecord, bool, error) {
	if err != nil {
		return servingcontract.CommandRecord{}, found, mapWarehouseError(err)
	}
	if !found {
		return servingcontract.CommandRecord{}, false, nil
	}
	converted, err := toServingCommandRecord(value)
	if err != nil {
		return servingcontract.CommandRecord{}, true, err
	}
	return converted, true, nil
}

func hasOnlyResultPayload(primary bool, other ...bool) bool {
	if !primary {
		return false
	}
	for _, present := range other {
		if present {
			return false
		}
	}
	return true
}

func toServingExpiry(value warehousecontract.ExpiryResult) servingcontract.ExpiryResult {
	return servingcontract.ExpiryResult{BatchID: value.BatchID, ProductID: value.ProductID, WrittenOffQuantity: int64(value.WrittenOffQuantity), Reason: value.Reason, ExpiresAt: value.ExpiresAt, ProcessedAt: value.ProcessedAt}
}

func mapWarehouseError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, warehousecontract.ErrInvalidArgument):
		return fmt.Errorf("%w: %w", servingcontract.ErrInvalidArgument, err)
	case errors.Is(err, warehousecontract.ErrInventoryUnavailable):
		return fmt.Errorf("%w: %w", servingcontract.ErrInventoryUnavailable, err)
	case errors.Is(err, warehousecontract.ErrCommandIdentityConflict):
		return fmt.Errorf("%w: %w", servingcontract.ErrCommandIdentityConflict, err)
	case errors.Is(err, warehousecontract.ErrConcurrentModification):
		return fmt.Errorf("%w: %w", servingcontract.ErrConcurrentModification, err)
	case errors.Is(err, warehousecontract.ErrDishNotFound):
		return fmt.Errorf("%w: %w", servingcontract.ErrDishNotFound, err)
	case errors.Is(err, warehousecontract.ErrProductNotFound):
		return fmt.Errorf("%w: %w", servingcontract.ErrProductNotFound, err)
	default:
		return err
	}
}
