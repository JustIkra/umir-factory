package main

import (
	"errors"
	"net/url"
	"sync"
	"time"
)

func e34FirstFree(s *e34Scenario) error {
	for _, id := range []string{"1", "2", "4", "01", "manual"} {
		if err := s.product(0, id, "Product "+id, "g"); err != nil {
			return err
		}
	}
	if err := s.dish(0, "1", "Dish 1", []any{map[string]any{"productID": "1", "quantity": "1"}}); err != nil {
		return err
	}
	before := make([]map[string]any, len(s.input.Scopes))
	beforeEvents := make([][]map[string]any, len(s.input.Scopes))
	for index := range s.input.Scopes {
		var err error
		before[index], err = s.read(index, "products", nil)
		if err != nil {
			return err
		}
		beforeEvents[index], err = s.events(index)
		if err != nil {
			return err
		}
	}
	for repeat := 0; repeat < 2; repeat++ {
		for _, probe := range []struct {
			index      int
			kind, want string
		}{{0, "product", "3"}, {0, "dish", "2"}, {1, "product", "1"}} {
			reply, err := s.read(probe.index, "suggestID", url.Values{"kind": {probe.kind}})
			if err != nil {
				return err
			}
			if err := errors.Join(e34Expect("/catalog/suggestions", e34Field(reply, "result", "suggestedID"), probe.want), e34Expect("/catalog/suggestionVersionUnchanged", e34Field(reply, "result", "reserved"), false)); err != nil {
				return err
			}
		}
	}
	for index := range s.input.Scopes {
		after, err := s.read(index, "products", nil)
		if err != nil {
			return err
		}
		afterEvents, err := s.events(index)
		if err != nil {
			return err
		}
		if err := errors.Join(e34Expect("/catalog/suggestionVersionUnchanged", e34Field(after, "context", "stateVersion"), e34Field(before[index], "context", "stateVersion")), e34Expect("/catalog/suggestionVersionUnchanged", e34Field(after, "context", "runtimeEpoch"), e34Field(before[index], "context", "runtimeEpoch")), e34Expect("/catalog/suggestionVersionUnchanged", afterEvents, beforeEvents[index])); err != nil {
			return err
		}
	}
	if err := s.product(0, "ручной/01", "Edited ID", "g"); err != nil {
		return err
	}
	edited, err := s.read(0, "products", nil)
	if err != nil {
		return err
	}
	if _, err := e34Row(edited, "productID", "ручной/01", "result", "items"); err != nil {
		return err
	}
	epoch, ok := e34Field(edited, "context", "runtimeEpoch").(string)
	if !ok || epoch == "" {
		return errors.New("race epoch missing")
	}
	commands := make([]string, 2)
	for i := range commands {
		commands[i], err = e34ID()
		if err != nil {
			return err
		}
	}
	replies := make([]map[string]any, 2)
	failures := make([]error, 2)
	var joined sync.WaitGroup
	for i := range commands {
		joined.Go(func() {
			name := []string{"Competing A", "Competing B"}[i]
			replies[i], failures[i] = s.call(0, "registerProduct", nil, map[string]any{"commandID": commands[i], "productID": "3", "name": name, "baseUnit": "g"}, map[string]string{"X-Umir-Runtime-Epoch": epoch}, 200, 409)
		})
	}
	joined.Wait()
	if err := errors.Join(failures...); err != nil {
		return err
	}
	winners, conflicts := 0, 0
	for _, reply := range replies {
		if e34Field(reply, "result", "productID") == "3" {
			winners++
		} else if reply["code"] == "ProductConflict" && reply["outcome"] == "not_committed" {
			conflicts++
		} else {
			return errors.New("race returned no exact definition/conflict fact")
		}
	}
	events, err := s.events(0)
	if err != nil {
		return err
	}
	facts := 0
	for _, event := range events {
		if event["type"] == "ProductRegistered" && e34Field(event, "payload", "product", "productID") == "3" {
			facts++
		}
	}
	catalog, err := s.read(0, "products", nil)
	if err != nil {
		return err
	}
	rows, err := e34Rows(catalog, "result", "items")
	if err != nil {
		return err
	}
	return errors.Join(e34Expect("/catalog/competingDefinitionFacts", facts, 1), e34Expect("/catalog/loserConflictWithoutRename", winners == 1 && conflicts == 1 && len(rows) == 7, true))
}

func e34CatalogIsolation(s *e34Scenario) error {
	definitions := make([]map[string]any, len(s.input.Scopes))
	for index := range s.input.Scopes {
		units := []string{"g", "ml"}
		if index == 1 {
			units = []string{"ml", "g"}
		}
		for i, id := range []string{"same", "other"} {
			if err := s.product(index, id, []string{"Tenant A", "Tenant B"}[index]+id, units[i]); err != nil {
				return err
			}
		}
		ingredients := []any{map[string]any{"productID": "same", "quantity": "1", "baseUnit": units[0]}}
		name := []string{"Dish A", "Dish B"}[index]
		definitions[index] = map[string]any{"dishID": "same-dish", "name": name, "ingredients": ingredients}
		if err := s.dish(index, "same-dish", name, ingredients); err != nil {
			return err
		}
		now, err := s.clock()
		if err != nil {
			return err
		}
		quantity := []string{"9007199254740993", "3000"}[index]
		if _, err := s.supply(index, "same", quantity, now.Add(24*time.Hour)); err != nil {
			return err
		}
		if _, err := s.supply(index, "other", "2000", now.Add(24*time.Hour)); err != nil {
			return err
		}
	}
	for index := range s.input.Scopes {
		for _, pair := range [][2]string{{"products", "servingProducts"}, {"dishCatalog", "servingDishes"}} {
			warehouse, err := s.read(index, pair[0], nil)
			if err != nil {
				return err
			}
			serving, err := s.read(index, pair[1], nil)
			if err != nil {
				return err
			}
			if err := e34Expect("/catalog/tenantIndependent", e34Field(serving, "result", "items"), e34Field(warehouse, "result", "items")); err != nil {
				return err
			}
			if pair[0] == "products" {
				product, err := e34Row(warehouse, "productID", "same", "result", "items")
				if err != nil {
					return err
				}
				if err := e34Expect("/catalog/tenantIndependent", product["name"], []string{"Tenant Asame", "Tenant Bsame"}[index]); err != nil {
					return err
				}
			} else {
				dish, err := e34Row(warehouse, "dishID", "same-dish", "result", "items")
				if err != nil {
					return err
				}
				if err := e34Expect("/catalog/tenantIndependent", dish, definitions[index]); err != nil {
					return err
				}
			}
		}
		for _, op := range []string{"dishDetails", "servingDish"} {
			reply, err := e34HTTP(s.ctx, s.input.Session, op, map[string]string{"restaurantID": s.input.Scopes[index].RestaurantID, "dishID": "same-dish"}, nil, nil, nil, 200)
			if err != nil {
				return err
			}
			if err := e34Expect("/catalog/tenantIndependent", e34Field(reply, "result", "dish"), definitions[index]); err != nil {
				return err
			}
		}
		stock, err := s.read(index, "inventory", nil)
		if err != nil {
			return err
		}
		row, err := e34Row(stock, "productID", "same", "result", "items")
		if err != nil {
			return err
		}
		other, err := e34Row(stock, "productID", "other", "result", "items")
		if err != nil {
			return err
		}
		rows, err := e34Rows(stock, "result", "items")
		if err != nil || len(rows) != 2 {
			return errors.New("stock must retain exactly two separate unit rows")
		}
		if err := errors.Join(e34Expect("/quantity/crossUnitTotalsAbsent", other["onHandQuantity"], "2000"), e34Expect("/quantity/crossUnitTotalsAbsent", other["baseUnit"], []string{"ml", "g"}[index])); err != nil {
			return err
		}
		if err := errors.Join(e34Expect("/quantity/largeExactString", row["onHandQuantity"], []string{"9007199254740993", "3000"}[index]), e34Expect("/catalog/tenantIndependent", row["baseUnit"], []string{"g", "ml"}[index]), e34Absent("/quantity/crossUnitTotalsAbsent", e34Field(stock, "result"), "totalQuantity")); err != nil {
			return err
		}
	}
	before, err := s.events(1)
	if err != nil {
		return err
	}
	beforeStock, err := s.read(1, "inventory", nil)
	if err != nil {
		return err
	}
	command, err := e34ID()
	if err != nil {
		return err
	}
	refused, err := s.mutate(0, "registerProduct", map[string]any{"commandID": command, "productID": "same", "name": "Conflicting", "baseUnit": "ml"}, 409)
	if err != nil {
		return err
	}
	if err := e34Expect("/catalog/tenantIndependent", refused["code"], "ProductConflict"); err != nil {
		return err
	}
	after, err := s.events(1)
	if err != nil {
		return err
	}
	afterStock, err := s.read(1, "inventory", nil)
	if err != nil {
		return err
	}
	return errors.Join(e34Expect("/owner/otherTenantUnchanged", after, before), e34Expect("/owner/otherTenantUnchanged", e34Field(afterStock, "result"), e34Field(beforeStock, "result")), e34Expect("/owner/otherTenantUnchanged", e34Field(afterStock, "context", "stateVersion"), e34Field(beforeStock, "context", "stateVersion")))
}

func e34Recommendations(s *e34Scenario) error {
	now, err := s.clock()
	if err != nil {
		return err
	}
	for _, id := range []string{"a", "b", "unallocated", "later"} {
		if err := s.product(0, id, id, "g"); err != nil {
			return err
		}
	}
	if err := s.dish(0, "dish", "Two tied products", []any{map[string]any{"productID": "a", "quantity": "1000"}, map[string]any{"productID": "b", "quantity": "1000"}}); err != nil {
		return err
	}
	if err := s.dish(0, "later-dish", "Later allocation", []any{map[string]any{"productID": "later", "quantity": "1000"}}); err != nil {
		return err
	}
	laterReceipt, err := s.supply(0, "later", "5000", now.Add(20*time.Hour))
	if err != nil {
		return err
	}
	laterBatch, err := e34Batch(laterReceipt)
	if err != nil {
		return err
	}
	available := map[string]map[string]any{"later": laterBatch}
	for _, id := range []string{"a", "b"} {
		reply, err := s.supply(0, id, "5000", now.Add(12*time.Hour))
		if err != nil {
			return err
		}
		batch, err := e34Batch(reply)
		if err != nil {
			return err
		}
		available[id] = batch
		if _, err := s.supply(0, id, "5000", now); err != nil {
			return err
		}
	}
	if _, err := s.supply(0, "unallocated", "5000", now.Add(6*time.Hour)); err != nil {
		return err
	}
	before, err := s.read(0, "inventory", nil)
	if err != nil {
		return err
	}
	recommendations, err := s.read(0, "recommendations", nil)
	if err != nil {
		return err
	}
	recommendation, err := e34Row(recommendations, "dishID", "dish", "result", "items")
	if err != nil {
		return err
	}
	ranked, err := e34Rows(recommendations, "result", "items")
	if err != nil || len(ranked) != 2 {
		return errors.New("two independent recommendation rankings required")
	}
	if err := errors.Join(e34Expect("/fefo/rankingMatchesActualAllocations", ranked[0]["dishID"], "dish"), e34Expect("/fefo/rankingMatchesActualAllocations", ranked[1]["dishID"], "later-dish")); err != nil {
		return err
	}
	proposal, err := s.mutate(0, "prepareOrder", map[string]any{"positions": []any{map[string]any{"dishID": "dish", "portions": "1"}}}, 200)
	if err != nil {
		return err
	}
	after, err := s.read(0, "inventory", nil)
	if err != nil {
		return err
	}
	if err := errors.Join(e34Expect("/quantity/recommendationReserved", e34Field(after, "result"), e34Field(before, "result")), e34Expect("/quantity/recommendationReserved", e34Field(after, "context", "stateVersion"), e34Field(before, "context", "stateVersion"))); err != nil {
		return err
	}
	command, err := e34ID()
	if err != nil {
		return err
	}
	confirmed, err := s.mutate(0, "confirmOrder", map[string]any{"commandID": command, "proposalID": e34Field(proposal, "result", "proposalID")}, 200)
	if err != nil {
		return err
	}
	if err := e34Expect("/fefo/rankingMatchesActualAllocations", e34Field(confirmed, "result", "kind"), "OrderConfirmed"); err != nil {
		return err
	}
	allocations, err := e34Rows(confirmed, "result", "order", "allocations")
	if err != nil || len(allocations) != 2 {
		return errors.New("actual two-product allocation required")
	}
	if err := e34RecommendationAllocations(recommendation, allocations, []string{"a", "b"}, available); err != nil {
		return err
	}
	for _, op := range []string{"menu", "stopList"} {
		if _, err := s.read(0, op, nil); err != nil {
			return err
		}
	}
	laterProposal, err := s.mutate(0, "prepareOrder", map[string]any{"positions": []any{map[string]any{"dishID": "later-dish", "portions": "1"}}}, 200)
	if err != nil {
		return err
	}
	laterCommand, err := e34ID()
	if err != nil {
		return err
	}
	laterConfirmed, err := s.mutate(0, "confirmOrder", map[string]any{"commandID": laterCommand, "proposalID": e34Field(laterProposal, "result", "proposalID")}, 200)
	if err != nil {
		return err
	}
	if err := e34Expect("/fefo/rankingMatchesActualAllocations", e34Field(laterConfirmed, "result", "kind"), "OrderConfirmed"); err != nil {
		return err
	}
	laterAllocations, err := e34Rows(laterConfirmed, "result", "order", "allocations")
	if err != nil || len(laterAllocations) != 1 {
		return errors.New("later recommendation actual allocation missing")
	}
	return e34RecommendationAllocations(ranked[1], laterAllocations, []string{"later"}, available)
}

func e34RecommendationAllocations(recommendation map[string]any, allocations []map[string]any, products []string, available map[string]map[string]any) error {
	reasons, err := e34Rows(recommendation, "reasons")
	if err != nil || len(reasons) != len(products) || len(allocations) != len(products) {
		return errors.New("all tied reasons and actual allocations required")
	}
	for i, product := range products {
		batch := available[product]
		expiry, ok := batch["expiresAt"].(string)
		if !ok || expiry == "" {
			return errors.New("recorded allocation expiry missing")
		}
		reason := reasons[i]
		if err := errors.Join(e34Expect("/fefo/allTiedReasonsPresent", reason["productID"], product), e34Expect("/fefo/allTiedReasonsPresent", reason["batchID"], batch["batchID"]), e34Expect("/fefo/allTiedReasonsPresent", reason["quantityPerPortion"], "1000"), e34Expect("/fefo/allTiedReasonsPresent", reason["baseUnit"], "g"), e34Expect("/fefo/allTiedReasonsPresent", e34SameInstant(reason["expiresAt"], expiry), true), e34Expect("/fefo/rankingMatchesActualAllocations", e34SameInstant(recommendation["priorityExpiresAt"], expiry), true), e34Expect("/fefo/rankingMatchesActualAllocations", batch["productID"], product)); err != nil {
			return err
		}
		matches := 0
		for _, allocation := range allocations {
			if allocation["productID"] == product && allocation["batchID"] == batch["batchID"] && allocation["quantity"] == reason["quantityPerPortion"] {
				matches++
			}
		}
		if err := e34Expect("/fefo/pendingOrExpiredUsed", matches, 1); err != nil {
			return err
		}
	}
	return nil
}
