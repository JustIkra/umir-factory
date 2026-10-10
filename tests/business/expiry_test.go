package business

import (
	"context"
	"testing"
	"time"

	servingcontract "github.com/JustIkra/umir-serving/contract"
	warehousecontract "github.com/JustIkra/umir-warehouse/contract"
)

func TestExpiryCommandResultBridgePreservesRecordedFacts(t *testing.T) {
	system := newTestSystem(t)
	system.registerProduct(t, "salt", "Sea salt", warehousecontract.BaseUnitG)
	receipt := system.receive(t, "salt-delivery", "salt", 2500, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	expiresAt := system.clock.Now().Add(24 * time.Hour)
	system.clock.Set(expiresAt)
	results, err := system.warehouse.ProcessDueExpirations(context.Background())
	if err != nil {
		t.Fatalf("process due expirations: %v", err)
	}
	if len(results) != 1 || results[0].WrittenOffQuantity != 2500 || results[0].ProductID != "salt" || !results[0].ExpiresAt.Equal(expiresAt) || !results[0].ProcessedAt.Equal(expiresAt) {
		t.Fatalf("expiry results = %#v, want the exact expired batch facts", results)
	}

	history, err := system.warehouse.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	var expiryCommandID string
	for _, event := range history.Events {
		if event.Type == warehousecontract.EventBatchExpired {
			expiryCommandID = event.CommandID
		}
	}
	if expiryCommandID == "" {
		t.Fatal("expiry event did not contain a command ID")
	}
	record, found, err := system.bridge.FindRecordedCommand(context.Background(), expiryCommandID)
	if err != nil || !found {
		t.Fatalf("find expiry command: found=%v error=%v", found, err)
	}
	if record.Result.Kind != servingcontract.BatchExpired || record.Result.ExpiryResult == nil {
		t.Fatalf("bridged expiry result = %#v, want BatchExpired payload", record.Result)
	}
	got := record.Result.ExpiryResult
	if got.BatchID != receipt.BatchIDs[0] || got.ProductID != "salt" || got.WrittenOffQuantity != 2500 || got.Reason != "EXPIRATION" || !got.ExpiresAt.Equal(expiresAt) || !got.ProcessedAt.Equal(expiresAt) {
		t.Fatalf("bridged expiry facts = %#v, want all recorded fields", got)
	}
}
