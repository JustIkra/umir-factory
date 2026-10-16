package relay_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustIkra/umir-factory/reference/durable/relay"
)

func TestConfirmedRowsAreMarkedAfterPublishInPositionOrder(t *testing.T) {
	// Rows deliberately handed over out of order by the store.
	store := newFakeStore(eventRow(1), eventRow(2), eventRow(3))
	store.rows[0], store.rows[2] = store.rows[2], store.rows[0]
	pub := &fakePub{}
	var markedWhenPublished []int
	pub.script = func(_ int, _ context.Context, _ relay.Message) (relay.Outcome, error) {
		marks, _ := store.snapshot()
		markedWhenPublished = append(markedWhenPublished, len(marks))
		return relay.Confirmed, nil
	}
	r := newRelay(store, pub)
	if err := runCycles(t, r, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if got, want := pub.ids(), []string{"evt1", "evt2", "evt3"}; !slices.Equal(got, want) {
		t.Fatalf("published %v, want %v (position order)", got, want)
	}
	if !slices.Equal(markedWhenPublished, []int{0, 1, 2}) {
		t.Fatalf("marks before each publish = %v: a row must be marked only after its own confirm and before the next publish", markedWhenPublished)
	}
	marks, releases := store.snapshot()
	if len(marks) != 3 || len(releases) != 0 {
		t.Fatalf("marks=%v releases=%v", marks, releases)
	}
	for i, m := range marks {
		if m.Owner != "relay-a" || m.Fence != 1 || !m.Routed || len(m.IDs) != 1 || m.IDs[0] != fmt.Sprintf("row%d", i+1) {
			t.Fatalf("mark %d = %+v", i, m)
		}
	}
	st, err := r.Stats(t.Context())
	if err != nil || st.Published != 3 || st.ReturnedAccepted != 0 {
		t.Fatalf("stats = %+v, %v", st, err)
	}
}

func TestMessageFieldsFollowTheCatalog(t *testing.T) {
	ev := eventRow(1)
	ev.Headers["x-umir-schema"] = "3"
	task := taskRow(2)
	store := newFakeStore(ev, task)
	pub := &fakePub{}
	if err := runCycles(t, newRelay(store, pub), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	msgs := pub.messages()
	if len(msgs) != 2 {
		t.Fatalf("published %d messages", len(msgs))
	}
	want := relay.Message{
		Exchange: "umir.events", RoutingKey: "warehouse.OrderPlaced", MessageID: "evt1", Type: "warehouse.OrderPlaced",
		Schema: 3, RestaurantID: "r1", Generation: "g1", Body: ev.Body,
	}
	if fmt.Sprintf("%+v", msgs[0]) != fmt.Sprintf("%+v", want) {
		t.Fatalf("event message = %+v, want %+v", msgs[0], want)
	}
	wantTask := relay.Message{
		Exchange: "umir.tasks", RoutingKey: "auth.mail", MessageID: "msg2", Type: "auth.mail",
		Schema: 1, Generation: "g1", Body: task.Body,
	}
	if fmt.Sprintf("%+v", msgs[1]) != fmt.Sprintf("%+v", wantTask) {
		t.Fatalf("task message = %+v, want %+v", msgs[1], wantTask)
	}
}

func TestMessageFallsBackToBodyForSchemaAndGeneration(t *testing.T) {
	row := eventRow(1)
	row.Headers = map[string]string{}
	store := newFakeStore(row)
	pub := &fakePub{}
	if err := runCycles(t, newRelay(store, pub), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	msgs := pub.messages()
	if len(msgs) != 1 || msgs[0].Schema != 1 || msgs[0].Generation != "g1" || msgs[0].RestaurantID != "r1" {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestReturnedAcceptedMarksUnroutedAndCounts(t *testing.T) {
	store := newFakeStore(eventRow(1), eventRow(2))
	pub := &fakePub{script: func(call int, _ context.Context, _ relay.Message) (relay.Outcome, error) {
		if call == 1 {
			return relay.Returned, nil
		}
		return relay.Confirmed, nil
	}}
	r := newRelay(store, pub)
	if err := runCycles(t, r, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	marks, releases := store.snapshot()
	if len(releases) != 0 || len(marks) != 2 {
		t.Fatalf("marks=%v releases=%v", marks, releases)
	}
	if marks[0].Routed || marks[0].IDs[0] != "row1" {
		t.Fatalf("first mark = %+v, want row1 with routed=false", marks[0])
	}
	if !marks[1].Routed {
		t.Fatalf("second mark = %+v, want routed=true", marks[1])
	}
	st := mustStats(t, r)
	if st.ReturnedAccepted != 1 || st.Published != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestReturnedRetryReleasesWithDelayAndContinues(t *testing.T) {
	store := newFakeStore(taskRow(1), taskRow(2))
	pub := &fakePub{script: func(call int, _ context.Context, _ relay.Message) (relay.Outcome, error) {
		if call == 1 {
			return relay.Returned, nil
		}
		return relay.Confirmed, nil
	}}
	r := newRelay(store, pub)
	r.RetryBase = 2 * time.Second
	r.RetryMax = time.Minute
	if err := runCycles(t, r, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	marks, releases := store.snapshot()
	if len(releases) != 1 || releases[0].IDs[0] != "row1" || len(releases[0].IDs) != 1 || releases[0].Delay != 2*time.Second {
		t.Fatalf("releases = %+v", releases)
	}
	if releases[0].Owner != "relay-a" || releases[0].Fence != 1 {
		t.Fatalf("release claim = %+v", releases[0])
	}
	if len(marks) != 1 || marks[0].IDs[0] != "row2" || !marks[0].Routed {
		t.Fatalf("marks = %+v: the row behind a returned task must still be published", marks)
	}
	if store.stateOf("row1") == "published" {
		t.Fatal("a retry-policy return must leave the row unpublished")
	}
	st := mustStats(t, r)
	if st.ReturnedRetried != 1 || st.ReturnedAccepted != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestCustomPolicyOverridesTheDefault(t *testing.T) {
	store := newFakeStore(taskRow(1))
	pub := &fakePub{script: func(int, context.Context, relay.Message) (relay.Outcome, error) { return relay.Returned, nil }}
	r := newRelay(store, pub)
	r.Policy = func(exchange, key string) relay.ReturnPolicy {
		if exchange != "umir.tasks" || key != "auth.mail" {
			t.Errorf("policy asked for %q/%q", exchange, key)
		}
		return relay.Accept
	}
	if err := runCycles(t, r, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	marks, _ := store.snapshot()
	if len(marks) != 1 || marks[0].Routed {
		t.Fatalf("marks = %+v", marks)
	}
}

func TestNackedAndUnknownStopTheBatchAndReleaseTheRest(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  relay.Outcome
		err  error
	}{
		{"nacked", relay.Nacked, nil},
		{"unknown", relay.Unknown, errors.New("connection lost")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore(eventRow(1), eventRow(2), eventRow(3))
			pub := &fakePub{script: func(call int, _ context.Context, _ relay.Message) (relay.Outcome, error) {
				if call == 2 {
					return tc.out, tc.err
				}
				return relay.Confirmed, nil
			}}
			r := newRelay(store, pub)
			r.RetryBase = time.Second
			if err := runCycles(t, r, 1); !errors.Is(err, context.Canceled) {
				t.Fatalf("Run = %v", err)
			}
			if got := pub.ids(); !slices.Equal(got, []string{"evt1", "evt2"}) {
				t.Fatalf("published %v: nothing may be published behind a failed row", got)
			}
			marks, releases := store.snapshot()
			if len(marks) != 1 || marks[0].IDs[0] != "row1" {
				t.Fatalf("marks = %+v", marks)
			}
			if len(releases) != 1 || !slices.Equal(releases[0].IDs, []string{"row2", "row3"}) || releases[0].Delay != time.Second {
				t.Fatalf("releases = %+v, want row2 and row3 with the retry delay", releases)
			}
			st := mustStats(t, r)
			if tc.out == relay.Nacked && st.Nacked != 1 || tc.out == relay.Unknown && st.Unknown != 1 {
				t.Fatalf("stats = %+v", st)
			}
		})
	}
}

func TestUnknownRepeatsWithThePreviousEventID(t *testing.T) {
	store := newFakeStore(eventRow(1))
	pub := &fakePub{script: func(call int, _ context.Context, _ relay.Message) (relay.Outcome, error) {
		if call == 1 {
			return relay.Unknown, errors.New("timeout")
		}
		return relay.Confirmed, nil
	}}
	r := newRelay(store, pub)
	if err := runCycles(t, r, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	msgs := pub.messages()
	if len(msgs) != 2 || msgs[0].MessageID != "evt1" || msgs[1].MessageID != "evt1" {
		t.Fatalf("messages = %+v: the repeat must carry the same message_id", msgs)
	}
	if string(msgs[0].Body) != string(msgs[1].Body) {
		t.Fatal("the repeat must carry the same body")
	}
	if store.stateOf("row1") != "published" {
		t.Fatalf("row state = %q", store.stateOf("row1"))
	}
}

func TestBackoffGrowsWithAttemptsUpToTheCap(t *testing.T) {
	rows := []relay.Row{eventRow(1)}
	rows[0].Attempts = 0
	store := newFakeStore(rows...)
	pub := &fakePub{script: func(int, context.Context, relay.Message) (relay.Outcome, error) {
		return relay.Nacked, nil
	}}
	r := newRelay(store, pub)
	r.RetryBase = time.Second
	r.RetryMax = 5 * time.Second
	if err := runCycles(t, r, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	_, releases := store.snapshot()
	var got []time.Duration
	for _, rel := range releases {
		got = append(got, rel.Delay)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	if !slices.Equal(got, want) {
		t.Fatalf("delays = %v, want %v", got, want)
	}
}

func TestLeaseLostOnMarkStopsTheBatch(t *testing.T) {
	store := newFakeStore(eventRow(1), eventRow(2), eventRow(3))
	store.markErr = func(c markCall) error {
		if c.IDs[0] == "row2" {
			return fmt.Errorf("outbox: %w", relay.ErrLeaseLost)
		}
		return nil
	}
	pub := &fakePub{}
	r := newRelay(store, pub)
	if err := runCycles(t, r, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run must keep running after a lost lease, got %v", err)
	}
	if got := pub.ids(); !slices.Equal(got, []string{"evt1", "evt2"}) {
		t.Fatalf("published %v: a lost lease must stop the batch", got)
	}
	_, releases := store.snapshot()
	if len(releases) != 0 {
		t.Fatalf("releases = %+v: rows of a lost claim are not ours to release", releases)
	}
	st := mustStats(t, r)
	if st.LeaseLost != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestMarkStoreErrorStopsTheBatchAndReportsIt(t *testing.T) {
	store := newFakeStore(eventRow(1), eventRow(2))
	boom := errors.New("database down")
	store.markErr = func(markCall) error { return boom }
	pub := &fakePub{}
	r := newRelay(store, pub)
	if err := runCycles(t, r, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if got := pub.ids(); !slices.Equal(got, []string{"evt1"}) {
		t.Fatalf("published %v", got)
	}
	st := mustStats(t, r)
	if st.StoreErrors != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestLeaseRunningOutEndsTheBatchEarly(t *testing.T) {
	store := newFakeStore(eventRow(1), eventRow(2))
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	pub := &fakePub{script: func(int, context.Context, relay.Message) (relay.Outcome, error) {
		now = now.Add(8 * time.Second) // the first publish was slow
		return relay.Confirmed, nil
	}}
	r := newRelay(store, pub)
	r.Lease = 10 * time.Second
	r.PublishTimeout = 3 * time.Second
	r.Now = func() time.Time { return now }
	if err := runCycles(t, r, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if got := pub.ids(); !slices.Equal(got, []string{"evt1"}) {
		t.Fatalf("published %v: a publish that could outlive the lease must not start", got)
	}
	_, releases := store.snapshot()
	if len(releases) != 1 || !slices.Equal(releases[0].IDs, []string{"row2"}) || releases[0].Delay != 0 {
		t.Fatalf("releases = %+v, want row2 back at once", releases)
	}
	st := mustStats(t, r)
	if st.LeaseExpiring != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestInvalidRowIsReleasedLongAndDoesNotBlockOthers(t *testing.T) {
	bad := eventRow(1)
	bad.Body = []byte(`{"schemaVersion":1}`) // no eventID
	badSchema := eventRow(2)
	badSchema.Headers = map[string]string{}
	badSchema.Body = []byte(`{"eventID":"evt2","generation":"g1"}`) // no schema anywhere
	notJSON := eventRow(3)
	notJSON.Body = []byte(`nope`)
	store := newFakeStore(bad, badSchema, notJSON, eventRow(4))
	pub := &fakePub{}
	r := newRelay(store, pub)
	r.RetryMax = time.Minute
	if err := runCycles(t, r, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if got := pub.ids(); !slices.Equal(got, []string{"evt4"}) {
		t.Fatalf("published %v", got)
	}
	_, releases := store.snapshot()
	if len(releases) != 3 {
		t.Fatalf("releases = %+v", releases)
	}
	for _, rel := range releases {
		if rel.Delay != time.Minute || len(rel.IDs) != 1 {
			t.Fatalf("release = %+v, want one row held for RetryMax", rel)
		}
	}
	st := mustStats(t, r)
	if st.Invalid != 3 || st.Published != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestCancelDuringPublishReleasesTheBatchAtOnce(t *testing.T) {
	store := newFakeStore(eventRow(1), eventRow(2))
	started := make(chan struct{})
	pub := &fakePub{script: func(_ int, ctx context.Context, _ relay.Message) (relay.Outcome, error) {
		close(started)
		<-ctx.Done()
		return relay.Unknown, ctx.Err()
	}}
	r := newRelay(store, pub)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	marks, releases := store.snapshot()
	if len(marks) != 0 || len(releases) != 1 || !slices.Equal(releases[0].IDs, []string{"row1", "row2"}) || releases[0].Delay != 0 {
		t.Fatalf("marks=%+v releases=%+v: shutdown must hand the claimed rows back at once", marks, releases)
	}
	st := mustStats(t, r)
	if st.Unknown != 0 {
		t.Fatalf("a publish interrupted by shutdown is not a broker failure: %+v", st)
	}
}

func TestConfirmedRowIsMarkedEvenWhenShutdownFollows(t *testing.T) {
	store := newFakeStore(eventRow(1), eventRow(2))
	ctx, cancel := context.WithCancel(t.Context())
	pub := &fakePub{script: func(call int, _ context.Context, _ relay.Message) (relay.Outcome, error) {
		if call == 1 {
			cancel() // shutdown arrives right after the first confirm
		}
		return relay.Confirmed, nil
	}}
	r := newRelay(store, pub)
	if err := r.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	marks, releases := store.snapshot()
	if len(marks) != 1 || marks[0].IDs[0] != "row1" {
		t.Fatalf("marks = %+v: a confirmed message must be recorded despite the shutdown", marks)
	}
	if len(releases) != 1 || !slices.Equal(releases[0].IDs, []string{"row2"}) || releases[0].Delay != 0 {
		t.Fatalf("releases = %+v", releases)
	}
}

func TestDrainedBatchContinuesWithoutWaiting(t *testing.T) {
	store := newFakeStore(eventRow(1), eventRow(2), eventRow(3))
	pub := &fakePub{}
	r := newRelay(store, pub)
	r.Batch = 2
	if err := runCycles(t, r, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if got := pub.ids(); len(got) != 3 {
		t.Fatalf("published %v: a full batch means more may wait, so the next claim must not sleep", got)
	}
}

func TestClaimErrorIsCountedAndRetriedAfterAWait(t *testing.T) {
	store := newFakeStore(eventRow(1))
	store.claimErr = errors.New("database down")
	pub := &fakePub{}
	r := newRelay(store, pub)
	var waits []time.Duration
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.Sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		switch len(waits) {
		case 2:
			store.mu.Lock()
			store.claimErr = nil
			store.mu.Unlock()
		case 4:
			cancel()
		}
		return ctx.Err()
	}
	if err := r.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if len(waits) < 4 || waits[1] <= waits[0] {
		t.Fatalf("waits = %v: consecutive store errors must back off", waits)
	}
	if got := pub.ids(); !slices.Equal(got, []string{"evt1"}) {
		t.Fatalf("published %v after the store recovered", got)
	}
	st := mustStats(t, r)
	if st.StoreErrors != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

// After Nacked or Unknown the relay waits before it claims again, with a pause
// that grows up to 5 s, so a dead broker costs no claim storm and later events
// of an aggregate do not overtake the held-back one. A confirmed batch resets it.
func TestBrokerFailuresBackOffUpToFiveSeconds(t *testing.T) {
	for _, out := range []relay.Outcome{relay.Nacked, relay.Unknown} {
		t.Run(out.String(), func(t *testing.T) {
			store := newFakeStore(eventRow(1))
			var healthy atomic.Bool
			pub := &fakePub{script: func(int, context.Context, relay.Message) (relay.Outcome, error) {
				if healthy.Load() {
					return relay.Confirmed, nil
				}
				return out, errors.New("broker down")
			}}
			r := newRelay(store, pub)
			var waits []time.Duration
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r.Sleep = func(ctx context.Context, d time.Duration) error { // fake clock: no real wait
				waits = append(waits, d)
				switch len(waits) {
				case 9:
					healthy.Store(true)
				case 12:
					cancel()
				}
				return ctx.Err()
			}
			if err := r.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("Run = %v", err)
			}
			want := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond,
				1600 * time.Millisecond, 3200 * time.Millisecond, 5 * time.Second, 5 * time.Second, 5 * time.Second}
			if len(waits) < 12 || !slices.Equal(waits[:len(want)], want) {
				t.Fatalf("waits = %v, want the start %v", waits, want)
			}
			// After the broker came back, the next waits are the plain poll again.
			if last := waits[len(waits)-1]; last != r.Poll && last != relay.DefaultPoll {
				t.Fatalf("pause after recovery = %v, want the poll", last)
			}
		})
	}
}

// An empty claim between two broker failures does not reset the pause: only a
// confirmed row does.
func TestEmptyClaimDoesNotResetTheBrokerPause(t *testing.T) {
	store := newFakeStore(eventRow(1))
	pub := &fakePub{script: func(int, context.Context, relay.Message) (relay.Outcome, error) {
		return relay.Nacked, nil
	}}
	r := newRelay(store, pub)
	var waits []time.Duration
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.Sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		switch len(waits) {
		case 1:
			store.mu.Lock()
			store.emptyClaims = 1 // the next cycle claims nothing
			store.mu.Unlock()
		case 3:
			cancel()
		}
		return ctx.Err()
	}
	if err := r.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	// nack (200 ms), empty claim (plain poll), nack again: the pause must be longer than after the first nack.
	if len(waits) < 3 || waits[2] <= waits[0] {
		t.Fatalf("waits = %v: the second broker failure must wait longer than the first", waits)
	}
}

func TestStatsMergeBacklogAndCounters(t *testing.T) {
	store := newFakeStore()
	store.backlog = relay.Backlog{Pending: 7, OldestAge: 3 * time.Second}
	r := newRelay(store, &fakePub{})
	st, err := r.Stats(t.Context())
	if err != nil || st.Pending != 7 || st.OldestAge != 3*time.Second {
		t.Fatalf("stats = %+v, %v", st, err)
	}
	boom := errors.New("stats failed")
	store.statsErr = boom
	if _, err := r.Stats(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestSecondRunIsRefused(t *testing.T) {
	store := newFakeStore()
	r := newRelay(store, &fakePub{})
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{})
	var once atomic.Bool
	r.Sleep = func(ctx context.Context, _ time.Duration) error {
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	<-entered
	if err := r.Run(t.Context()); !errors.Is(err, relay.ErrRunning) {
		t.Fatalf("second Run = %v, want ErrRunning", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run = %v", err)
	}
}

func TestRunValidatesItsConfiguration(t *testing.T) {
	for name, mutate := range map[string]func(*relay.Relay){
		"no store": func(r *relay.Relay) { r.Store = nil },
		"no pub":   func(r *relay.Relay) { r.Pub = nil },
		"no owner": func(r *relay.Relay) { r.Owner = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRelay(newFakeStore(), &fakePub{})
			mutate(r)
			if err := r.Run(t.Context()); err == nil || errors.Is(err, context.Canceled) {
				t.Fatalf("Run = %v, want a configuration error", err)
			}
		})
	}
}

func TestNotifyWakesTheWait(t *testing.T) {
	store := newFakeStore()
	pub := &fakePub{}
	r := newRelay(store, pub)
	r.Poll = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	waitFor(t, "the first claim", func() bool { store.mu.Lock(); defer store.mu.Unlock(); return store.claims >= 1 })

	store.mu.Lock()
	store.rows = append(store.rows, eventRow(1))
	store.mu.Unlock()
	r.Notify()
	r.Notify() // coalesced, never blocks
	waitFor(t, "the notified publish", func() bool { return len(pub.messages()) == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}

func TestNotifyBeforeRunIsNotLost(t *testing.T) {
	store := newFakeStore(eventRow(1))
	pub := &fakePub{}
	r := newRelay(store, pub)
	r.Poll = time.Hour
	r.Notify()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	waitFor(t, "the publish", func() bool { return len(pub.messages()) == 1 })
	cancel()
	<-done
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDefaultWaitStopsOnCancel(t *testing.T) {
	r := newRelay(newFakeStore(), &fakePub{})
	r.Poll = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func mustStats(t *testing.T, r *relay.Relay) relay.Stats {
	t.Helper()
	st, err := r.Stats(t.Context())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	return st
}
