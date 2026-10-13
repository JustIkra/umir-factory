package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

type e34Scenario struct {
	ctx   context.Context
	input backendScenarioInput
}

func e34Field(data any, names ...string) any {
	for _, name := range names {
		object, ok := data.(map[string]any)
		if !ok {
			return nil
		}
		data, ok = object[name]
		if !ok || data == nil {
			return nil
		}
	}
	return data
}

func e34Expect(pointer string, actual, expected any) error {
	if actual == nil || expected == nil || !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("frozen assertion %s failed or source fact missing", pointer)
	}
	return nil
}

func e34Rows(data any, names ...string) ([]map[string]any, error) {
	values, ok := e34Field(data, names...).([]any)
	if !ok {
		return nil, errors.New("required owner collection missing")
	}
	rows := make([]map[string]any, 0, len(values))
	for _, value := range values {
		row, ok := value.(map[string]any)
		if !ok || len(row) == 0 {
			return nil, errors.New("invalid owner row")
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func e34Row(data any, key, value string, names ...string) (map[string]any, error) {
	rows, err := e34Rows(data, names...)
	if err != nil {
		return nil, err
	}
	var found map[string]any
	for _, row := range rows {
		if row[key] == value {
			if found != nil {
				return nil, errors.New("duplicate owner row")
			}
			found = row
		}
	}
	if found == nil {
		return nil, errors.New("required owner row missing")
	}
	return found, nil
}

func e34ID() (string, error) { return faultproxy.NewUUID() }

func e34Fork(b *backendSession) *backendSession {
	fork := newBackendSession(b.base, b.origin)
	if fork == nil {
		return nil
	}
	fork.probe = b.probe
	fork.client.Transport = b.client.Transport
	fork.client.Jar = b.client.Jar
	fork.csrf = b.csrfValue()
	return fork
}

func e34HTTP(ctx context.Context, b *backendSession, op string, params map[string]string, query url.Values, payload any, headers map[string]string, statuses ...int) (map[string]any, error) {
	fork := e34Fork(b)
	if fork == nil || len(statuses) == 0 {
		return nil, errors.New("real HTTP session required")
	}
	defer func() {
		b.mu.Lock()
		b.observations = append(b.observations, fork.observations...)
		b.mu.Unlock()
	}()
	var payloadRaw []byte
	if payload != nil {
		var err error
		payloadRaw, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	data, err := fork.callBytes(ctx, op, params, query, payloadRaw, headers, true, statuses[0])
	requestRaw, marshalErr := json.Marshal(map[string]any{"params": params, "query": query, "body": json.RawMessage(payloadRaw), "bodyPresent": payload != nil, "runtimeEpoch": headers["X-Umir-Runtime-Epoch"]})
	if marshalErr != nil {
		return nil, marshalErr
	}
	reply := fork.publicReply()
	if err == nil {
		e34Retain(ctx, op, requestRaw, reply.Body, reply.Status)
		return data, nil
	}
	if !slices.Contains(statuses, reply.Status) || len(reply.Body) == 0 {
		return nil, err
	}
	operation, ok := fork.operations[op]
	if !ok {
		return nil, err
	}
	pointer := schemaPointer(operation.Path, operation.Method, false, reply.Status)
	if _, exists := operation.Responses[fmt.Sprint(reply.Status)]; !exists {
		pointer = strings.ReplaceAll(pointer, "/responses/"+strconv.Itoa(reply.Status)+"/", "/responses/default/")
	}
	if faultproxy.ValidateSchema(fork.wire, pointer, reply.Body, 32) != nil {
		return nil, errors.New("typed owner refusal missing")
	}
	parsed, parseErr := faultproxy.StrictJSON(reply.Body, 32)
	object, ok := parsed.(map[string]any)
	if parseErr != nil || !ok {
		return nil, errors.New("typed owner refusal invalid")
	}
	e34Retain(ctx, op, requestRaw, reply.Body, reply.Status)
	return object, nil
}

func (s *e34Scenario) call(index int, op string, query url.Values, payload any, headers map[string]string, statuses ...int) (map[string]any, error) {
	return e34HTTP(s.ctx, s.input.Session, op, map[string]string{"restaurantID": s.input.Scopes[index].RestaurantID}, query, payload, headers, statuses...)
}

func (s *e34Scenario) read(index int, op string, query url.Values) (map[string]any, error) {
	data, err := s.call(index, op, query, nil, nil, 200)
	if err != nil {
		return nil, err
	}
	scope := s.input.Scopes[index]
	if e34Field(data, "context", "tenantID") != scope.RestaurantID || e34Field(data, "context", "orgID") != scope.OrgID {
		return nil, errors.New("owner response scope differs")
	}
	epoch, ok := e34Field(data, "context", "runtimeEpoch").(string)
	if !ok || epoch == "" {
		return nil, errors.New("owner runtime epoch missing")
	}
	return data, nil
}

func (s *e34Scenario) mutate(index int, op string, payload any, statuses ...int) (map[string]any, error) {
	readOp := "products"
	if op == "prepareOrder" || op == "confirmOrder" {
		readOp = "menu"
	}
	current, err := s.read(index, readOp, nil)
	if err != nil {
		return nil, err
	}
	epoch, ok := e34Field(current, "context", "runtimeEpoch").(string)
	if !ok || epoch == "" {
		return nil, errors.New("captured epoch missing")
	}
	return s.call(index, op, nil, payload, map[string]string{"X-Umir-Runtime-Epoch": epoch}, statuses...)
}

func (s *e34Scenario) product(index int, id, name, unit string) error {
	command, err := e34ID()
	if err != nil {
		return err
	}
	reply, err := s.mutate(index, "registerProduct", map[string]any{"commandID": command, "productID": id, "name": name, "baseUnit": unit}, 200)
	if err != nil {
		return err
	}
	return e34Expect("/catalog/definition", e34Field(reply, "result"), map[string]any{"productID": id, "name": name, "baseUnit": unit})
}

func (s *e34Scenario) dish(index int, id, name string, ingredients []any) error {
	command, err := e34ID()
	if err != nil {
		return err
	}
	reply, err := s.mutate(index, "defineDish", map[string]any{"commandID": command, "dishID": id, "name": name, "ingredients": ingredients}, 200)
	if err != nil {
		return err
	}
	return e34Expect("/catalog/dishID", e34Field(reply, "result", "dishID"), id)
}

func (s *e34Scenario) org(index int) (map[string]any, error) {
	return e34HTTP(s.ctx, s.input.Session, "organization", map[string]string{"orgID": s.input.Scopes[index].OrgID}, nil, nil, nil, 200)
}

func (s *e34Scenario) zone(index int, zone string) error {
	org, err := s.org(index)
	if err != nil {
		return err
	}
	command, err := e34ID()
	if err != nil {
		return err
	}
	updated, err := e34HTTP(s.ctx, s.input.Session, "updateSettings", map[string]string{"orgID": s.input.Scopes[index].OrgID}, nil, map[string]any{"commandID": command, "expectedSettingsVersion": e34Field(org, "settings", "settingsVersion"), "timeZone": zone}, nil, 200)
	if err != nil {
		return err
	}
	return e34Expect("/settings/appliedZone", e34Field(updated, "settings", "timeZone"), zone)
}

func (s *e34Scenario) clock() (time.Time, error) {
	clock, err := s.input.Client.clock(s.ctx, "warehouse")
	if err != nil {
		return time.Time{}, err
	}
	raw, err := json.Marshal(clock)
	if err != nil {
		return time.Time{}, err
	}
	e34Retain(s.ctx, "clock.read", nil, raw, 0)
	instant, err := time.Parse(time.RFC3339Nano, clock.Instant)
	if err != nil {
		return time.Time{}, errors.New("owner clock instant invalid")
	}
	return instant, nil
}

func e34Instant(t time.Time) map[string]any {
	return map[string]any{"instant": t.Format(time.RFC3339Nano)}
}

func (s *e34Scenario) delivery(index int, lines []any, created any, statuses ...int) (map[string]any, error) {
	org, err := s.org(index)
	if err != nil {
		return nil, err
	}
	command, err := e34ID()
	if err != nil {
		return nil, err
	}
	delivery, err := e34ID()
	if err != nil {
		return nil, err
	}
	return s.mutate(index, "receiveDelivery", map[string]any{"commandID": command, "deliveryID": delivery, "number": "e34-" + delivery, "createdAt": created, "expectedSettingsVersion": e34Field(org, "settings", "settingsVersion"), "lines": lines}, statuses...)
}

func e34Line(product, quantity string, made, received any, shelf string, months bool) map[string]any {
	unit := "day"
	if months {
		unit = "month"
	}
	return map[string]any{"productID": product, "quantity": quantity, "manufacturedAt": made, "receivedAt": received, "shelfLife": map[string]any{"value": shelf, "unit": unit}}
}

func (s *e34Scenario) supply(index int, product, quantity string, expiry time.Time) (map[string]any, error) {
	now, err := s.clock()
	if err != nil {
		return nil, err
	}
	days := int64(1)
	if remaining := expiry.Sub(now); remaining > 24*time.Hour {
		days = int64((remaining + 24*time.Hour - 1) / (24 * time.Hour))
	}
	return s.delivery(index, []any{e34Line(product, quantity, e34Instant(expiry.Add(-time.Duration(days)*24*time.Hour)), e34Instant(now), strconv.FormatInt(days, 10), false)}, e34Instant(now), 200)
}

func e34Batch(reply map[string]any) (map[string]any, error) {
	rows, err := e34Rows(reply, "result", "batches")
	if err != nil || len(rows) != 1 {
		return nil, errors.New("one authoritative batch required")
	}
	return rows[0], nil
}

func (s *e34Scenario) report(index int, op string, query url.Values) (map[string]any, error) {
	org, err := s.org(index)
	if err != nil {
		return nil, err
	}
	if query == nil {
		query = url.Values{}
	} else {
		copyQuery := url.Values{}
		for key, values := range query {
			copyQuery[key] = slices.Clone(values)
		}
		query = copyQuery
	}
	version, ok := e34Field(org, "settings", "settingsVersion").(string)
	if !ok || version == "" {
		return nil, errors.New("captured settings version missing")
	}
	query.Set("expectedSettingsVersion", version)
	return s.read(index, op, query)
}

func e34Events(history map[string]any, scope backendScope, epoch string) ([]map[string]any, error) {
	rows, err := e34Rows(history, "result", "events")
	if err != nil || len(rows) >= 500 {
		return nil, errors.New("complete bounded journal observation required")
	}
	events := make([]map[string]any, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		event, ok := row["event"].(map[string]any)
		if !ok || row["tenantID"] != scope.RestaurantID || row["runtimeEpoch"] != epoch {
			return nil, errors.New("journal scope/epoch mismatch")
		}
		if tenant, exists := event["tenantID"]; exists && tenant != scope.RestaurantID {
			return nil, errors.New("nested journal tenant mismatch")
		}
		if runtime, exists := event["runtimeEpoch"]; exists && runtime != epoch {
			return nil, errors.New("nested journal epoch mismatch")
		}
		id, ok := event["eventID"].(string)
		if !ok || id == "" || seen[id] {
			return nil, errors.New("journal identity missing or duplicated")
		}
		seen[id] = true
		events = append(events, event)
	}
	return events, nil
}

func (s *e34Scenario) events(index int) ([]map[string]any, error) {
	history, err := s.read(index, "history", nil)
	if err != nil {
		return nil, err
	}
	epoch, ok := e34Field(history, "context", "runtimeEpoch").(string)
	if !ok || epoch == "" {
		return nil, errors.New("journal epoch missing")
	}
	return e34Events(history, s.input.Scopes[index], epoch)
}

func e34Count(events []map[string]any, eventType string) int {
	count := 0
	for _, event := range events {
		if event["type"] == eventType {
			count++
		}
	}
	return count
}

func e34Duration(data map[string]any, from, to string) (time.Duration, error) {
	a, ok := data[from].(string)
	b, other := data[to].(string)
	if !ok || !other {
		return 0, errors.New("resolved owner interval missing")
	}
	start, err := time.Parse(time.RFC3339Nano, a)
	end, endErr := time.Parse(time.RFC3339Nano, b)
	if err != nil || endErr != nil || !end.After(start) {
		return 0, errors.New("resolved owner interval invalid")
	}
	return end.Sub(start), nil
}

func e34SameInstant(actual any, expected string) bool {
	value, ok := actual.(string)
	if !ok {
		return false
	}
	observed, err := time.Parse(time.RFC3339Nano, value)
	wanted, expectedErr := time.Parse(time.RFC3339Nano, expected)
	return err == nil && expectedErr == nil && observed.Equal(wanted)
}

func e34Absent(pointer string, data any, key string) error {
	parent, ok := data.(map[string]any)
	if !ok || len(parent) == 0 {
		return fmt.Errorf("frozen assertion %s has no captured parent", pointer)
	}
	if _, exists := parent[key]; exists {
		return fmt.Errorf("frozen assertion %s is present", pointer)
	}
	return nil
}

func e34Number(value any, expected int) bool {
	actual, ok := value.(json.Number)
	return ok && actual.String() == fmt.Sprint(expected)
}
