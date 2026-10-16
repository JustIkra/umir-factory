//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/JustIkra/umir-factory/reference/durable/postgres"
)

func TestInboxApplyDoesNotApplyTwice(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	in := postgres.NewInbox(rt)

	calls := 0
	apply := func(consumer, id string) (bool, error) {
		return in.Apply(t.Context(), consumer, id, func(tx pgx.Tx) error {
			calls++
			_, err := tx.Exec(t.Context(), "INSERT INTO biz (id) VALUES (nextval('outbox_claim_fence_seq'))")
			return err
		})
	}
	if applied, err := apply("serving.projection", "evt-1"); err != nil || !applied {
		t.Fatalf("first Apply = %v, %v", applied, err)
	}
	if applied, err := apply("serving.projection", "evt-1"); err != nil || applied {
		t.Fatalf("repeated Apply = %v, %v; want applied=false", applied, err)
	}
	if calls != 1 {
		t.Fatalf("fn ran %d times, want 1", calls)
	}
	// The key is (consumer, message): another consumer applies the same message.
	if applied, err := apply("gateway.live", "evt-1"); err != nil || !applied {
		t.Fatalf("other consumer Apply = %v, %v", applied, err)
	}
	if applied, err := apply("serving.projection", "evt-2"); err != nil || !applied {
		t.Fatalf("other message Apply = %v, %v", applied, err)
	}
	if n := count(t, t.Context(), rt, "SELECT count(*) FROM inbox"); n != 3 {
		t.Fatalf("inbox rows = %d, want 3", n)
	}
	if n := count(t, t.Context(), rt, "SELECT count(*) FROM biz"); n != 3 {
		t.Fatalf("effects = %d, want 3", n)
	}
}

func TestInboxApplyRollbackLeavesNoRecord(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	in := postgres.NewInbox(rt)
	boom := errors.New("handler failed")

	applied, err := in.Apply(t.Context(), "c", "evt-1", func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), "INSERT INTO biz (id, note) VALUES (1, 'effect')"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || applied {
		t.Fatalf("Apply = %v, %v; want applied=false and the handler error", applied, err)
	}
	if n := count(t, t.Context(), rt, "SELECT (SELECT count(*) FROM inbox) + (SELECT count(*) FROM biz)"); n != 0 {
		t.Fatalf("%d rows survived the rollback; the inbox record must roll back with the effect", n)
	}
	// Redelivery after the failure applies normally.
	applied, err = in.Apply(t.Context(), "c", "evt-1", func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), "INSERT INTO biz (id, note) VALUES (1, 'effect')")
		return err
	})
	if err != nil || !applied {
		t.Fatalf("redelivery Apply = %v, %v", applied, err)
	}
}

func TestInboxApplyConcurrentDuplicatesRunOnce(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, withMaxConns(10))
	in := postgres.NewInbox(rt)

	var runs, appliedCount atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			applied, err := in.Apply(t.Context(), "c", "evt-1", func(pgx.Tx) error {
				runs.Add(1)
				time.Sleep(50 * time.Millisecond) // keep the first transaction open while the others arrive
				return nil
			})
			if err != nil {
				t.Errorf("Apply: %v", err)
			}
			if applied {
				appliedCount.Add(1)
			}
		})
	}
	wg.Wait()
	if runs.Load() != 1 || appliedCount.Load() != 1 {
		t.Fatalf("fn ran %d times, applied=%d; want exactly once", runs.Load(), appliedCount.Load())
	}
}

func TestInboxApplyValidatesKeys(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	in := postgres.NewInbox(rt)
	noop := func(pgx.Tx) error { return nil }
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'x'
	}
	for _, c := range [][2]string{{"", "e"}, {"c", ""}, {"c", string(long)}, {string(long), "e"}} {
		if _, err := in.Apply(t.Context(), c[0], c[1], noop); err == nil {
			t.Errorf("Apply(%q, %d bytes) must be rejected", c[0], len(c[1]))
		}
	}
	if _, err := in.Apply(t.Context(), "c", "e", nil); err == nil {
		t.Error("a nil fn must be rejected")
	}
}

func TestInboxApplyInsideTheScopeOfTheMessage(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	in := postgres.NewInbox(rt)
	applied, err := in.Apply(t.Context(), "serving.projection", "evt-9", func(tx pgx.Tx) error {
		ctx := t.Context()
		if err := postgres.SetScope(ctx, tx, postgres.Scope{Actor: postgres.ActorTenant, OrgID: "o1", RestaurantID: "r1"}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO dishes (org_id, restaurant_id, name) VALUES ('o1', 'r1', 'from inbox')")
		return err
	})
	if err != nil || !applied {
		t.Fatalf("Apply = %v, %v", applied, err)
	}
	var visible []string
	if err := rt.InScopedTx(t.Context(), postgres.Scope{Actor: postgres.ActorTenant, OrgID: "o1", RestaurantID: "r1"}, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		visible, err = column(ctx, tx, "SELECT name FROM dishes")
		return err
	}); err != nil || len(visible) != 1 {
		t.Fatalf("visible = %v, %v", visible, err)
	}
}

func TestInboxDeleteOlderThan(t *testing.T) {
	f := newFixture(t)
	clock := newFakeClock()
	rt := f.runtime(t, withClock(clock))
	in := postgres.NewInbox(rt)
	noop := func(pgx.Tx) error { return nil }

	for _, id := range []string{"old-1", "old-2", "old-3"} {
		if _, err := in.Apply(t.Context(), "c", id, noop); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(10 * 24 * time.Hour)
	if _, err := in.Apply(t.Context(), "c", "fresh", noop); err != nil {
		t.Fatal(err)
	}
	deleted, err := in.DeleteOlderThan(t.Context(), 7*24*time.Hour, 2)
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteOlderThan batch 2 = %d, %v", deleted, err)
	}
	deleted, err = in.DeleteOlderThan(t.Context(), 7*24*time.Hour, 2)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteOlderThan second pass = %d, %v", deleted, err)
	}
	if n := count(t, t.Context(), rt, "SELECT count(*) FROM inbox"); n != 1 {
		t.Fatalf("inbox rows = %d, want only the fresh one", n)
	}
	if _, err := in.DeleteOlderThan(t.Context(), 0, 1); err == nil {
		t.Fatal("a zero age must be rejected")
	}
}
