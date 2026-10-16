//go:build integration

package relay_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/JustIkra/umir-factory/reference/durable/itest"
	"github.com/JustIkra/umir-factory/reference/durable/postgres"
	"github.com/JustIkra/umir-factory/reference/durable/rabbitmq"
	"github.com/JustIkra/umir-factory/reference/durable/relay"
)

var (
	// basic.ack (class 60, method 80) as it appears on the wire in both directions.
	basicAck = []byte{0x00, 0x3C, 0x00, 0x50}
)

func TestFlowFromTransactionToInbox(t *testing.T) {
	h := newHarness(t, options{})
	rec := newRecorder()
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	r := h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL))
	h.run(r)

	keys := []string{"e1", "e2", "e3", "e4", "e5"}
	for _, k := range keys {
		h.commit(k)
	}
	eventually(t, "all effects", waitLong, func() bool { return len(h.effectCounts()) == len(keys) })
	h.assertEffectsOnce(keys...)
	if got := rec.total(); got != len(keys) {
		t.Fatalf("deliveries = %d, want %d (no failures, no duplicates)", got, len(keys))
	}
	eventually(t, "outbox published", waitLong, func() bool { return h.pendingRows() == 0 })
	if n := h.count("SELECT count(*) FROM outbox WHERE routed IS TRUE"); n != len(keys) {
		t.Fatalf("%d rows are marked routed, want %d", n, len(keys))
	}
	st := relayStats(t, r)
	if st.Published != int64(len(keys)) || st.Pending != 0 || st.ReturnedAccepted != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestPublishLatencyStaysUnderOneSecond(t *testing.T) {
	h := newHarness(t, options{})
	rec := newRecorder()
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	r := h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL))
	r.Poll = relay.DefaultPoll // the production default
	h.run(r)

	var delays []time.Duration
	for i := range 20 {
		key := fmt.Sprintf("lat-%d", i)
		start := time.Now()
		h.commit(key)
		eventually(t, "delivery of "+key, 10*time.Second, func() bool { _, ok := rec.firstSeen(key); return ok })
		at, _ := rec.firstSeen(key)
		delays = append(delays, at.Sub(start))
	}
	sort.Slice(delays, func(i, j int) bool { return delays[i] < delays[j] })
	p95 := delays[(len(delays)*95+99)/100-1]
	if p95 > time.Second {
		t.Fatalf("p95 delay = %v (all: %v), want at most 1 s (C48)", p95, delays)
	}
}

func TestRolledBackTransactionPublishesNothing(t *testing.T) {
	h := newHarness(t, options{})
	rec := newRecorder()
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	r := h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL))
	h.run(r)

	boom := errors.New("business failure after the event was added")
	err := h.db.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO biz (key) VALUES ('lost')"); err != nil {
			return err
		}
		if _, err := h.ob.AddEvent(ctx, tx, eventFor("lost")); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	h.commit("kept")
	eventually(t, "the kept effect", waitLong, func() bool { return len(h.effectCounts()) == 1 })
	h.assertEffectsOnce("kept")
	if got := rec.unique(); !slices.Equal(got, []string{"kept"}) {
		t.Fatalf("delivered %v: the rolled back event must never reach the broker", got)
	}
}

// A transaction cut by the network: before COMMIT it leaves nothing, after
// COMMIT the caller cannot tell. Either way the retry logic of the service
// (look at the business row, repeat only when it is missing) ends with one event.
func TestTransactionFaultsEndWithOneEffect(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		applied bool // is the business row there after the cut?
	}{
		{"cut before COMMIT", "INSERT INTO outbox", false},
		{"cut right after COMMIT", "commit\x00", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, options{})
			rec := newRecorder()
			h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
			h.run(h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL)))

			proxy, proxiedURL := viaProxy(t, h.dbURL, "5432")
			proxied := openDB(t, proxiedURL, nil)
			proxy.CutAfterMatch(itest.ToServer, []byte(tc.pattern))
			err := commit(t.Context(), proxied, postgres.NewOutbox(proxied), "order-1", eventFor("order-1"))
			if err == nil {
				t.Fatal("the cut must fail the transaction")
			}
			var unknown *postgres.CommitUnknownError
			if errors.As(err, &unknown) != tc.applied {
				t.Fatalf("err = %v; CommitUnknownError expected exactly when the cut came after COMMIT", err)
			}
			if tc.applied {
				eventually(t, "the committed row", waitLong, func() bool { return h.count("SELECT count(*) FROM biz") == 1 })
			} else if n := h.count("SELECT count(*) FROM biz"); n != 0 {
				t.Fatalf("biz rows = %d after a cut before COMMIT", n)
			}
			// The service looks at its own row and repeats only when it is missing.
			if h.count("SELECT count(*) FROM biz WHERE key = 'order-1'") == 0 {
				h.commit("order-1")
			}
			eventually(t, "the effect", waitLong, func() bool { return len(h.effectCounts()) == 1 })
			h.assertEffectsOnce("order-1")
			eventually(t, "outbox published", waitLong, func() bool { return h.pendingRows() == 0 })
			if got := h.count("SELECT count(*) FROM outbox"); got != 1 {
				t.Fatalf("outbox rows = %d, want 1", got)
			}
		})
	}
}

// A relay that dies before publishing leaves its rows claimed; the lease runs
// out and another relay publishes them.
func TestRelayCrashBeforePublishLosesNothing(t *testing.T) {
	h := newHarness(t, options{fakeClock: true})
	rec := newRecorder()
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	h.commit("e1")
	h.commit("e2")

	storeA := &deadStore{OutboxStore: pgStore{h.ob}}
	pubA := &hookPub{inner: h.startPublisher(h.amqpURL)}
	a := h.newRelay("relay-a", storeA, pubA)
	ctxA, cancelA := context.WithCancel(t.Context())
	pubA.hook = func(_ int, _ relay.Message, _ func() (relay.Outcome, error)) (relay.Outcome, error) {
		storeA.dead.Store(true) // the process dies right here
		cancelA()
		return relay.Unknown, errDead
	}
	if err := a.Run(ctxA); !errors.Is(err, context.Canceled) {
		t.Fatalf("relay A Run = %v", err)
	}
	if rec.total() != 0 || h.pendingRows() != 2 {
		t.Fatalf("deliveries=%d pending=%d: nothing was published", rec.total(), h.pendingRows())
	}
	h.clock.Advance(testLeaseTerm + time.Second)

	h.run(h.newRelay("relay-b", pgStore{h.ob}, h.startPublisher(h.amqpURL)))
	eventually(t, "effects after the takeover", waitLong, func() bool { return len(h.effectCounts()) == 2 })
	h.assertEffectsOnce("e1", "e2")
	if got := rec.total(); got != 2 {
		t.Fatalf("deliveries = %d, want 2", got)
	}
}

// A relay that dies after the broker confirmed but before it recorded the row
// causes a second delivery of the same eventID; the inbox absorbs it.
func TestRelayCrashAfterConfirmRepeatsTheEventOnce(t *testing.T) {
	h := newHarness(t, options{fakeClock: true})
	rec := newRecorder()
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	h.commit("e1")

	storeA := &deadStore{OutboxStore: pgStore{h.ob}}
	pubA := &hookPub{inner: h.startPublisher(h.amqpURL)}
	a := h.newRelay("relay-a", storeA, pubA)
	ctxA, cancelA := context.WithCancel(t.Context())
	pubA.hook = func(_ int, _ relay.Message, do func() (relay.Outcome, error)) (relay.Outcome, error) {
		out, err := do() // the broker has the message and has confirmed it
		storeA.dead.Store(true)
		cancelA()
		return out, err
	}
	if err := a.Run(ctxA); !errors.Is(err, context.Canceled) {
		t.Fatalf("relay A Run = %v", err)
	}
	eventually(t, "first delivery", waitLong, func() bool { return len(h.effectCounts()) == 1 })
	if h.pendingRows() != 1 {
		t.Fatal("the row must still be unpublished: the crash came before the record")
	}
	h.clock.Advance(testLeaseTerm + time.Second)

	h.run(h.newRelay("relay-b", pgStore{h.ob}, h.startPublisher(h.amqpURL)))
	eventually(t, "repeat delivery", waitLong, func() bool { return rec.total() == 2 })
	eventually(t, "outbox published", waitLong, func() bool { return h.pendingRows() == 0 })
	h.assertEffectsOnce("e1")
}

// The confirm is lost on the wire: Unknown. The relay repeats the row with the
// same message_id and the consumer applies it once.
func TestLostConfirmIsRepeatedWithTheSameEventID(t *testing.T) {
	h := newHarness(t, options{})
	rec := newRecorder()
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	proxy, proxiedURL := viaProxy(t, h.amqpURL, "5672")
	pub := &hookPub{inner: h.startPublisher(proxiedURL)}
	pub.hook = func(n int, _ relay.Message, do func() (relay.Outcome, error)) (relay.Outcome, error) {
		if n == 1 {
			proxy.DropToClient(true)
			defer proxy.DropToClient(false)
		}
		return do()
	}
	r := h.newRelay("relay-a", pgStore{h.ob}, pub)
	r.PublishTimeout = 500 * time.Millisecond
	h.commit("e1")
	h.run(r)

	eventually(t, "repeat delivery", waitLong, func() bool { return rec.total() == 2 })
	eventually(t, "outbox published", waitLong, func() bool { return h.pendingRows() == 0 })
	h.assertEffectsOnce("e1")
	if got := rec.unique(); !slices.Equal(got, []string{"e1"}) {
		t.Fatalf("message ids = %v: a repeat must keep the eventID", got)
	}
	if st := relayStats(t, r); st.Unknown < 1 || st.Published != 1 {
		t.Fatalf("stats = %+v, want Unknown and one Published", st)
	}
}

func TestConsumerCrashBeforeApplyRedeliversAndAppliesOnce(t *testing.T) {
	h := newHarness(t, options{})
	rec := newRecorder()
	var first *consumer
	crashed := make(chan struct{})
	first = h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, func(stage string, _ rabbitmq.Delivery) error {
		if stage == "before-apply" && rec.total() == 1 {
			first.cancel() // the process dies before the effect
			close(crashed)
			return errDead
		}
		return nil
	}))
	h.run(h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL)))
	h.commit("e1")
	<-crashed
	<-first.done
	if len(h.effectCounts()) != 0 {
		t.Fatal("no effect may exist before the restart")
	}
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	eventually(t, "effect after the restart", waitLong, func() bool { return len(h.effectCounts()) == 1 })
	h.assertEffectsOnce("e1")
	if rec.total() != 2 {
		t.Fatalf("deliveries = %d, want 2", rec.total())
	}
}

func TestConsumerCrashBeforeAckRedeliversAndAppliesOnce(t *testing.T) {
	h := newHarness(t, options{})
	rec := newRecorder()
	var first *consumer
	crashed := make(chan struct{})
	first = h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, func(stage string, _ rabbitmq.Delivery) error {
		if stage == "after-apply" && rec.total() == 1 {
			first.cancel() // the process dies after COMMIT, before the ack
			close(crashed)
			return errDead
		}
		return nil
	}))
	h.run(h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL)))
	h.commit("e1")
	<-crashed
	<-first.done
	h.assertEffectsOnce("e1")
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	eventually(t, "redelivery", waitLong, func() bool { return rec.total() == 2 })
	eventually(t, "the queue is drained", waitLong, func() bool { return queueDepth(t, h.amqpURL, eventsQueue) == 0 })
	h.assertEffectsOnce("e1")
}

func TestLostAckRedeliversAndAppliesOnce(t *testing.T) {
	h := newHarness(t, options{})
	rec := newRecorder()
	proxy, proxiedURL := viaProxy(t, h.amqpURL, "5672")
	h.startConsumer(proxiedURL, eventsQueue, h.applyHandler(rec, nil))
	h.run(h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL)))
	proxy.CutAfterMatch(itest.ToServer, basicAck)
	h.commit("e1")

	eventually(t, "redelivery after the lost ack", waitLong, func() bool { return rec.total() == 2 })
	h.assertEffectsOnce("e1")
	eventually(t, "the queue is drained", waitLong, func() bool { return queueDepth(t, h.amqpURL, eventsQueue) == 0 })
}

func TestSameEventIDDeliveredTwiceIsAppliedOnce(t *testing.T) {
	h := newHarness(t, options{})
	rec := newRecorder()
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	r := h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL))
	// Two outbox rows, one eventID: the producer wrote the event twice.
	if err := h.db.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		for range 2 {
			if _, err := h.ob.AddEvent(ctx, tx, eventFor("same")); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.run(r)
	eventually(t, "two deliveries", waitLong, func() bool { return rec.total() == 2 })
	eventually(t, "outbox published", waitLong, func() bool { return h.pendingRows() == 0 })
	h.assertEffectsOnce("same")
}

func TestReturnedEventIsAcceptedAndCounted(t *testing.T) {
	h := newHarness(t, options{noEventQueue: true})
	r := h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL))
	for _, k := range []string{"e1", "e2", "e3"} {
		h.commit(k)
	}
	h.run(r)
	eventually(t, "rows accepted", waitLong, func() bool { return h.pendingRows() == 0 })
	if n := h.count("SELECT count(*) FROM outbox WHERE published_at IS NOT NULL AND routed IS FALSE"); n != 3 {
		t.Fatalf("%d rows are published with routed=false, want 3", n)
	}
	st := relayStats(t, r)
	if st.ReturnedAccepted != 3 || st.Published != 3 || st.ReturnedRetried != 0 || st.Pending != 0 {
		t.Fatalf("stats = %+v", st)
	}
	// An accepted row is not published again once a queue appears.
	ch := adminChannel(t, h.amqpURL)
	if _, err := ch.QueueDeclare("late", true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := ch.QueueBind("late", rabbitmq.WarehouseBinding, rabbitmq.ExchangeEvents, false, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := queueDepth(t, h.amqpURL, "late"); n != 0 {
		t.Fatalf("late queue holds %d messages", n)
	}
}

func TestReturnedTaskIsRetriedUntilTheBindingAppears(t *testing.T) {
	h := newHarness(t, options{})
	ch := adminChannel(t, h.amqpURL)
	if err := ch.QueueUnbind(rabbitmq.QueueAuthMail, rabbitmq.QueueAuthMail, rabbitmq.ExchangeTasks, nil); err != nil {
		t.Fatalf("unbind: %v", err)
	}
	r := h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL))
	if err := commit(t.Context(), h.db, h.ob, "mail-1", taskFor("mail-1")); err != nil {
		t.Fatal(err)
	}
	h.run(r)

	eventually(t, "several returns", waitLong, func() bool { return relayStats(t, r).ReturnedRetried >= 3 })
	if h.pendingRows() != 1 {
		t.Fatalf("pending rows = %d: a retry return must leave the row in the outbox", h.pendingRows())
	}
	if n := h.count("SELECT attempts FROM outbox"); n < 3 {
		t.Fatalf("attempts = %d, want a retry for every return", n)
	}
	if st := relayStats(t, r); st.ReturnedAccepted != 0 || st.Published != 0 {
		t.Fatalf("stats = %+v", st)
	}

	if err := ch.QueueBind(rabbitmq.QueueAuthMail, rabbitmq.QueueAuthMail, rabbitmq.ExchangeTasks, false, nil); err != nil {
		t.Fatalf("bind: %v", err)
	}
	eventually(t, "the task is published after the binding appeared", waitLong, func() bool { return h.pendingRows() == 0 })
	if n := h.count("SELECT count(*) FROM outbox WHERE routed IS TRUE"); n != 1 {
		t.Fatalf("routed rows = %d, want 1", n)
	}
	rec := newRecorder()
	h.startConsumer(h.amqpURL, rabbitmq.QueueAuthMail, h.applyHandler(rec, nil))
	eventually(t, "the task reaches its consumer", waitLong, func() bool { return len(h.effectCounts()) == 1 })
	h.assertEffectsOnce("mail-1")
	if rec.total() != 1 {
		t.Fatalf("deliveries = %d, want 1", rec.total())
	}
}

func TestPublishedRowsOlderThanTheLimitAreDeleted(t *testing.T) {
	h := newHarness(t, options{fakeClock: true})
	rec := newRecorder()
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	r := h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL))
	x := h.run(r)
	h.commit("e1")
	h.commit("e2")
	eventually(t, "published", waitLong, func() bool { return h.pendingRows() == 0 })
	x.stop()

	h.commit("e3") // stays unpublished: the relay is stopped
	if n, err := h.ob.DeleteOlderThan(t.Context(), time.Hour, 100); err != nil || n != 0 {
		t.Fatalf("DeleteOlderThan(1h) = %d, %v: nothing is old yet", n, err)
	}
	h.clock.Advance(2 * time.Hour)
	n, err := h.ob.DeleteOlderThan(t.Context(), time.Hour, 100)
	if err != nil || n != 2 {
		t.Fatalf("DeleteOlderThan(1h) = %d, %v, want the two published rows", n, err)
	}
	if rest := h.count("SELECT count(*) FROM outbox"); rest != 1 || h.pendingRows() != 1 {
		t.Fatalf("outbox rows = %d, pending = %d: only the unpublished row may remain", rest, h.pendingRows())
	}
}

func TestTwoRelaysNeverPublishARowTwiceUnderALiveLease(t *testing.T) {
	h := newHarness(t, options{})
	rec := newRecorder()
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	const total = 40
	var keys []string
	for i := range total {
		key := fmt.Sprintf("e%02d", i)
		keys = append(keys, key)
		h.commit(key)
	}
	ra := h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL))
	rb := h.newRelay("relay-b", pgStore{h.ob}, h.startPublisher(h.amqpURL))
	ra.Batch, rb.Batch = 3, 3
	h.run(ra)
	h.run(rb)

	eventually(t, "all effects", waitLong, func() bool { return len(h.effectCounts()) == total })
	eventually(t, "outbox published", waitLong, func() bool { return h.pendingRows() == 0 })
	time.Sleep(300 * time.Millisecond) // a duplicate would have arrived by now
	h.assertEffectsOnce(keys...)
	if got := rec.total(); got != total {
		t.Fatalf("deliveries = %d, want %d: a row was published twice", got, total)
	}
	a, b := relayStats(t, ra).Published, relayStats(t, rb).Published
	if a+b != total || a == 0 || b == 0 {
		t.Fatalf("published by A=%d, B=%d: both relays should take part and together publish %d", a, b, total)
	}
}

func TestExpiredLeaseHandsTheBatchToAnotherRelayAndStopsTheFirst(t *testing.T) {
	h := newHarness(t, options{fakeClock: true})
	rec := newRecorder()
	h.startConsumer(h.amqpURL, eventsQueue, h.applyHandler(rec, nil))
	h.commit("e1")
	h.commit("e2")
	h.commit("e3")

	gate := make(chan struct{})
	blocked := make(chan struct{})
	pubA := &hookPub{inner: h.startPublisher(h.amqpURL)}
	pubA.hook = func(n int, _ relay.Message, do func() (relay.Outcome, error)) (relay.Outcome, error) {
		if n == 1 {
			close(blocked)
			<-gate // a slow publish: the lease runs out meanwhile
		}
		return do()
	}
	a := h.newRelay("relay-a", pgStore{h.ob}, pubA)
	xa := h.run(a)
	<-blocked

	h.clock.Advance(testLeaseTerm + time.Second)
	b := h.newRelay("relay-b", pgStore{h.ob}, h.startPublisher(h.amqpURL))
	h.run(b)
	eventually(t, "relay B published the batch", waitLong, func() bool { return h.pendingRows() == 0 })

	close(gate) // A's slow publish goes out, then A learns it lost the lease
	eventually(t, "relay A notices", waitLong, func() bool { return relayStats(t, a).LeaseLost == 1 })
	xa.stop()
	if got := pubA.calls.Load(); got != 1 {
		t.Fatalf("relay A made %d publishes: it must stop the batch at the lost lease", got)
	}
	h.assertEffectsOnce("e1", "e2", "e3")
	eventually(t, "the late duplicate arrives", waitLong, func() bool { return rec.total() == 4 })
	h.assertEffectsOnce("e1", "e2", "e3")
	if st := relayStats(t, b); st.Published != 3 {
		t.Fatalf("relay B stats = %+v", st)
	}
	if st := relayStats(t, a); st.Published != 0 {
		t.Fatalf("relay A recorded %d rows although it lost the lease", st.Published)
	}
}

func TestStatsReportBacklogAndAge(t *testing.T) {
	h := newHarness(t, options{fakeClock: true})
	r := h.newRelay("relay-a", pgStore{h.ob}, h.startPublisher(h.amqpURL))
	st := relayStats(t, r)
	if st.Pending != 0 || st.OldestAge != 0 {
		t.Fatalf("empty outbox stats = %+v", st)
	}
	h.commit("e1")
	h.clock.Advance(90 * time.Second)
	h.commit("e2")
	st = relayStats(t, r)
	if st.Pending != 2 || st.OldestAge != 90*time.Second {
		t.Fatalf("stats = %+v, want 2 pending and age 90s", st)
	}
}

// queueDepth returns the ready messages of a queue.
func queueDepth(t testing.TB, amqpURL, name string) int {
	t.Helper()
	conn := dialAMQP(t, amqpURL)
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	q, err := ch.QueueDeclarePassive(name, true, false, false, false, nil)
	if err != nil {
		t.Fatalf("passive declare %s: %v", name, err)
	}
	return q.Messages
}
