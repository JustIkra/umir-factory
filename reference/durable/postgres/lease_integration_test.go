//go:build integration

package postgres_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustIkra/umir-factory/reference/durable/postgres"
)

const leaseTTL = 15 * time.Second

func newLease(t *testing.T, db *postgres.DB, name, holder string) *postgres.Lease {
	t.Helper()
	l, err := postgres.NewLease(db, name, holder, leaseTTL)
	if err != nil {
		t.Fatalf("NewLease: %v", err)
	}
	return l
}

func TestLeaseHasASingleOwner(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, withMaxConns(10))

	var owners atomic.Int32
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			l := newLease(t, rt, "expiry-scheduler", "holder-"+string(rune('a'+i)))
			fence, ok, err := l.Acquire(t.Context())
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			if ok {
				owners.Add(1)
				if fence <= 0 {
					t.Errorf("fence = %d", fence)
				}
			}
		})
	}
	wg.Wait()
	if owners.Load() != 1 {
		t.Fatalf("%d holders acquired the same lease", owners.Load())
	}
}

func TestLeaseFenceGrowsAfterExpiryAndOldOwnerIsRejected(t *testing.T) {
	f := newFixture(t)
	clock := newFakeClock()
	rt := f.runtime(t, withClock(clock))
	a := newLease(t, rt, "job", "a")
	b := newLease(t, rt, "job", "b")

	fenceA, ok, err := a.Acquire(t.Context())
	if err != nil || !ok {
		t.Fatalf("a.Acquire = %d %v %v", fenceA, ok, err)
	}
	if _, ok, err := b.Acquire(t.Context()); err != nil || ok {
		t.Fatalf("b acquired a live lease: %v %v", ok, err)
	}
	// The owner may renew and re-acquire without a new fence.
	clock.Advance(leaseTTL / 2)
	if err := a.Renew(t.Context()); err != nil {
		t.Fatalf("a.Renew: %v", err)
	}
	if again, ok, err := a.Acquire(t.Context()); err != nil || !ok || again != fenceA {
		t.Fatalf("a repeated Acquire = %d %v %v, want the same fence %d", again, ok, err, fenceA)
	}
	// Renewal moved the expiry, so b is still locked out at the original ttl.
	clock.Advance(leaseTTL/2 + time.Second)
	if _, ok, err := b.Acquire(t.Context()); err != nil || ok {
		t.Fatalf("b acquired a lease that a had renewed: %v %v", ok, err)
	}

	clock.Advance(leaseTTL)
	fenceB, ok, err := b.Acquire(t.Context())
	if err != nil || !ok || fenceB <= fenceA {
		t.Fatalf("b.Acquire after expiry = %d %v %v, want a fence above %d", fenceB, ok, err, fenceA)
	}
	if err := a.Renew(t.Context()); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("a.Renew after losing the lease = %v, want ErrLeaseLost", err)
	}
	if err := b.Renew(t.Context()); err != nil {
		t.Fatalf("b.Renew: %v", err)
	}
}

func TestLeaseRenewAfterExpiryFailsEvenIfNobodyTookIt(t *testing.T) {
	f := newFixture(t)
	clock := newFakeClock()
	rt := f.runtime(t, withClock(clock))
	a := newLease(t, rt, "job", "a")
	if _, ok, err := a.Acquire(t.Context()); err != nil || !ok {
		t.Fatal(err)
	}
	clock.Advance(leaseTTL + time.Second)
	if err := a.Renew(t.Context()); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("Renew after expiry = %v, want ErrLeaseLost", err)
	}
	// Taking it again is a new term with a larger fence.
	fence, ok, err := a.Acquire(t.Context())
	if err != nil || !ok || fence != 2 {
		t.Fatalf("re-Acquire after expiry = %d %v %v, want fence 2", fence, ok, err)
	}
}

func TestLeaseReleaseOfSomeoneElsesLeaseIsHarmless(t *testing.T) {
	f := newFixture(t)
	clock := newFakeClock()
	rt := f.runtime(t, withClock(clock))
	a := newLease(t, rt, "job", "a")
	b := newLease(t, rt, "job", "b")

	fenceA, ok, err := a.Acquire(t.Context())
	if err != nil || !ok {
		t.Fatal(err)
	}
	// b never held it.
	if err := b.Release(t.Context()); err != nil {
		t.Fatalf("Release by a stranger: %v", err)
	}
	if err := a.Renew(t.Context()); err != nil {
		t.Fatalf("the owner lost the lease to a stranger's Release: %v", err)
	}
	// a's term ends, b takes over, then a releases its stale lease.
	clock.Advance(leaseTTL + time.Second)
	fenceB, ok, err := b.Acquire(t.Context())
	if err != nil || !ok || fenceB <= fenceA {
		t.Fatalf("b.Acquire = %d %v %v", fenceB, ok, err)
	}
	if err := a.Release(t.Context()); err != nil {
		t.Fatalf("stale Release: %v", err)
	}
	if err := b.Renew(t.Context()); err != nil {
		t.Fatalf("b lost its lease to a stale Release: %v", err)
	}
}

func TestLeaseReleaseLetsAnotherHolderTakeOverWithAHigherFence(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	a := newLease(t, rt, "job", "a")
	b := newLease(t, rt, "job", "b")
	fenceA, ok, err := a.Acquire(t.Context())
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := a.Release(t.Context()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := a.Release(t.Context()); err != nil {
		t.Fatalf("a second Release must be a no-op: %v", err)
	}
	fenceB, ok, err := b.Acquire(t.Context())
	if err != nil || !ok || fenceB <= fenceA {
		t.Fatalf("b.Acquire after Release = %d %v %v, want a fence above %d", fenceB, ok, err, fenceA)
	}
	if err := a.Renew(t.Context()); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("a.Renew after Release = %v, want ErrLeaseLost", err)
	}
}

func TestLeasesAreIndependentByName(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	x := newLease(t, rt, "x", "same")
	y := newLease(t, rt, "y", "same")
	for _, l := range []*postgres.Lease{x, y} {
		if _, ok, err := l.Acquire(t.Context()); err != nil || !ok {
			t.Fatalf("Acquire: %v %v", ok, err)
		}
	}
	if x.Fence() != 1 || y.Fence() != 1 {
		t.Fatalf("fences = %d, %d; each lease counts on its own", x.Fence(), y.Fence())
	}
}

func TestNewLeaseValidatesArguments(t *testing.T) {
	f := newFixture(t)
	rt := f.runtime(t, nil)
	for _, c := range []struct {
		name, holder string
		ttl          time.Duration
	}{{"", "h", time.Second}, {"n", "", time.Second}, {"n", "h", 0}, {"n", "h", -time.Second}} {
		if _, err := postgres.NewLease(rt, c.name, c.holder, c.ttl); err == nil {
			t.Errorf("NewLease(%q, %q, %v) must fail", c.name, c.holder, c.ttl)
		}
	}
}
