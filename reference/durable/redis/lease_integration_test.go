//go:build integration

package redis

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newLease(t *testing.T, e *env) *Lease {
	t.Helper()
	l, err := e.c.Lease("gw")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLeaseSingleOwner(t *testing.T) {
	e := newEnv(t)
	l := newLease(t, e)
	var won atomic.Int32
	var fences sync.Map
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			g, err := l.Acquire(t.Context(), "expiry", fmt.Sprintf("replica-%d", i), 30*time.Second)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			if g.Acquired {
				won.Add(1)
				fences.Store(i, g.Fence)
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d owners, want 1", won.Load())
	}
	if ttl := e.pttl(t, e.key("gw", "expiry:lease")); ttl <= 0 || ttl > 30*time.Second {
		t.Fatalf("lease ttl=%v", ttl)
	}
}

func TestLeaseFenceMonotonicAcrossExpiry(t *testing.T) {
	e := newEnv(t)
	l := newLease(t, e)
	ctx := t.Context()
	a, err := l.Acquire(ctx, "expiry", "a", 150*time.Millisecond)
	if err != nil || !a.Acquired || a.Fence < 1 {
		t.Fatalf("a: %+v, %v", a, err)
	}
	if again, err := l.Acquire(ctx, "expiry", "a", 150*time.Millisecond); err != nil || !again.Acquired || again.Fence != a.Fence {
		t.Fatalf("a again: %+v, %v", again, err)
	}
	if b, err := l.Acquire(ctx, "expiry", "b", time.Second); err != nil || b.Acquired {
		t.Fatalf("b while a holds: %+v, %v", b, err)
	}
	time.Sleep(300 * time.Millisecond)

	// Renewal after expiry fails even though nobody else took the lease yet.
	if ok, err := l.Renew(ctx, "expiry", "a", time.Second); err != nil || ok {
		t.Fatalf("renew after expiry: %v, %v", ok, err)
	}
	b, err := l.Acquire(ctx, "expiry", "b", 5*time.Second)
	if err != nil || !b.Acquired || b.Fence <= a.Fence {
		t.Fatalf("b: %+v (a fence %d), %v", b, a.Fence, err)
	}
	// The old owner can neither renew nor release the new owner's lease.
	if ok, err := l.Renew(ctx, "expiry", "a", time.Second); err != nil || ok {
		t.Fatalf("a renews b's lease: %v, %v", ok, err)
	}
	if err := l.Release(ctx, "expiry", "a"); err != nil {
		t.Fatalf("foreign release: %v", err)
	}
	if ok, err := l.Renew(ctx, "expiry", "b", 5*time.Second); err != nil || !ok {
		t.Fatalf("b lost the lease to a foreign release: %v, %v", ok, err)
	}
	if err := l.Release(ctx, "expiry", "b"); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(ctx, "expiry", "b"); err != nil { // repeat: no-op
		t.Fatalf("second release: %v", err)
	}
	c, err := l.Acquire(ctx, "expiry", "c", 5*time.Second)
	if err != nil || !c.Acquired || c.Fence <= b.Fence {
		t.Fatalf("c: %+v (b fence %d), %v", c, b.Fence, err)
	}
	if v, _ := e.get(t, e.key("gw", "expiry:fence")); v == "" {
		t.Fatal("no fence counter")
	}
	if ttl := e.pttl(t, e.key("gw", "expiry:fence")); ttl != -1 {
		t.Fatalf("the fence counter must have no TTL, got %v", ttl)
	}
}

func TestLeaseRenewExtends(t *testing.T) {
	e := newEnv(t)
	l := newLease(t, e)
	ctx := t.Context()
	if g, err := l.Acquire(ctx, "expiry", "a", 400*time.Millisecond); err != nil || !g.Acquired {
		t.Fatalf("%+v, %v", g, err)
	}
	for range 4 { // 4 x 200ms is longer than the original TTL
		time.Sleep(200 * time.Millisecond)
		if ok, err := l.Renew(ctx, "expiry", "a", 400*time.Millisecond); err != nil || !ok {
			t.Fatalf("renew: %v, %v", ok, err)
		}
	}
	if g, err := l.Acquire(ctx, "expiry", "b", time.Second); err != nil || g.Acquired {
		t.Fatalf("b got a renewed lease: %+v, %v", g, err)
	}
}

func TestLeaseNamesAreIndependent(t *testing.T) {
	e := newEnv(t)
	l := newLease(t, e)
	x, errX := l.Acquire(t.Context(), "expiry", "a", time.Minute)
	y, errY := l.Acquire(t.Context(), "other", "b", time.Minute)
	if errX != nil || errY != nil || !x.Acquired || !y.Acquired {
		t.Fatalf("%+v %+v", x, y)
	}
}

func TestLeaseRejectsBadArguments(t *testing.T) {
	e := newEnv(t)
	l := newLease(t, e)
	for _, c := range []struct {
		name, owner string
		ttl         time.Duration
	}{{"", "a", time.Second}, {"bad name", "a", time.Second}, {"x", "", time.Second}, {"x", "a", 0}, {"x", "a", time.Microsecond}} {
		if _, err := l.Acquire(t.Context(), c.name, c.owner, c.ttl); err == nil {
			t.Errorf("Acquire(%q, %q, %v) accepted", c.name, c.owner, c.ttl)
		}
	}
}
