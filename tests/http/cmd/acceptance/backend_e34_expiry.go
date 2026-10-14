package main

import (
	"errors"
	"time"
)

func e34Deadline(s *e34Scenario) error {
	now, err := s.clock()
	if err != nil {
		return err
	}
	if err := s.product(0, "deadline", "Deadline", "g"); err != nil {
		return err
	}
	if err := s.dish(0, "deadline-dish", "Deadline dish", []any{map[string]any{"productID": "deadline", "quantity": "1000"}}); err != nil {
		return err
	}
	deadline := now.Add(time.Minute)
	receipt, err := s.supply(0, "deadline", "5000", deadline)
	if err != nil {
		return err
	}
	batch, err := e34Batch(receipt)
	if err != nil {
		return err
	}
	expires, ok := batch["expiresAt"].(string)
	if !ok {
		return errors.New("saved batch expiry missing")
	}
	saved, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !saved.Equal(deadline) {
		return errors.New("saved batch expiry differs from supplied physical shelf life")
	}
	beforeMenu, err := s.read(0, "menu", nil)
	if err != nil {
		return err
	}
	if _, err := e34Row(beforeMenu, "dishID", "deadline-dish", "result", "dishes"); err != nil {
		return err
	}
	if err := s.input.Client.advance(s.ctx, expires); err != nil {
		return err
	}
	for repeat := 0; repeat < 2; repeat++ {
		if _, err := s.input.Client.schedulerTick(s.ctx); err != nil {
			return err
		}
	}
	stock, err := s.read(0, "inventory", nil)
	if err != nil {
		return err
	}
	row, err := e34Row(stock, "productID", "deadline", "result", "items")
	if err != nil {
		return err
	}
	pending, err := s.read(0, "pendingWriteoffs", nil)
	if err != nil {
		return err
	}
	pendingRows, err := e34Rows(pending, "items")
	if err != nil || len(pendingRows) != 1 || e34Field(pendingRows[0], "batch", "batchID") != batch["batchID"] || pendingRows[0]["status"] != "pending_writeoff" {
		return errors.New("real pending batch missing or different")
	}
	events, err := s.events(0)
	if err != nil {
		return err
	}
	menu, err := s.read(0, "menu", nil)
	if err != nil {
		return err
	}
	items, err := e34Rows(menu, "result", "dishes")
	if err != nil || len(items) != 0 {
		return errors.New("expired batch still makes dish available")
	}
	proposal, err := s.mutate(0, "prepareOrder", map[string]any{"positions": []any{map[string]any{"dishID": "deadline-dish", "portions": "1"}}}, 200)
	if err != nil {
		return err
	}
	accepted, ok := e34Field(proposal, "result", "acceptedPositions").([]any)
	if !ok || len(accepted) != 0 {
		return errors.New("expired batch allocated to a new order")
	}
	return errors.Join(e34Expect("/quantity/available", row["availableQuantity"], "0"), e34Expect("/quantity/pending", row["pendingWriteoffQuantity"], "5000"), e34Expect("/quantity/onHand", row["onHandQuantity"], "5000"), e34Expect("/journal/pendingFacts", e34Count(events, "BatchExpiryPending"), 1), e34Expect("/journal/finalWriteoffFacts", e34Count(events, "BatchWriteoffConfirmed"), 0))
}

func e34HumanWriteoff(s *e34Scenario) error {
	if err := s.product(0, "writeoff", "Writeoff", "g"); err != nil {
		return err
	}
	now, err := s.clock()
	if err != nil {
		return err
	}
	receipt, err := s.supply(0, "writeoff", "5000", now)
	if err != nil {
		return err
	}
	batch, err := e34Batch(receipt)
	if err != nil {
		return err
	}
	availableReceipt, err := s.supply(0, "writeoff", "2000", now.Add(time.Hour))
	if err != nil {
		return err
	}
	available, err := e34Batch(availableReceipt)
	if err != nil {
		return err
	}
	id, err := e34ID()
	if err != nil {
		return err
	}
	human, err := s.input.Session.enrollUser(s.ctx, s.input.Client, s.input.Scopes[0], "e34-writeoff-"+id+"@umir.invalid", []string{"Warehouse"})
	if err != nil {
		return err
	}
	stock, err := s.read(0, "inventory", nil)
	if err != nil {
		return err
	}
	epoch, ok := e34Field(stock, "context", "runtimeEpoch").(string)
	if !ok || epoch == "" {
		return errors.New("writeoff epoch missing")
	}
	commands := make([]string, 2)
	for i := range commands {
		commands[i], err = e34ID()
		if err != nil {
			return err
		}
	}
	type result struct {
		data  map[string]any
		err   error
		index int
	}
	results := make(chan result, 2)
	sessions := []*backendSession{s.input.Session, human}
	for i, session := range sessions {
		go func() {
			data, err := e34HTTP(s.ctx, session, "confirmWriteoff", map[string]string{"restaurantID": s.input.Scopes[0].RestaurantID}, nil, map[string]any{"commandID": commands[i], "batchID": batch["batchID"]}, map[string]string{"X-Umir-Runtime-Epoch": epoch}, 200, 409)
			results <- result{data, err, i}
		}()
	}
	winner := -1
	var original map[string]any
	conflicts := 0
	allResults := make([]result, 0, len(sessions))
	for range sessions {
		allResults = append(allResults, <-results)
	}
	for _, result := range allResults {
		if result.err != nil {
			err = errors.Join(err, result.err)
			continue
		}
		if e34Field(result.data, "result", "batchID") == batch["batchID"] {
			if winner != -1 {
				return errors.New("multiple real writeoff winners")
			}
			winner, original = result.index, result.data
		} else if result.data["code"] == "AlreadyWrittenOff" && result.data["outcome"] == "not_committed" {
			conflicts++
		} else {
			return errors.New("competing writeoff typed fact missing")
		}
	}
	if err != nil || winner == -1 || conflicts != 1 {
		return errors.Join(err, errors.New("one real writeoff winner and conflict required"))
	}
	replay, err := e34HTTP(s.ctx, sessions[1-winner], "confirmWriteoff", map[string]string{"restaurantID": s.input.Scopes[0].RestaurantID}, nil, map[string]any{"commandID": commands[winner], "batchID": batch["batchID"]}, map[string]string{"X-Umir-Runtime-Epoch": epoch}, 200)
	if err != nil {
		return err
	}
	for _, test := range []struct {
		batch any
		code  string
	}{{batch["batchID"], "AlreadyWrittenOff"}, {available["batchID"], "NotPendingWriteoff"}} {
		command, err := e34ID()
		if err != nil {
			return err
		}
		refused, err := s.call(0, "confirmWriteoff", nil, map[string]any{"commandID": command, "batchID": test.batch}, map[string]string{"X-Umir-Runtime-Epoch": epoch}, 409)
		if err != nil {
			return err
		}
		pointer := "/writeoff/competingError"
		if test.code == "NotPendingWriteoff" {
			pointer = "/writeoff/availableBatchError"
		}
		if err := e34Expect(pointer, refused["code"], test.code); err != nil {
			return err
		}
	}
	events, err := s.events(0)
	if err != nil {
		return err
	}
	var fact map[string]any
	for _, event := range events {
		if event["type"] == "BatchWriteoffConfirmed" {
			fact = event
		}
	}
	return errors.Join(e34Expect("/journal/finalWriteoffFacts", e34Count(events, "BatchWriteoffConfirmed"), 1), e34Expect("/quantity/writtenOff", e34Field(original, "result", "writtenOffQuantity"), "5000"), e34Expect("/writeoff/originalActorUnchangedOnReplay", e34Field(replay, "result"), e34Field(original, "result")), e34Expect("/writeoff/originalActorUnchangedOnReplay", e34Field(fact, "payload", "actorUserID"), e34Field(original, "result", "actorUserID")))
}
