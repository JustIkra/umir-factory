//go:build integration

package redis

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustIkra/umir-factory/reference/durable/itest"
)

func newSlots(t *testing.T, e *env, owner string) *Slots {
	t.Helper()
	s, err := e.c.Slots(owner)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func acquire(t *testing.T, s *Slots, holder string, ttl time.Duration, dims ...SlotDimension) SlotGrant {
	t.Helper()
	g, err := s.Acquire(t.Context(), SlotRequest{Holder: holder, TTL: ttl, Dimensions: dims})
	if err != nil {
		t.Fatalf("acquire %s: %v", holder, err)
	}
	return g
}

func members(t *testing.T, e *env, owner, key string) int {
	t.Helper()
	n, err := e.rdb.ZCard(t.Context(), e.key(owner, key)).Result()
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func TestSlotsNeverExceedCountUnder64ParallelCalls(t *testing.T) {
	e := newEnv(t)
	s := newSlots(t, e, "gw")
	dim := SlotDimension{Key: "sse:cell", MaxCount: 10}
	var granted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 64 {
		wg.Go(func() {
			<-start
			g, err := s.Acquire(t.Context(), SlotRequest{Holder: fmt.Sprintf("stream-%d", i), TTL: 30 * time.Second, Dimensions: []SlotDimension{dim}})
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			if g.Granted {
				granted.Add(1)
			} else if g.Denied != 0 {
				t.Errorf("denied by dimension %d", g.Denied)
			}
		})
	}
	close(start)
	wg.Wait()
	if granted.Load() != 10 {
		t.Fatalf("granted=%d, want 10", granted.Load())
	}
	if n := members(t, e, "gw", dim.Key); n != 10 {
		t.Fatalf("%d members, want 10", n)
	}
}

func TestSlotsNeverExceedUnitsUnder64ParallelCalls(t *testing.T) {
	e := newEnv(t)
	s := newSlots(t, e, "wh")
	const maxBytes = 100
	dims := []SlotDimension{
		{Key: "capslots:g1:r1", MaxCount: 128},
		{Key: "capbytes:g1:r1", MaxUnits: maxBytes, Units: 30},
	}
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			g, err := s.Acquire(t.Context(), SlotRequest{Holder: fmt.Sprintf("cap-%d", i), TTL: time.Minute, Dimensions: dims})
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			if g.Granted {
				granted.Add(1)
			} else if g.Denied != 1 {
				t.Errorf("denied by dimension %d, want 1", g.Denied)
			}
		})
	}
	wg.Wait()
	if granted.Load() != 3 {
		t.Fatalf("granted=%d, want 3 (3 x 30 <= 100 < 4 x 30)", granted.Load())
	}
	// A denial by the size dimension left no trace in the count dimension.
	if n := members(t, e, "wh", "capslots:g1:r1"); n != 3 {
		t.Fatalf("count dimension has %d members, want 3", n)
	}
	// A small one still fits into what is left.
	small := []SlotDimension{{Key: "capslots:g1:r1", MaxCount: 128}, {Key: "capbytes:g1:r1", MaxUnits: maxBytes, Units: 10}}
	if g := acquire(t, s, "small", time.Minute, small...); !g.Granted {
		t.Fatalf("10 units of the remaining 10 denied: %+v", g)
	}
	if g := acquire(t, s, "one-more", time.Minute, small...); g.Granted {
		t.Fatal("exceeded the size limit")
	}
}

func TestSlotsCountLimitByRows(t *testing.T) {
	e := newEnv(t)
	s := newSlots(t, e, "wh")
	dims := func(rows int64) []SlotDimension {
		return []SlotDimension{{Key: "capslots:g:r", MaxCount: 128}, {Key: "caprows:g:r", MaxUnits: 10000, Units: rows}}
	}
	if g := acquire(t, s, "a", time.Minute, dims(6000)...); !g.Granted {
		t.Fatal("a denied")
	}
	if g := acquire(t, s, "b", time.Minute, dims(5000)...); g.Granted || g.Denied != 1 {
		t.Fatalf("b: %+v", g)
	}
	if g := acquire(t, s, "c", time.Minute, dims(4000)...); !g.Granted {
		t.Fatal("c denied")
	}
}

func TestSlotsAtomicAcrossDimensions(t *testing.T) {
	e := newEnv(t)
	s := newSlots(t, e, "gw")
	sess := SlotDimension{Key: "sse:s:abc", MaxCount: 1}
	org := SlotDimension{Key: "sse:o:org1", MaxCount: 5}
	cell := SlotDimension{Key: "sse:cell", MaxCount: 5}
	if g := acquire(t, s, "one", 30*time.Second, sess, org, cell); !g.Granted {
		t.Fatalf("first: %+v", g)
	}
	g := acquire(t, s, "two", 30*time.Second, sess, org, cell)
	if g.Granted || g.Denied != 0 {
		t.Fatalf("second: %+v, want denied by dimension 0", g)
	}
	for _, k := range []string{"sse:o:org1", "sse:cell"} {
		if n := members(t, e, "gw", k); n != 1 {
			t.Fatalf("%s has %d members after a denied acquire, want 1", k, n)
		}
	}
}

func TestSlotsReleaseIsIdempotentAndFreesTheSlot(t *testing.T) {
	e := newEnv(t)
	s := newSlots(t, e, "gw")
	dim := SlotDimension{Key: "sse:s:x", MaxCount: 1}
	if g := acquire(t, s, "a", time.Minute, dim); !g.Granted {
		t.Fatal("a denied")
	}
	if g := acquire(t, s, "b", time.Minute, dim); g.Granted {
		t.Fatal("b admitted over the limit")
	}
	// Somebody else's release does not free a's slot.
	if err := s.Release(t.Context(), "b", dim.Key); err != nil {
		t.Fatal(err)
	}
	if g := acquire(t, s, "c", time.Minute, dim); g.Granted {
		t.Fatal("a foreign release freed the slot")
	}
	for range 3 {
		if err := s.Release(t.Context(), "a", dim.Key); err != nil {
			t.Fatalf("release: %v", err)
		}
	}
	if err := s.Release(t.Context(), "never-existed", dim.Key); err != nil {
		t.Fatal(err)
	}
	if g := acquire(t, s, "b", time.Minute, dim); !g.Granted {
		t.Fatal("slot not freed")
	}
	if g := acquire(t, s, "c", time.Minute, dim); g.Granted {
		t.Fatal("repeated releases created extra room")
	}
}

func TestSlotsReleaseFreesUnits(t *testing.T) {
	e := newEnv(t)
	s := newSlots(t, e, "wh")
	dim := SlotDimension{Key: "cap:bytes", MaxUnits: 50, Units: 50}
	if g := acquire(t, s, "a", time.Minute, dim); !g.Granted {
		t.Fatal("a denied")
	}
	if g := acquire(t, s, "b", time.Minute, dim); g.Granted {
		t.Fatal("b admitted")
	}
	if err := s.Release(t.Context(), "a", dim.Key); err != nil {
		t.Fatal(err)
	}
	n, err := e.rdb.HLen(t.Context(), e.key("wh", dim.Key+unitsSuffix)).Result()
	if err != nil || n != 0 {
		t.Fatalf("%d size entries left after release (%v)", n, err)
	}
	if g := acquire(t, s, "b", time.Minute, dim); !g.Granted {
		t.Fatal("units not freed")
	}
}

func TestSlotsExpireAndRenew(t *testing.T) {
	e := newEnv(t)
	s := newSlots(t, e, "gw")
	dim := SlotDimension{Key: "sse:s:ttl", MaxCount: 1}
	if g := acquire(t, s, "a", 300*time.Millisecond, dim); !g.Granted {
		t.Fatal("a denied")
	}
	if ttl := e.pttl(t, e.key("gw", dim.Key)); ttl <= 0 || ttl > 300*time.Millisecond {
		t.Fatalf("key ttl=%v", ttl)
	}
	for range 3 {
		time.Sleep(150 * time.Millisecond)
		ok, err := s.Renew(t.Context(), "a", 300*time.Millisecond, dim.Key)
		if err != nil || !ok {
			t.Fatalf("renew: %v, %v", ok, err)
		}
	}
	if g := acquire(t, s, "b", time.Second, dim); g.Granted {
		t.Fatal("renewed slot was taken")
	}
	time.Sleep(450 * time.Millisecond)
	if ok, err := s.Renew(t.Context(), "a", time.Second, dim.Key); err != nil || ok {
		t.Fatalf("renew after expiry: %v, %v", ok, err)
	}
	if g := acquire(t, s, "b", time.Second, dim); !g.Granted {
		t.Fatal("expired slot not reclaimed")
	}
	// The expired holder cannot come back over the limit by renewing.
	if ok, err := s.Renew(t.Context(), "a", time.Second, dim.Key); err != nil || ok {
		t.Fatalf("renew of a reclaimed slot: %v, %v", ok, err)
	}
}

func TestSlotsRenewIsAllOrNothing(t *testing.T) {
	e := newEnv(t)
	s := newSlots(t, e, "gw")
	long := SlotDimension{Key: "sse:o:1", MaxCount: 5}
	short := SlotDimension{Key: "sse:s:1", MaxCount: 5}
	acquire(t, s, "a", 10*time.Second, long, short)
	if err := s.Release(t.Context(), "a", short.Key); err != nil {
		t.Fatal(err)
	}
	before := e.pttl(t, e.key("gw", long.Key))
	if ok, err := s.Renew(t.Context(), "a", time.Minute, long.Key, short.Key); err != nil || ok {
		t.Fatalf("renew with a lost dimension: %v, %v", ok, err)
	}
	if after := e.pttl(t, e.key("gw", long.Key)); after > before {
		t.Fatalf("a failed renew extended the other dimension: %v -> %v", before, after)
	}
}

func TestSlotsReacquireBySameHolderIsNotCountedTwice(t *testing.T) {
	e := newEnv(t)
	s := newSlots(t, e, "wh")
	dims := []SlotDimension{{Key: "capslots:g:r", MaxCount: 1}, {Key: "capbytes:g:r", MaxUnits: 100, Units: 60}}
	for range 3 {
		if g := acquire(t, s, "same", time.Minute, dims...); !g.Granted {
			t.Fatalf("reacquire denied: %+v", g)
		}
	}
	if n := members(t, e, "wh", "capslots:g:r"); n != 1 {
		t.Fatalf("%d members", n)
	}
	if g := acquire(t, s, "other", time.Minute, dims...); g.Granted {
		t.Fatal("other admitted")
	}
}

func TestSlotsOnCacheRedis(t *testing.T) {
	e := newEnvOn(t, itest.RedisCacheURL(t))
	s := newSlots(t, e, "wh")
	dim := SlotDimension{Key: "capslots:g:r", MaxCount: 4}
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			g, err := s.Acquire(t.Context(), SlotRequest{Holder: fmt.Sprintf("c-%d", i), TTL: time.Minute, Dimensions: []SlotDimension{dim}})
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			if g.Granted {
				granted.Add(1)
			}
		})
	}
	wg.Wait()
	if granted.Load() != 4 {
		t.Fatalf("granted=%d, want 4", granted.Load())
	}
}
