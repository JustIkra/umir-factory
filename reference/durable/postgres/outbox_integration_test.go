//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/JustIkra/umir-factory/reference/durable/postgres"
)

const testLease = 30 * time.Second

func newEvent(n int) postgres.Event {
	return postgres.Event{
		Exchange:     "umir.events",
		RoutingKey:   "warehouse.OrderPlaced",
		Headers:      map[string]string{"x-umir-schema": "1", "x-umir-restaurant-id": "r1"},
		Body:         []byte(fmt.Sprintf(`{"eventID":"e%d","n":%d}`, n, n)),
		OrgID:        "o1",
		RestaurantID: "r1",
	}
}

func addEvents(t *testing.T, db *postgres.DB, ob *postgres.Outbox, n int) []string {
	t.Helper()
	var ids []string
	err := db.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		ids = nil
		for i := range n {
			id, err := ob.AddEvent(ctx, tx, newEvent(i))
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("add events: %v", err)
	}
	return ids
}

func rowIDs(rows []postgres.Row) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

func TestOutboxIsAtomicWithTheBusinessRow(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	ob := postgres.NewOutbox(rt)

	boom := errors.New("business failure after AddEvent")
	err := rt.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO biz (id, note) VALUES (1, 'rolled back')"); err != nil {
			return err
		}
		if _, err := ob.AddEvent(ctx, tx, newEvent(1)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n := count(t, t.Context(), rt, "SELECT (SELECT count(*) FROM biz) + (SELECT count(*) FROM outbox)"); n != 0 {
		t.Fatalf("%d rows survived the rollback; the event must disappear with the business row", n)
	}

	var id string
	err = rt.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO biz (id, note) VALUES (2, 'committed')"); err != nil {
			return err
		}
		var err error
		id, err = ob.AddEvent(ctx, tx, newEvent(2))
		return err
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n := count(t, t.Context(), rt, "SELECT (SELECT count(*) FROM biz) + (SELECT count(*) FROM outbox WHERE id = $1::uuid)", id); n != 2 {
		t.Fatalf("committed rows = %d, want business row and event", n)
	}
}

func TestOutboxRowShape(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	columns := []string{"id", "position", "exchange", "routing_key", "headers", "body", "org_id", "restaurant_id",
		"created_at", "not_before", "published_at", "routed", "attempts", "claimed_by", "claim_fence", "claim_expires_at"}
	var got []string
	err := rt.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		got, err = column(ctx, tx, "SELECT column_name FROM information_schema.columns WHERE table_name = 'outbox' ORDER BY ordinal_position")
		return err
	})
	if err != nil || !slices.Equal(got, columns) {
		t.Fatalf("outbox columns = %v (%v), want %v", got, err, columns)
	}
}

func TestOutboxClaimReturnsTheEventWithItsFields(t *testing.T) {
	f := newFixture(t)
	clock := newFakeClock()
	rt := f.runtime(t, withClock(clock))
	ob := postgres.NewOutbox(rt)

	ev := newEvent(7)
	ev.OrgID, ev.RestaurantID = "", "" // a global event has no scope: NULL columns
	var id string
	if err := rt.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = ob.AddEvent(ctx, tx, ev)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows, fence, err := ob.Claim(t.Context(), "relay-a", 10, testLease)
	if err != nil || len(rows) != 1 || fence <= 0 {
		t.Fatalf("Claim = %v, fence %d, err %v", rows, fence, err)
	}
	r := rows[0]
	var body map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil || body["eventID"] != "e7" {
		t.Fatalf("body = %s (%v)", r.Body, err)
	}
	if r.ID != id || r.Position <= 0 || r.Exchange != "umir.events" || r.RoutingKey != "warehouse.OrderPlaced" ||
		r.Headers["x-umir-schema"] != "1" || r.OrgID != "" || r.RestaurantID != "" || r.Attempts != 1 {
		t.Fatalf("row = %+v", r)
	}
	if !r.CreatedAt.Equal(clock.Now()) {
		t.Fatalf("CreatedAt = %v, want the injected clock %v", r.CreatedAt, clock.Now())
	}
	if n := count(t, t.Context(), rt, "SELECT count(*) FROM outbox WHERE org_id IS NULL AND restaurant_id IS NULL"); n != 1 {
		t.Fatal("empty scope must be stored as NULL")
	}
}

func TestOutboxRejectsInvalidEvents(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	ob := postgres.NewOutbox(rt)
	bad := map[string]func(*postgres.Event){
		"no exchange":     func(e *postgres.Event) { e.Exchange = "" },
		"no routing key":  func(e *postgres.Event) { e.RoutingKey = "" },
		"body not JSON":   func(e *postgres.Event) { e.Body = []byte("{nope") },
		"empty body":      func(e *postgres.Event) { e.Body = nil },
		"id not a UUID":   func(e *postgres.Event) { e.ID = "not-a-uuid" },
		"empty header":    func(e *postgres.Event) { e.Headers = map[string]string{"": "v"} },
		"routing key NUL": func(e *postgres.Event) { e.RoutingKey = "a\x00b" },
	}
	for name, mutate := range bad {
		t.Run(name, func(t *testing.T) {
			ev := newEvent(1)
			mutate(&ev)
			err := rt.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				_, err := ob.AddEvent(ctx, tx, ev)
				return err
			})
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if n := count(t, t.Context(), rt, "SELECT count(*) FROM outbox"); n != 0 {
		t.Fatalf("%d invalid events were stored", n)
	}
}

func TestOutboxClaimOfTwoRelaysNeverOverlaps(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, withMaxConns(8))
	ob := postgres.NewOutbox(rt)
	all := addEvents(t, rt, ob, 60)

	var wg sync.WaitGroup
	claimed := make([][]string, 2)
	errs := make([]error, 2)
	for i, owner := range []string{"relay-a", "relay-b"} {
		wg.Go(func() {
			for {
				rows, _, err := ob.Claim(t.Context(), owner, 7, testLease)
				if err != nil {
					errs[i] = err
					return
				}
				if len(rows) == 0 {
					return
				}
				claimed[i] = append(claimed[i], rowIDs(rows)...)
			}
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
	}
	seen := map[string]string{}
	for i, ids := range claimed {
		for _, id := range ids {
			if prev, dup := seen[id]; dup {
				t.Fatalf("event %s was claimed twice (relay %d and %s)", id, i, prev)
			}
			seen[id] = fmt.Sprint(i)
		}
	}
	if len(seen) != len(all) {
		t.Fatalf("claimed %d of %d events", len(seen), len(all))
	}
	if len(claimed[0]) == 0 || len(claimed[1]) == 0 {
		t.Logf("one relay did all the work (%d/%d); disjointness still holds", len(claimed[0]), len(claimed[1]))
	}
}

func TestOutboxClaimTakesRowsInPositionOrder(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	ob := postgres.NewOutbox(rt)
	ids := addEvents(t, rt, ob, 5)
	rows, _, err := ob.Claim(t.Context(), "relay-a", 3, testLease)
	if err != nil || !slices.Equal(rowIDs(rows), ids[:3]) {
		t.Fatalf("Claim = %v (%v), want the first three in order %v", rowIDs(rows), err, ids[:3])
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].Position <= rows[i-1].Position {
			t.Fatalf("positions are not increasing: %v", rows)
		}
	}
	if _, _, err := ob.Claim(t.Context(), "relay-a", 0, testLease); err == nil {
		t.Fatal("limit 0 must be rejected")
	}
	if _, _, err := ob.Claim(t.Context(), "relay-a", 1, 0); err == nil {
		t.Fatal("a zero lease must be rejected")
	}
	if _, _, err := ob.Claim(t.Context(), "", 1, testLease); err == nil {
		t.Fatal("an empty owner must be rejected")
	}
}

func TestOutboxExpiredLeaseHandsTheBatchToAnotherRelay(t *testing.T) {
	f := newFixture(t)
	clock := newFakeClock()
	rt := f.runtime(t, withClock(clock))
	ob := postgres.NewOutbox(rt)
	ids := addEvents(t, rt, ob, 3)

	a, fenceA, err := ob.Claim(t.Context(), "relay-a", 10, testLease)
	if err != nil || len(a) != 3 {
		t.Fatalf("relay-a claim: %v %v", a, err)
	}
	// While the lease is alive nobody else gets the rows.
	if b, _, err := ob.Claim(t.Context(), "relay-b", 10, testLease); err != nil || len(b) != 0 {
		t.Fatalf("relay-b claimed %d rows under a live lease (%v)", len(b), err)
	}
	clock.Advance(testLease + time.Second)
	b, fenceB, err := ob.Claim(t.Context(), "relay-b", 10, testLease)
	if err != nil || !slices.Equal(rowIDs(b), ids) {
		t.Fatalf("relay-b claim after expiry: %v %v", rowIDs(b), err)
	}
	if fenceB <= fenceA {
		t.Fatalf("fence did not grow: %d then %d", fenceA, fenceB)
	}
	if b[0].Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", b[0].Attempts)
	}

	// The old owner finds out when it reports.
	if err := ob.MarkPublished(t.Context(), "relay-a", fenceA, ids, true); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("old owner MarkPublished = %v, want ErrLeaseLost", err)
	}
	if err := ob.Release(t.Context(), "relay-a", fenceA, ids, 0); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("old owner Release = %v, want ErrLeaseLost", err)
	}
	// ... and cannot use the new owner's fence under its own name.
	if err := ob.MarkPublished(t.Context(), "relay-a", fenceB, ids, true); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("wrong owner with the right fence = %v, want ErrLeaseLost", err)
	}
	if n := count(t, t.Context(), rt, "SELECT count(*) FROM outbox WHERE published_at IS NOT NULL"); n != 0 {
		t.Fatalf("%d rows were published by the lost lease", n)
	}
	if err := ob.MarkPublished(t.Context(), "relay-b", fenceB, ids, true); err != nil {
		t.Fatalf("new owner MarkPublished: %v", err)
	}
	// Repeating the report after an unknown commit is harmless.
	if err := ob.MarkPublished(t.Context(), "relay-b", fenceB, ids, true); err != nil {
		t.Fatalf("repeated MarkPublished: %v", err)
	}
	if rows, _, err := ob.Claim(t.Context(), "relay-c", 10, testLease); err != nil || len(rows) != 0 {
		t.Fatalf("published rows were claimed again: %v %v", rows, err)
	}
}

func TestOutboxOwnerWithAnExpiredButUntakenLeaseMayStillReport(t *testing.T) {
	f := newFixture(t)
	clock := newFakeClock()
	rt := f.runtime(t, withClock(clock))
	ob := postgres.NewOutbox(rt)
	ids := addEvents(t, rt, ob, 2)
	rows, fence, err := ob.Claim(t.Context(), "relay-a", 10, testLease)
	if err != nil || len(rows) != 2 {
		t.Fatalf("claim: %v %v", rows, err)
	}
	clock.Advance(time.Hour) // slow broker; nobody took the rows meanwhile
	if err := ob.MarkPublished(t.Context(), "relay-a", fence, ids, true); err != nil {
		t.Fatalf("the message was published; recording it must work while no one else claimed: %v", err)
	}
}

func TestOutboxMarkPublishedRecordsRoutedFlag(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	ob := postgres.NewOutbox(rt)
	ids := addEvents(t, rt, ob, 2)
	_, fence, err := ob.Claim(t.Context(), "relay-a", 10, testLease)
	if err != nil {
		t.Fatal(err)
	}
	if err := ob.MarkPublished(t.Context(), "relay-a", fence, ids[:1], true); err != nil {
		t.Fatal(err)
	}
	if err := ob.MarkPublished(t.Context(), "relay-a", fence, ids[1:], false); err != nil {
		t.Fatal(err)
	}
	if n := count(t, t.Context(), rt, "SELECT count(*) FROM outbox WHERE id = $1::uuid AND routed AND published_at IS NOT NULL", ids[0]); n != 1 {
		t.Fatal("routed=true was not recorded")
	}
	if n := count(t, t.Context(), rt, "SELECT count(*) FROM outbox WHERE id = $1::uuid AND routed IS FALSE AND published_at IS NOT NULL", ids[1]); n != 1 {
		t.Fatal("routed=false was not recorded")
	}
	if err := ob.MarkPublished(t.Context(), "relay-a", fence, nil, true); err != nil {
		t.Fatalf("an empty batch is a no-op: %v", err)
	}
}

func TestOutboxReleaseReturnsRowsWithADelay(t *testing.T) {
	f := newFixture(t)
	clock := newFakeClock()
	rt := f.runtime(t, withClock(clock))
	ob := postgres.NewOutbox(rt)
	ids := addEvents(t, rt, ob, 2)
	_, fence, err := ob.Claim(t.Context(), "relay-a", 10, testLease)
	if err != nil {
		t.Fatal(err)
	}
	if err := ob.Release(t.Context(), "relay-a", fence, ids, 10*time.Second); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if rows, _, err := ob.Claim(t.Context(), "relay-b", 10, testLease); err != nil || len(rows) != 0 {
		t.Fatalf("rows came back before the delay: %v %v", rows, err)
	}
	clock.Advance(11 * time.Second)
	rows, _, err := ob.Claim(t.Context(), "relay-b", 10, testLease)
	if err != nil || len(rows) != 2 || rows[0].Attempts != 2 {
		t.Fatalf("after the delay: %v %v", rows, err)
	}
}

func TestOutboxNotBeforeHoldsBackAnEvent(t *testing.T) {
	f := newFixture(t)
	clock := newFakeClock()
	rt := f.runtime(t, withClock(clock))
	ob := postgres.NewOutbox(rt)
	ev := newEvent(1)
	ev.NotBefore = clock.Now().Add(time.Minute)
	if err := rt.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := ob.AddEvent(ctx, tx, ev)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rows, _, err := ob.Claim(t.Context(), "relay-a", 10, testLease); err != nil || len(rows) != 0 {
		t.Fatalf("a delayed event was claimed early: %v %v", rows, err)
	}
	clock.Advance(time.Minute)
	if rows, _, err := ob.Claim(t.Context(), "relay-a", 10, testLease); err != nil || len(rows) != 1 {
		t.Fatalf("a due event was not claimed: %v %v", rows, err)
	}
}

func TestOutboxDeleteOlderThanRemovesOnlyOldPublishedRows(t *testing.T) {
	f := newFixture(t)
	clock := newFakeClock()
	rt := f.runtime(t, withClock(clock))
	ob := postgres.NewOutbox(rt)
	ids := addEvents(t, rt, ob, 4)

	rows, fence, err := ob.Claim(t.Context(), "relay-a", 2, testLease)
	if err != nil || len(rows) != 2 {
		t.Fatal(err)
	}
	if err := ob.MarkPublished(t.Context(), "relay-a", fence, rowIDs(rows), true); err != nil {
		t.Fatal(err)
	}
	clock.Advance(48 * time.Hour)
	rows2, fence2, err := ob.Claim(t.Context(), "relay-a", 1, testLease)
	if err != nil || len(rows2) != 1 {
		t.Fatal(err)
	}
	if err := ob.MarkPublished(t.Context(), "relay-a", fence2, rowIDs(rows2), true); err != nil {
		t.Fatal(err)
	}

	// Two rows were published 48h ago, one just now, one is unpublished.
	deleted, err := ob.DeleteOlderThan(t.Context(), 24*time.Hour, 1)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteOlderThan with batch 1 = %d, %v; want 1", deleted, err)
	}
	deleted, err = ob.DeleteOlderThan(t.Context(), 24*time.Hour, 100)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteOlderThan = %d, %v; want the remaining old row", deleted, err)
	}
	left := count(t, t.Context(), rt, "SELECT count(*) FROM outbox")
	if left != 2 {
		t.Fatalf("rows left = %d, want the recent published row and the unpublished one", left)
	}
	if n := count(t, t.Context(), rt, "SELECT count(*) FROM outbox WHERE id = $1::uuid", ids[3]); n != 1 {
		t.Fatal("the unpublished row was deleted")
	}
	if _, err := ob.DeleteOlderThan(t.Context(), 0, 10); err == nil {
		t.Fatal("a zero age must be rejected")
	}
	if _, err := ob.DeleteOlderThan(t.Context(), time.Hour, 0); err == nil {
		t.Fatal("a zero batch must be rejected")
	}
}

func TestOutboxStats(t *testing.T) {
	f := newFixture(t)
	clock := newFakeClock()
	rt := f.runtime(t, withClock(clock))
	ob := postgres.NewOutbox(rt)

	stats, err := ob.Stats(t.Context())
	if err != nil || stats.Pending != 0 || stats.OldestAge != 0 {
		t.Fatalf("empty outbox stats = %+v, %v", stats, err)
	}
	ids := addEvents(t, rt, ob, 3)
	clock.Advance(90 * time.Second)
	stats, err = ob.Stats(t.Context())
	if err != nil || stats.Pending != 3 || stats.OldestAge != 90*time.Second {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	rows, fence, err := ob.Claim(t.Context(), "relay-a", 3, testLease)
	if err != nil || len(rows) != 3 {
		t.Fatal(err)
	}
	if err := ob.MarkPublished(t.Context(), "relay-a", fence, ids, true); err != nil {
		t.Fatal(err)
	}
	stats, err = ob.Stats(t.Context())
	if err != nil || stats.Pending != 0 || stats.OldestAge != 0 {
		t.Fatalf("stats after publishing = %+v, %v", stats, err)
	}
}

func TestOutboxClaimSkipsRowsLockedByAnotherTransaction(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, withMaxConns(4))
	ob := postgres.NewOutbox(rt)
	ids := addEvents(t, rt, ob, 6)

	// Another relay is in the middle of claiming: its transaction holds FOR
	// UPDATE on the head of the queue.
	holder, err := rt.Pool().Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			ignoreErr(holder.Rollback(context.WithoutCancel(t.Context())))
		}
	}
	defer release()
	held, err := column(t.Context(), holder, "SELECT id::text FROM outbox ORDER BY position LIMIT 2 FOR UPDATE")
	if err != nil || !slices.Equal(held, ids[:2]) {
		t.Fatalf("held rows = %v (%v), want the first two %v", held, err, ids[:2])
	}

	// With plain FOR UPDATE this Claim would wait for the holder; with SKIP
	// LOCKED it returns at once with the rows behind the locked head.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start := time.Now()
	rows, fence, err := ob.Claim(ctx, "relay-b", 10, testLease)
	if err != nil {
		t.Fatalf("Claim blocked or failed behind a locked head: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Claim took %v: it waited for the locked rows", time.Since(start))
	}
	if !slices.Equal(rowIDs(rows), ids[2:]) || fence <= 0 {
		t.Fatalf("Claim = %v (fence %d), want the rows behind the locked head %v", rowIDs(rows), fence, ids[2:])
	}

	// Once the other transaction lets go, its rows are free again.
	release()
	rows, _, err = ob.Claim(t.Context(), "relay-b", 10, testLease)
	if err != nil || !slices.Equal(rowIDs(rows), ids[:2]) {
		t.Fatalf("Claim after the release = %v (%v), want the head %v", rowIDs(rows), err, ids[:2])
	}
}

func TestOutboxClaimConvertsNonStringHeadersInsteadOfBlockingTheQueue(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	ob := postgres.NewOutbox(rt)

	// A row written by other means than AddEvent: jsonb holds any JSON value.
	exec(t, rt, `INSERT INTO outbox (id, exchange, routing_key, headers, body) VALUES
		(gen_random_uuid(), 'umir.events', 'odd', '{"x-attempt": 3, "x-flag": true, "x-null": null, "x-obj": {"a": [1]}, "x-text": "ok"}', '{}'::jsonb)`)
	addEvents(t, rt, ob, 1)

	rows, _, err := ob.Claim(t.Context(), "relay-a", 10, testLease)
	if err != nil || len(rows) != 2 {
		t.Fatalf("Claim = %d rows, err %v: a row with unusual headers must not block the queue", len(rows), err)
	}
	want := map[string]string{"x-attempt": "3", "x-flag": "true", "x-obj": `{"a":[1]}`, "x-text": "ok"}
	if !maps.Equal(rows[0].Headers, want) {
		t.Fatalf("headers = %v, want %v", rows[0].Headers, want)
	}
	if rows[1].Headers["x-umir-schema"] != "1" {
		t.Fatalf("regular row headers = %v", rows[1].Headers)
	}
}

func TestOutboxHeadersMustBeAJSONObject(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	for _, headers := range []string{`'[]'`, `'"text"'`, `'7'`, `'null'`} {
		err := rt.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO outbox (id, exchange, routing_key, headers, body) VALUES (gen_random_uuid(), 'e', 'k', "+headers+"::jsonb, '{}'::jsonb)")
			return err
		})
		if err == nil || sqlState(t, err) != "23514" {
			t.Errorf("headers %s: err = %v, want a check violation (23514)", headers, err)
		}
	}
}
