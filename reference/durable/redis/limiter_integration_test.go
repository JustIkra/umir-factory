//go:build integration

package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/JustIkra/umir-factory/reference/durable/itest"
)

func newLimiter(t *testing.T, e *env) *Limiter {
	t.Helper()
	l, err := e.c.Limiter("gw")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func req(id, fp string, dims ...Dimension) Request {
	return Request{ID: id, Fingerprint: fp, Policy: "test.rule", Dimensions: dims}
}

func counter(t *testing.T, e *env, dimKey string) int {
	t.Helper()
	v, ok := e.get(t, e.key("gw", "adm:"+dimKey))
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("counter %q is not a number", v)
	}
	return n
}

func mustAdmit(t *testing.T, l *Limiter, r Request) Decision {
	t.Helper()
	d, err := l.Admit(t.Context(), r)
	if err != nil {
		t.Fatalf("admit %s: %v", r.ID, err)
	}
	return d
}

func TestAdmitLastSlotUnder64ParallelCalls(t *testing.T) {
	e := newEnv(t)
	l := newLimiter(t, e)
	const limit = 20
	dim := Dimension{Key: "last:slot", Limit: limit, Window: time.Minute}
	for i := range limit - 1 {
		if d := mustAdmit(t, l, req(fmt.Sprintf("pre-%d", i), "fp", dim)); !d.Allowed {
			t.Fatalf("pre-fill %d denied", i)
		}
	}
	var allowed, denied atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 64 {
		wg.Go(func() {
			<-start
			d, err := l.Admit(t.Context(), req(fmt.Sprintf("par-%d", i), "fp", dim))
			if err != nil {
				t.Errorf("admit: %v", err)
				return
			}
			if d.Allowed {
				allowed.Add(1)
			} else {
				denied.Add(1)
				if d.Reason != ReasonLimitExceeded || d.RetryAfter <= 0 || d.RetryAfter > time.Minute {
					t.Errorf("bad denial %+v", d)
				}
			}
		})
	}
	close(start)
	wg.Wait()
	if allowed.Load() != 1 || denied.Load() != 63 {
		t.Fatalf("allowed=%d denied=%d, want 1 and 63", allowed.Load(), denied.Load())
	}
	if n := counter(t, e, dim.Key); n != limit {
		t.Fatalf("counter=%d, want %d", n, limit)
	}
}

func TestAdmitManyDistinctIDsNeverExceedLimit(t *testing.T) {
	e := newEnv(t)
	l := newLimiter(t, e)
	dim := Dimension{Key: "burst", Limit: 10, Window: time.Minute}
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			d, err := l.Admit(t.Context(), req(fmt.Sprintf("b-%d", i), "fp", dim))
			if err != nil {
				t.Errorf("admit: %v", err)
				return
			}
			if d.Allowed {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("allowed=%d, want 10", allowed.Load())
	}
}

func TestAdmitWindowIsFixedAndNeverExtended(t *testing.T) {
	e := newEnv(t)
	l := newLimiter(t, e)
	const window = 1500 * time.Millisecond
	dim := Dimension{Key: "fixed:window", Limit: 3, Window: window}
	key := e.key("gw", "adm:"+dim.Key)

	mustAdmit(t, l, req("w-1", "fp", dim))
	first := e.pttl(t, key)
	if first <= 0 || first > window {
		t.Fatalf("ttl after first admit = %v, want within (0, %v]", first, window)
	}
	time.Sleep(400 * time.Millisecond)
	if d := mustAdmit(t, l, req("w-2", "fp", dim)); !d.Allowed {
		t.Fatal("second admit denied")
	}
	if d := mustAdmit(t, l, req("w-1", "fp", dim)); !d.Allowed || d.Reason != ReasonReplay {
		t.Fatalf("replay: %+v", d)
	}
	second := e.pttl(t, key)
	if second <= 0 || second > first-300*time.Millisecond {
		t.Fatalf("ttl %v after a later admit and a replay (first %v): the window was extended", second, first)
	}
	// Exhaust, then deny with the remaining window as RetryAfter.
	mustAdmit(t, l, req("w-3", "fp", dim))
	d := mustAdmit(t, l, req("w-4", "fp", dim))
	if d.Allowed || d.Reason != ReasonLimitExceeded || d.Dimension != 0 {
		t.Fatalf("expected denial, got %+v", d)
	}
	left := e.pttl(t, key)
	if d.RetryAfter <= 0 || d.RetryAfter > left+50*time.Millisecond || d.RetryAfter < left-300*time.Millisecond {
		t.Fatalf("RetryAfter=%v, remaining window %v", d.RetryAfter, left)
	}
	if e.exists(t, e.key("gw", "admid:"+idRecordName("test.rule", "w-4"))) {
		t.Fatal("a denied request left an admission record")
	}
	// A new window starts after expiry and the ID records are gone with it.
	time.Sleep(d.RetryAfter + 200*time.Millisecond)
	if e.exists(t, key) || e.exists(t, e.key("gw", "admid:"+idRecordName("test.rule", "w-1"))) {
		t.Fatal("window keys outlived the window")
	}
	if d := mustAdmit(t, l, req("w-4", "fp", dim)); !d.Allowed || d.Reason == ReasonReplay {
		t.Fatalf("new window: %+v", d)
	}
	if n := counter(t, e, dim.Key); n != 1 {
		t.Fatalf("counter=%d in the new window, want 1", n)
	}
}

func TestAdmitSameIDIsChargedOnce(t *testing.T) {
	e := newEnv(t)
	l := newLimiter(t, e)
	dim := Dimension{Key: "idempotent", Limit: 2, Window: time.Minute}
	for i := range 5 {
		d := mustAdmit(t, l, req("same", "fp", dim))
		if !d.Allowed {
			t.Fatalf("attempt %d denied", i)
		}
		if wantReplay := i > 0; (d.Reason == ReasonReplay) != wantReplay {
			t.Fatalf("attempt %d reason %q", i, d.Reason)
		}
	}
	if n := counter(t, e, dim.Key); n != 1 {
		t.Fatalf("counter=%d after 5 identical attempts, want 1", n)
	}
	mustAdmit(t, l, req("other", "fp", dim))
	if d := mustAdmit(t, l, req("third", "fp", dim)); d.Allowed {
		t.Fatal("limit 2 admitted a third distinct ID")
	}
	// An already admitted ID stays admitted even when the budget is now spent.
	if d := mustAdmit(t, l, req("same", "fp", dim)); !d.Allowed || d.Reason != ReasonReplay {
		t.Fatalf("replay on an exhausted budget: %+v", d)
	}
	if n := counter(t, e, dim.Key); n != 2 {
		t.Fatalf("counter=%d, want 2", n)
	}
	// Policies have separate ID spaces.
	other := req("same", "fp", Dimension{Key: "idempotent:p2", Limit: 2, Window: time.Minute})
	other.Policy = "test.other"
	if d := mustAdmit(t, l, other); !d.Allowed || d.Reason == ReasonReplay {
		t.Fatalf("same ID in another policy: %+v", d)
	}
}

func TestAdmitFingerprintChangeIsConflict(t *testing.T) {
	e := newEnv(t)
	l := newLimiter(t, e)
	dim := Dimension{Key: "conflict", Limit: 5, Window: time.Minute}
	mustAdmit(t, l, req("c-1", "fp-a", dim))
	d, err := l.Admit(t.Context(), req("c-1", "fp-b", dim))
	if !errors.Is(err, ErrAdmissionConflict) || d.Allowed {
		t.Fatalf("got %+v, %v; want ErrAdmissionConflict", d, err)
	}
	if errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatal("conflict reported as unavailable")
	}
	if n := counter(t, e, dim.Key); n != 1 {
		t.Fatalf("counter=%d, want 1", n)
	}
	if d := mustAdmit(t, l, req("c-1", "fp-a", dim)); !d.Allowed || d.Reason != ReasonReplay {
		t.Fatalf("original fingerprint after conflict: %+v", d)
	}
}

func TestAdmitChecksAllDimensionsBeforeWriting(t *testing.T) {
	e := newEnv(t)
	l := newLimiter(t, e)
	roomy := Dimension{Key: "multi:roomy", Limit: 10, Window: time.Minute}
	tight := Dimension{Key: "multi:tight", Limit: 1, Window: 5 * time.Second}
	if d := mustAdmit(t, l, req("m-1", "fp", roomy, tight)); !d.Allowed {
		t.Fatal("first denied")
	}
	d := mustAdmit(t, l, req("m-2", "fp", roomy, tight))
	if d.Allowed || d.Dimension != 1 || d.Reason != ReasonLimitExceeded {
		t.Fatalf("got %+v, want denial by dimension 1", d)
	}
	if n := counter(t, e, roomy.Key); n != 1 {
		t.Fatalf("roomy counter=%d after a denied request, want 1", n)
	}
	if d.RetryAfter <= 0 || d.RetryAfter > 5*time.Second {
		t.Fatalf("RetryAfter=%v, want the tight window", d.RetryAfter)
	}
	// The ID record lives as long as the longest window and is not extended.
	rec := e.key("gw", "admid:"+idRecordName("test.rule", "m-1"))
	if ttl := e.pttl(t, rec); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("id record ttl=%v", ttl)
	}
}

func TestAdmitMemoryIsBounded(t *testing.T) {
	e := newEnv(t)
	l := newLimiter(t, e)
	dim := Dimension{Key: "bounded", Limit: 5, Window: time.Minute}
	for i := range 200 {
		mustAdmit(t, l, req(fmt.Sprintf("id-%d", i), "fp", dim))
	}
	var keys int
	iter := e.rdb.Scan(t.Context(), 0, e.root+"umir:gw:adm*", 100).Iterator()
	for iter.Next(t.Context()) {
		keys++
	}
	if err := iter.Err(); err != nil {
		t.Fatal(err)
	}
	if keys != 1+5 { // one counter plus one record per admitted ID
		t.Fatalf("%d keys after 200 attempts with limit 5, want 6", keys)
	}
}

// loadObserver records the commands of one client and can turn them into an
// event log.
type loadObserver struct {
	mu  sync.Mutex
	log []string
}

func (o *loadObserver) DialHook(next goredis.DialHook) goredis.DialHook { return next }
func (o *loadObserver) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		err := next(ctx, cmd)
		name := strings.ToLower(cmd.Name())
		entry := name
		if err != nil && strings.HasPrefix(err.Error(), "NOSCRIPT") {
			entry += ":noscript"
		}
		if name == "evalsha" || name == "script" || name == "eval" {
			o.mu.Lock()
			o.log = append(o.log, entry)
			o.mu.Unlock()
		}
		return err
	}
}
func (o *loadObserver) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return next
}
func (o *loadObserver) events() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.log...)
}

func TestAdmitLoadsScriptAfterNoscriptAndRetries(t *testing.T) {
	e := newEnv(t)
	l := newLimiter(t, e)
	// A script text the server has never seen: its SHA is unknown, so the
	// first EVALSHA answers NOSCRIPT without anyone flushing the script cache.
	l.script = newScript(limiterScript + "\n-- noscript test " + e.root)
	known, err := e.rdb.ScriptExists(t.Context(), l.script.sha).Result()
	if err != nil || len(known) != 1 || known[0] {
		t.Fatalf("test script is already known to the server: %v, %v", known, err)
	}
	obs := &loadObserver{}
	e.rdb.AddHook(obs)
	dim := Dimension{Key: "noscript", Limit: 3, Window: time.Minute}
	if d := mustAdmit(t, l, req("n-1", "fp", dim)); !d.Allowed {
		t.Fatal("admit after NOSCRIPT was denied")
	}
	got := strings.Join(obs.events(), ",")
	if got != "evalsha:noscript,script,evalsha" {
		t.Fatalf("command sequence %q, want evalsha:noscript,script,evalsha", got)
	}
	known, err = e.rdb.ScriptExists(t.Context(), l.script.sha).Result() // a SCRIPT call, after the checked sequence
	if err != nil || !known[0] {
		t.Fatalf("script was not loaded: %v, %v", known, err)
	}
	// The next call goes straight through EVALSHA.
	seen := len(obs.events())
	mustAdmit(t, l, req("n-2", "fp", dim))
	if got := strings.Join(obs.events()[seen:], ","); got != "evalsha" {
		t.Fatalf("second call sequence %q, want evalsha", got)
	}
	if n := counter(t, e, dim.Key); n != 2 {
		t.Fatalf("counter=%d, want 2 (the NOSCRIPT retry must not double-count)", n)
	}
}

func TestAdmitUnknownShaFromOtherClientRecovers(t *testing.T) {
	e := newEnv(t)
	l := newLimiter(t, e)
	l.script.sha = strings.Repeat("0", 40) // never loaded anywhere
	dim := Dimension{Key: "badsha", Limit: 3, Window: time.Minute}
	if d := mustAdmit(t, l, req("s-1", "fp", dim)); !d.Allowed {
		t.Fatal("denied")
	}
	if l.script.sha == strings.Repeat("0", 40) {
		t.Fatal("the SHA was not corrected after loading")
	}
}

func TestAdmitUnavailableOnConnectionFailures(t *testing.T) {
	raw := itest.RedisLimiterURL(t)
	root := itest.RedisPrefixOn(t, raw)
	proxy := itest.NewTCPProxy(t, targetOf(t, raw))
	c, err := Open(t.Context(), Config{URL: viaProxy(t, raw, proxy), Timeout: 400 * time.Millisecond, KeyRoot: root})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { closeClient(t, c) })
	direct := newEnvOn(t, raw)
	direct.root = root // read the same keys the proxied client writes
	l, err := c.Limiter("gw")
	if err != nil {
		t.Fatal(err)
	}
	dim := Dimension{Key: "outage", Limit: 5, Window: time.Minute}
	ctx := t.Context()

	if d, err := l.Admit(ctx, req("o-0", "fp", dim)); err != nil || !d.Allowed {
		t.Fatalf("baseline: %+v, %v", d, err)
	}

	t.Run("cut in the middle of the request", func(t *testing.T) {
		proxy.CutAfterMatch(itest.ToServer, []byte("evalsha"))
		d, err := l.Admit(ctx, req("o-2", "fp", dim))
		if !errors.Is(err, ErrAdmissionUnavailable) || d.Allowed {
			t.Fatalf("got %+v, %v", d, err)
		}
		if n := counter(t, direct, dim.Key); n != 1 {
			t.Fatalf("counter=%d, the half-sent request must not be applied", n)
		}
	})

	t.Run("lost reply then retry with the same ID", func(t *testing.T) {
		// Warm the pool: a fresh connection would lose its handshake replies
		// and never send the request.
		if err := c.Ping(ctx); err != nil {
			t.Fatalf("ping: %v", err)
		}
		proxy.DropToClient(true)
		d, err := l.Admit(ctx, req("o-3", "fp", dim))
		proxy.DropToClient(false)
		if !errors.Is(err, ErrAdmissionUnavailable) || d.Allowed {
			t.Fatalf("got %+v, %v", d, err)
		}
		// The server did apply it; the retry is recognised and not charged again.
		d, err = l.Admit(ctx, req("o-3", "fp", dim))
		if err != nil || !d.Allowed || d.Reason != ReasonReplay {
			t.Fatalf("retry: %+v, %v", d, err)
		}
		if n := counter(t, direct, dim.Key); n != 2 {
			t.Fatalf("counter=%d, want 2", n)
		}
	})

	t.Run("server gone", func(t *testing.T) {
		proxy.Close()
		d, err := l.Admit(ctx, req("o-4", "fp", dim))
		if !errors.Is(err, ErrAdmissionUnavailable) || d.Allowed {
			t.Fatalf("got %+v, %v", d, err)
		}
	})
}

func TestAdmitTimeoutIsUnavailable(t *testing.T) {
	raw := itest.RedisLimiterURL(t)
	root := itest.RedisPrefixOn(t, raw)
	proxy := itest.NewTCPProxy(t, targetOf(t, raw))
	c, err := Open(t.Context(), Config{URL: viaProxy(t, raw, proxy), Timeout: 200 * time.Millisecond, KeyRoot: root})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { closeClient(t, c) })
	l, err := c.Limiter("gw")
	if err != nil {
		t.Fatal(err)
	}
	proxy.Pause()
	defer proxy.Resume()
	began := time.Now()
	d, err := l.Admit(t.Context(), req("t-1", "fp", Dimension{Key: "timeout", Limit: 1, Window: time.Minute}))
	if !errors.Is(err, ErrAdmissionUnavailable) || d.Allowed {
		t.Fatalf("got %+v, %v", d, err)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Fatalf("the call took %v with a 200ms timeout", took)
	}
}

func TestAdmitCanceledContextIsUnavailable(t *testing.T) {
	e := newEnv(t)
	l := newLimiter(t, e)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	d, err := l.Admit(ctx, req("x-1", "fp", Dimension{Key: "canceled", Limit: 5, Window: time.Minute}))
	if !errors.Is(err, ErrAdmissionUnavailable) || d.Allowed {
		t.Fatalf("got %+v, %v", d, err)
	}
	if counter(t, e, "canceled") != 0 {
		t.Fatal("a canceled call wrote a counter")
	}
}
