package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sync"
)

type e34FactsKey struct{}

type e34SourceFact struct {
	Operation string          `json:"operation"`
	Request   json.RawMessage `json:"request,omitempty"`
	Response  json.RawMessage `json:"response"`
	Status    int             `json:"status,omitempty"`
}

type e34Facts struct {
	mu    sync.Mutex
	rows  []e34SourceFact
	err   error
	bytes int
}

func e34Retain(ctx context.Context, operation string, request, response []byte, status int) {
	facts, ok := ctx.Value(e34FactsKey{}).(*e34Facts)
	if !ok {
		return
	}
	facts.mu.Lock()
	defer facts.mu.Unlock()
	if !slices.Contains([]string{"products", "registerProduct", "dishCatalog", "dishDetails", "defineDish", "receiveDelivery", "inventory", "history", "confirmWriteoff", "suggestID", "servingProducts", "servingDishes", "servingDish", "menu", "stopList", "recommendations", "prepareOrder", "confirmOrder", "analyticsstock", "analyticsmovements", "pendingWriteoffs", "organization", "updateSettings", "clock.read", "warehouseRuntime"}, operation) {
		facts.err = errors.New("group evidence operation outside nonsecret allowlist")
		return
	}
	if len(facts.rows) >= 512 || len(request) > 1<<20 || len(response) > 1<<20 || facts.bytes+len(request)+len(response) > 16<<20 {
		facts.err = errors.New("group source evidence limit exceeded")
		return
	}
	if !json.Valid(response) || len(request) > 0 && !json.Valid(request) {
		facts.err = errors.New("group source evidence unavailable")
		return
	}
	facts.rows = append(facts.rows, e34SourceFact{operation, slices.Clone(request), slices.Clone(response), status})
	facts.bytes += len(request) + len(response)
}

func (facts *e34Facts) save(client *backendClient, id string, assertionsSucceeded bool) error {
	facts.mu.Lock()
	defer facts.mu.Unlock()
	if facts.err != nil {
		return facts.err
	}
	if len(facts.rows) == 0 || !filepath.IsAbs(client.run.Fixtures.HarnessPrivateRoot) {
		return errors.New("private group source evidence unavailable")
	}
	unique, err := e34ID()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(struct {
		Case                string          `json:"case"`
		FoundationIdentity  string          `json:"foundationIdentity"`
		ContractIdentity    string          `json:"contractIdentity"`
		AssertionsSucceeded bool            `json:"assertionsSucceeded"`
		Facts               []e34SourceFact `json:"facts"`
	}{id, "sha256:ffd4c1eccc1ee317cbe1449ab56c6831c534f0e86612845a17318022953c8468", backendContract, assertionsSucceeded, facts.rows})
	if err != nil {
		return err
	}
	return writeBackendPrivateSnapshot(filepath.Join(client.run.Fixtures.HarnessPrivateRoot, "backend-e34-"+id+"-"+unique+"-facts.json"), raw)
}
