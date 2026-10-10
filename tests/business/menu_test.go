package business

import (
	"context"
	"errors"
	"reflect"
	"testing"

	servingcontract "github.com/JustIkra/umir-serving/contract"
	warehousecontract "github.com/JustIkra/umir-warehouse/contract"
)

func TestCatalogIncludesProductsAndDishesWithoutDeliveries(t *testing.T) {
	system := newTestSystem(t)
	product, err := system.serving.RegisterProduct(context.Background(), servingcontract.RegisterProductRequest{
		CommandID: "register-salt",
		ProductID: "salt",
		Name:      "Sea salt",
		BaseUnit:  "g",
	})
	if err != nil || product != (servingcontract.Product{ProductID: "salt", Name: "Sea salt", BaseUnit: "g"}) {
		t.Fatalf("register product through serving bridge: product=%#v error=%v", product, err)
	}
	dish, err := system.serving.DefineDish(context.Background(), servingcontract.DefineDishRequest{
		CommandID: "define-soup",
		DishID:    "soup",
		Name:      "Soup",
		Recipe:    []servingcontract.Ingredient{{ProductID: "salt", Quantity: 1000, BaseUnit: "g"}},
	})
	if err != nil || dish.DishID != "soup" || len(dish.Recipe) != 1 || dish.Recipe[0] != (servingcontract.Ingredient{ProductID: "salt", Quantity: 1000, BaseUnit: "g"}) {
		t.Fatalf("define dish through serving bridge: dish=%#v error=%v", dish, err)
	}

	products, err := system.serving.GetProducts(context.Background())
	if err != nil {
		t.Fatalf("get products: %v", err)
	}
	if !products.EvaluatedAt.Equal(system.clock.Now()) || products.StateVersion != 2 || len(products.Items) != 1 || products.Items[0] != (servingcontract.Product{ProductID: "salt", Name: "Sea salt", BaseUnit: "g"}) {
		t.Fatalf("products = %#v, want catalog item and warehouse metadata", products)
	}
	dishes, err := system.serving.GetDishCatalog(context.Background())
	if err != nil {
		t.Fatalf("get dish catalog: %v", err)
	}
	if !dishes.EvaluatedAt.Equal(system.clock.Now()) || dishes.StateVersion != 2 || len(dishes.Items) != 1 || dishes.Items[0].DishID != "soup" || len(dishes.Items[0].Recipe) != 1 {
		t.Fatalf("dish catalog = %#v, want the recipe and warehouse metadata", dishes)
	}
	if got, err := system.serving.GetDish(context.Background(), "soup"); err != nil || !got.EvaluatedAt.Equal(system.clock.Now()) || got.StateVersion != 2 || !reflect.DeepEqual(got.Dish, dishes.Items[0]) {
		t.Fatalf("get dish details = %#v, error=%v; want catalog value and capture metadata", got, err)
	}
	if _, err := system.serving.GetDish(context.Background(), "missing"); !errors.Is(err, servingcontract.ErrDishNotFound) {
		t.Fatalf("unknown dish error = %v, want serving ErrDishNotFound", err)
	}
	menu, err := system.serving.GetMenu(context.Background())
	if err != nil {
		t.Fatalf("get menu: %v", err)
	}
	if len(menu.Dishes) != 0 {
		t.Fatalf("available menu = %#v, want unavailable dish omitted", menu.Dishes)
	}
	stopList, err := system.serving.GetStopList(context.Background())
	if err != nil {
		t.Fatalf("get stop list: %v", err)
	}
	if len(stopList.Dishes) != 1 || stopList.Dishes[0].DishID != "soup" || len(stopList.Dishes[0].Deficits) != 1 || stopList.Dishes[0].Deficits[0].AvailableQuantity != 0 {
		t.Fatalf("stop list = %#v, want soup with its zero-stock deficit", stopList)
	}
}

func TestRecommendationTwoBatches(t *testing.T) {
	system := newTestSystem(t)
	system.registerProduct(t, "cheese", "Cheese", warehousecontract.BaseUnitG)
	system.registerProduct(t, "cream", "Cream", warehousecontract.BaseUnitML)
	system.defineDish(t, "dish-a", "Cheese snack", warehousecontract.Ingredient{ProductID: "cheese", Quantity: 40_000, BaseUnit: warehousecontract.BaseUnitG})
	system.defineDish(t, "dish-b", "Cheese pasta", warehousecontract.Ingredient{ProductID: "cheese", Quantity: 40_000, BaseUnit: warehousecontract.BaseUnitG}, warehousecontract.Ingredient{ProductID: "cream", Quantity: 25_000, BaseUnit: warehousecontract.BaseUnitML})
	cheeseReceipt := system.receive(t, "cheese-delivery", "cheese", 40_000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})
	creamReceipt := system.receive(t, "cream-delivery", "cream", 25_000, warehousecontract.ShelfLife{Value: 1, Unit: warehousecontract.ShelfLifeDay})

	evaluation, err := system.bridge.EvaluateMenu(context.Background())
	if err != nil {
		t.Fatalf("evaluate menu through bridge: %v", err)
	}
	if !evaluation.EvaluatedAt.Equal(system.clock.Now()) || evaluation.StateVersion != 6 || len(evaluation.Dishes) != 2 {
		t.Fatalf("menu evaluation metadata or count = %#v", evaluation)
	}
	for _, dish := range evaluation.Dishes {
		if !dish.IsAvailable || len(dish.Deficits) != 0 {
			t.Fatalf("independent one-portion evaluation = %#v, want available with no deficits", dish)
		}
	}
	if len(evaluation.Dishes[0].Allocations) != 1 || evaluation.Dishes[0].Allocations[0].Quantity != 40_000 || evaluation.Dishes[0].Allocations[0].BatchID != cheeseReceipt.BatchIDs[0] {
		t.Fatalf("first dish allocation = %#v, want its full independent cheese batch", evaluation.Dishes[0].Allocations)
	}
	if len(evaluation.Dishes[1].Allocations) != 2 {
		t.Fatalf("second dish allocations = %#v, want cheese and cream", evaluation.Dishes[1].Allocations)
	}

	recommendations, err := system.serving.GetRecommendations(context.Background())
	if err != nil {
		t.Fatalf("get recommendations: %v", err)
	}
	if recommendations.StateVersion != evaluation.StateVersion || !recommendations.EvaluatedAt.Equal(evaluation.EvaluatedAt) || len(recommendations.Items) != 2 {
		t.Fatalf("recommendations = %#v, want shared menu snapshot metadata", recommendations)
	}
	var pasta *servingcontract.Recommendation
	for index := range recommendations.Items {
		if recommendations.Items[index].DishID == "dish-b" {
			pasta = &recommendations.Items[index]
		}
	}
	if pasta == nil || len(pasta.Reasons) != 2 {
		t.Fatalf("tied-expiry recommendation = %#v, want both equal-expiry batch reasons", pasta)
	}
	if pasta.Reasons[0].ProductID != "cheese" || pasta.Reasons[0].BatchID != cheeseReceipt.BatchIDs[0] || pasta.Reasons[0].QuantityPerPortion != 40_000 || pasta.Reasons[0].BaseUnit != "g" {
		t.Fatalf("first tied-expiry reason = %#v, want exact cheese allocation metadata", pasta.Reasons[0])
	}
	if pasta.Reasons[1].ProductID != "cream" || pasta.Reasons[1].BatchID != creamReceipt.BatchIDs[0] || pasta.Reasons[1].QuantityPerPortion != 25_000 || pasta.Reasons[1].BaseUnit != "ml" {
		t.Fatalf("second tied-expiry reason = %#v, want exact cream allocation metadata", pasta.Reasons[1])
	}
}

func TestBridgeCatalogResultsAreDetachedFromWarehouseState(t *testing.T) {
	system := newTestSystem(t)
	system.registerProduct(t, "salt", "Sea salt", warehousecontract.BaseUnitG)
	system.defineDish(t, "soup", "Soup", warehousecontract.Ingredient{ProductID: "salt", Quantity: 1000, BaseUnit: warehousecontract.BaseUnitG})

	products, err := system.bridge.GetProducts(context.Background())
	if err != nil {
		t.Fatalf("get products: %v", err)
	}
	products.Items[0].Name = "mutated"
	dishes, err := system.bridge.GetDishCatalog(context.Background())
	if err != nil {
		t.Fatalf("get dish catalog: %v", err)
	}
	dishes.Items[0].Recipe[0].Quantity = -1
	details, err := system.bridge.GetDish(context.Background(), "soup")
	if err != nil {
		t.Fatalf("get dish details: %v", err)
	}
	if !details.EvaluatedAt.Equal(system.clock.Now()) || details.StateVersion != 2 || details.Dish.Recipe[0].Quantity != 1000 {
		t.Fatalf("dish details = %#v; want warehouse capture metadata and an intact recipe", details)
	}
	details.Dish.Recipe[0].Quantity = -2
	secondProducts, err := system.bridge.GetProducts(context.Background())
	if err != nil {
		t.Fatalf("get products again: %v", err)
	}
	secondDishes, err := system.bridge.GetDishCatalog(context.Background())
	if err != nil {
		t.Fatalf("get dish catalog again: %v", err)
	}
	secondDetails, err := system.bridge.GetDish(context.Background(), "soup")
	if err != nil {
		t.Fatalf("get dish details again: %v", err)
	}
	if secondProducts.Items[0].Name != "Sea salt" || secondDishes.Items[0].Recipe[0].Quantity != 1000 || secondDetails.Dish.Recipe[0].Quantity != 1000 {
		t.Fatalf("bridge caller mutation reached later reads: products=%#v dishes=%#v details=%#v", secondProducts, secondDishes, secondDetails)
	}
}
