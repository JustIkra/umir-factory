//go:build integration

package redis

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func newContinuity(t *testing.T, e *env, window time.Duration, clock *fakeClock) *Continuity {
	t.Helper()
	c, err := e.c.Continuity(ContinuityConfig{Window: window, Clock: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func startClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
}

func TestContinuityMarkerMatchesIsNormal(t *testing.T) {
	e := newEnv(t)
	clock := startClock()
	c := newContinuity(t, e, time.Hour, clock)
	if err := e.rdb.Set(t.Context(), e.key("limiter", "generation"), "gen-1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	st, err := c.Check(t.Context(), "gen-1")
	if err != nil || st.Mode != Normal || st.Remaining != 0 {
		t.Fatalf("got %+v, %v", st, err)
	}
	if e.exists(t, e.key("limiter", "strict_since")) {
		t.Fatal("strict_since written in normal mode")
	}
}

func TestContinuityMissingMarkerStartsStrictWindowThenRecovers(t *testing.T) {
	e := newEnv(t)
	clock := startClock()
	const window = 10 * time.Minute
	c := newContinuity(t, e, window, clock)
	marker, since := e.key("limiter", "generation"), e.key("limiter", "strict_since")

	st, err := c.Check(t.Context(), "gen-1")
	if err != nil || st.Mode != Strict || st.Reason != ReasonMarkerMissing || st.Remaining != window {
		t.Fatalf("first check: %+v, %v", st, err)
	}
	want := strconv.FormatInt(clock.Now().UnixMilli(), 10)
	if v, ok := e.get(t, since); !ok || v != want {
		t.Fatalf("strict_since=%q, want %q", v, want)
	}
	if e.exists(t, marker) {
		t.Fatal("Check wrote the marker while the window runs")
	}

	// Later checks (any replica) keep the first moment: SET NX.
	clock.Advance(4 * time.Minute)
	st, err = c.Check(t.Context(), "gen-1")
	if err != nil || st.Mode != Strict || st.Remaining != 6*time.Minute {
		t.Fatalf("second check: %+v, %v", st, err)
	}
	if v, _ := e.get(t, since); v != want {
		t.Fatalf("strict_since moved to %q", v)
	}

	// Just before the end: still strict.
	clock.Advance(6*time.Minute - time.Millisecond)
	if st, err = c.Check(t.Context(), "gen-1"); err != nil || st.Mode != Strict || st.Remaining != time.Millisecond {
		t.Fatalf("before the end: %+v, %v", st, err)
	}

	// The window is over: the marker is restored and strict_since removed.
	clock.Advance(time.Millisecond)
	st, err = c.Check(t.Context(), "gen-1")
	if err != nil || st.Mode != Normal {
		t.Fatalf("after the window: %+v, %v", st, err)
	}
	if v, ok := e.get(t, marker); !ok || v != "gen-1" {
		t.Fatalf("marker=%q, want gen-1", v)
	}
	if e.exists(t, since) {
		t.Fatal("strict_since was not removed")
	}
	if e.pttl(t, marker) != -1 {
		t.Fatal("the marker must have no TTL")
	}
	if st, err = c.Check(t.Context(), "gen-1"); err != nil || st.Mode != Normal {
		t.Fatalf("after recovery: %+v, %v", st, err)
	}
	// Losing the marker again starts a fresh window from the new moment.
	if err := e.rdb.Del(t.Context(), marker).Err(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if st, err = c.Check(t.Context(), "gen-1"); err != nil || st.Mode != Strict || st.Remaining != window {
		t.Fatalf("second loss: %+v, %v", st, err)
	}
}

func TestContinuityRealShortWindow(t *testing.T) {
	e := newEnv(t)
	c, err := e.c.Continuity(ContinuityConfig{Window: 400 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.Check(t.Context(), "gen-real")
	if err != nil || st.Mode != Strict {
		t.Fatalf("got %+v, %v", st, err)
	}
	time.Sleep(600 * time.Millisecond)
	if st, err = c.Check(t.Context(), "gen-real"); err != nil || st.Mode != Normal {
		t.Fatalf("after the window: %+v, %v", st, err)
	}
}

func TestContinuityMismatchIsStrictWithoutWrites(t *testing.T) {
	e := newEnv(t)
	clock := startClock()
	c := newContinuity(t, e, time.Minute, clock)
	marker := e.key("limiter", "generation")
	if err := e.rdb.Set(t.Context(), marker, "gen-other", 0).Err(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		st, err := c.Check(t.Context(), "gen-1")
		if err != nil || st.Mode != Strict || st.Reason != ReasonMarkerMismatch {
			t.Fatalf("got %+v, %v", st, err)
		}
		clock.Advance(time.Hour)
	}
	if v, _ := e.get(t, marker); v != "gen-other" {
		t.Fatalf("marker overwritten with %q", v)
	}
	if e.exists(t, e.key("limiter", "strict_since")) {
		t.Fatal("strict_since written for a mismatch")
	}
}

func TestContinuityRestoredMarkerClearsStaleStrictSince(t *testing.T) {
	e := newEnv(t)
	clock := startClock()
	c := newContinuity(t, e, time.Hour, clock)
	since := e.key("limiter", "strict_since")
	if _, err := c.Check(t.Context(), "gen-1"); err != nil { // enters the strict window
		t.Fatal(err)
	}
	// redis-init restores the marker during the window.
	if err := e.rdb.Set(t.Context(), e.key("limiter", "generation"), "gen-1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	st, err := c.Check(t.Context(), "gen-1")
	if err != nil || st.Mode != Normal {
		t.Fatalf("got %+v, %v", st, err)
	}
	if e.exists(t, since) {
		t.Fatal("stale strict_since stayed")
	}
}

func TestContinuityCorruptStrictSinceRestartsWindow(t *testing.T) {
	e := newEnv(t)
	clock := startClock()
	c := newContinuity(t, e, time.Minute, clock)
	if err := e.rdb.Set(t.Context(), e.key("limiter", "strict_since"), "garbage", 0).Err(); err != nil {
		t.Fatal(err)
	}
	st, err := c.Check(t.Context(), "gen-1")
	if err != nil || st.Mode != Strict || st.Remaining != time.Minute {
		t.Fatalf("got %+v, %v", st, err)
	}
	// A moment in the future (clock skew) never lengthens the window.
	if err := e.rdb.Set(t.Context(), e.key("limiter", "strict_since"), strconv.FormatInt(clock.Now().Add(time.Hour).UnixMilli(), 10), 0).Err(); err != nil {
		t.Fatal(err)
	}
	if st, err = c.Check(t.Context(), "gen-1"); err != nil || st.Mode != Strict || st.Remaining != time.Minute {
		t.Fatalf("future strict_since: %+v, %v", st, err)
	}
}

func TestContinuityParallelReplicasShareOneMoment(t *testing.T) {
	e := newEnv(t)
	clock := startClock()
	c := newContinuity(t, e, time.Hour, clock)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if st, err := c.Check(t.Context(), "gen-1"); err != nil || st.Mode != Strict {
				t.Errorf("got %+v, %v", st, err)
			}
		})
	}
	wg.Wait()
	if v, ok := e.get(t, e.key("limiter", "strict_since")); !ok || v != strconv.FormatInt(clock.Now().UnixMilli(), 10) {
		t.Fatalf("strict_since=%q", v)
	}
}

func TestContinuityRejectsEmptyConfigured(t *testing.T) {
	e := newEnv(t)
	c := newContinuity(t, e, time.Hour, startClock())
	if _, err := c.Check(t.Context(), ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("got %v", err)
	}
}
