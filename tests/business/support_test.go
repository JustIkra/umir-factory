package business

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	servingbootstrap "github.com/JustIkra/umir-serving/bootstrap"
	servingcontract "github.com/JustIkra/umir-serving/contract"
	warehousebootstrap "github.com/JustIkra/umir-warehouse/bootstrap"
	warehousecontract "github.com/JustIkra/umir-warehouse/contract"
)

type testClock struct {
	mu  sync.RWMutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: moscowTime(2026, time.October, 10, 10, 0)}
}

func moscowTime(year int, month time.Month, day, hour, minute int) time.Time {
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		panic(err)
	}
	return time.Date(year, month, day, hour, minute, 0, 0, location)
}

func (c *testClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *testClock) Set(value time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = value
}

type testIDs struct {
	prefix string
	next   int
}

func (i *testIDs) Next() string {
	i.next++
	return fmt.Sprintf("%s-%d", i.prefix, i.next)
}

type testSystem struct {
	warehouse *warehousebootstrap.Runtime
	serving   *servingbootstrap.Runtime
	bridge    *bridge
	clock     *testClock
}

func newTestSystem(t *testing.T) testSystem {
	t.Helper()
	return newTestSystemWithOrders(t, nil)
}

func newTestSystemWithOrders(t *testing.T, wrap func(servingcontract.OrdersWarehouse) servingcontract.OrdersWarehouse) testSystem {
	t.Helper()
	ctx := context.Background()
	clock := newTestClock()
	warehouseIDs := &testIDs{prefix: "warehouse"}
	warehouse, err := warehousebootstrap.NewInMemory(warehousebootstrap.Options{
		Now:               clock.Now,
		NewID:             warehouseIDs.Next,
		EventsPerSnapshot: 1,
	})
	if err != nil {
		t.Fatalf("create warehouse runtime: %v", err)
	}
	if err := warehouse.Recover(ctx); err != nil {
		t.Fatalf("recover warehouse runtime: %v", err)
	}
	bridge := newBridge(warehouse)
	var orders servingcontract.OrdersWarehouse = bridge
	if wrap != nil {
		orders = wrap(orders)
	}
	servingIDs := &testIDs{prefix: "serving"}
	serving, err := servingbootstrap.NewInMemory(servingbootstrap.Options{
		Now:              clock.Now,
		NewID:            servingIDs.Next,
		OrdersWarehouse:  orders,
		CatalogWarehouse: bridge,
		MenuWarehouse:    bridge,
	})
	if err != nil {
		t.Fatalf("create serving runtime: %v", err)
	}
	return testSystem{warehouse: warehouse, serving: serving, bridge: bridge, clock: clock}
}

func (s testSystem) registerProduct(t *testing.T, id, name string, unit warehousecontract.BaseUnit) {
	t.Helper()
	_, err := s.warehouse.RegisterProduct(context.Background(), warehousecontract.RegisterProductRequest{
		CommandID: "register-" + id,
		ProductID: id,
		Name:      name,
		BaseUnit:  unit,
	})
	if err != nil {
		t.Fatalf("register product %q: %v", id, err)
	}
}

func (s testSystem) defineDish(t *testing.T, id, name string, ingredients ...warehousecontract.Ingredient) {
	t.Helper()
	_, err := s.warehouse.DefineDish(context.Background(), warehousecontract.DefineDishRequest{
		CommandID:   "define-" + id,
		DishID:      id,
		Name:        name,
		Ingredients: ingredients,
	})
	if err != nil {
		t.Fatalf("define dish %q: %v", id, err)
	}
}

func (s testSystem) receive(t *testing.T, deliveryID, productID string, quantity warehousecontract.Quantity, shelfLife warehousecontract.ShelfLife) warehousecontract.DeliveryReceipt {
	t.Helper()
	createdAt := s.clock.Now()
	receipt, err := s.warehouse.ReceiveDelivery(context.Background(), warehousecontract.ReceiveDeliveryRequest{
		CommandID:  "receive-" + deliveryID,
		DeliveryID: deliveryID,
		Number:     "delivery-number-" + deliveryID,
		CreatedAt:  createdAt,
		Lines: []warehousecontract.DeliveryLine{{
			ProductID:      productID,
			ManufacturedAt: createdAt,
			ShelfLife:      shelfLife,
			ReceivedAt:     createdAt,
			Quantity:       quantity,
		}},
	})
	if err != nil {
		t.Fatalf("receive delivery %q: %v", deliveryID, err)
	}
	return receipt
}
