//go:build integration

package relay_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/JustIkra/umir-factory/reference/durable/itest"
	"github.com/JustIkra/umir-factory/reference/durable/postgres"
	"github.com/JustIkra/umir-factory/reference/durable/rabbitmq"
	"github.com/JustIkra/umir-factory/reference/durable/relay"
)

const (
	waitLong      = 30 * time.Second
	testRetryTTL  = 300 * time.Millisecond
	testGen       = "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10"
	eventsQueue   = "t.events"
	consumerName  = "t-consumer"
	testLeaseTerm = 30 * time.Second
)

var errDead = errors.New("process is dead")

// ---- adapters from the reference packages to the relay interfaces ----------

// pgStore adapts postgres.Outbox to relay.OutboxStore. A service writes the
// same few lines in its composition root.
type pgStore struct{ ob *postgres.Outbox }

func mapLease(err error) error {
	if errors.Is(err, postgres.ErrLeaseLost) {
		return fmt.Errorf("%w: %w", relay.ErrLeaseLost, err)
	}
	return err
}

func (s pgStore) Claim(ctx context.Context, owner string, limit int, lease time.Duration) ([]relay.Row, int64, error) {
	rows, fence, err := s.ob.Claim(ctx, owner, limit, lease)
	if err != nil {
		return nil, 0, err
	}
	out := make([]relay.Row, len(rows))
	for i, r := range rows {
		out[i] = relay.Row(r)
	}
	return out, fence, nil
}

func (s pgStore) MarkPublished(ctx context.Context, owner string, fence int64, ids []string, routed bool) error {
	return mapLease(s.ob.MarkPublished(ctx, owner, fence, ids, routed))
}

func (s pgStore) Release(ctx context.Context, owner string, fence int64, ids []string, delay time.Duration) error {
	return mapLease(s.ob.Release(ctx, owner, fence, ids, delay))
}

func (s pgStore) Stats(ctx context.Context) (relay.Backlog, error) {
	st, err := s.ob.Stats(ctx)
	return relay.Backlog(st), err
}

// deadStore models a crashed process: once dead, nothing reaches the database.
type deadStore struct {
	relay.OutboxStore
	dead atomic.Bool
}

func (s *deadStore) MarkPublished(ctx context.Context, owner string, fence int64, ids []string, routed bool) error {
	if s.dead.Load() {
		return errDead
	}
	return s.OutboxStore.MarkPublished(ctx, owner, fence, ids, routed)
}

func (s *deadStore) Release(ctx context.Context, owner string, fence int64, ids []string, delay time.Duration) error {
	if s.dead.Load() {
		return errDead
	}
	return s.OutboxStore.Release(ctx, owner, fence, ids, delay)
}

// mqPub adapts rabbitmq.Publisher to relay.Publisher.
type mqPub struct{ p *rabbitmq.Publisher }

func (a mqPub) Publish(ctx context.Context, m relay.Message) (relay.Outcome, error) {
	out, err := a.p.Publish(ctx, rabbitmq.Message(m))
	return relay.Outcome(out), err
}

// hookPub lets a test run code around the real publish. do performs it.
type hookPub struct {
	inner relay.Publisher
	calls atomic.Int32
	hook  func(n int, m relay.Message, do func() (relay.Outcome, error)) (relay.Outcome, error)
}

func (p *hookPub) Publish(ctx context.Context, m relay.Message) (relay.Outcome, error) {
	n := int(p.calls.Add(1))
	do := func() (relay.Outcome, error) { return p.inner.Publish(ctx, m) }
	if p.hook == nil {
		return do()
	}
	return p.hook(n, m, do)
}

// ---- environment ------------------------------------------------------------

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type options struct {
	fakeClock    bool
	noEventQueue bool // leave umir.events without a bound queue
}

type harness struct {
	t       *testing.T
	dbURL   string
	db      *postgres.DB
	clock   *fakeClock
	ob      *postgres.Outbox
	in      *postgres.Inbox
	amqpURL string
}

func migrationSpec() postgres.Spec {
	return postgres.Spec{
		Service: "relaytest",
		Dir:     "migrations",
		FS: fstest.MapFS{
			"migrations/0001_base.sql": {Data: []byte(`-- umir:include storage_meta
-- umir:include worker_leases
-- umir:include outbox
-- umir:include inbox
CREATE TABLE biz (key text PRIMARY KEY);
CREATE TABLE effects (seq bigserial PRIMARY KEY, event_id text NOT NULL);
`)},
		},
	}
}

func openDB(t testing.TB, rawURL string, clock *fakeClock) *postgres.DB {
	t.Helper()
	cfg := postgres.Config{URL: rawURL, ApplicationName: "t-factory-relay", MaxConns: 8, ConnectTimeout: 10 * time.Second}
	if clock != nil {
		cfg.Clock = clock.Now
	}
	db, err := postgres.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func fastTopology() rabbitmq.Topology {
	topo := rabbitmq.DefaultTopology(rabbitmq.TopologyOptions{})
	topo.RetryTTL = testRetryTTL
	return topo
}

func dialAMQP(t testing.TB, amqpURL string) *amqp.Connection {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
			t.Errorf("close connection: %v", err)
		}
	})
	return conn
}

func adminChannel(t testing.TB, amqpURL string) *amqp.Channel {
	t.Helper()
	ch, err := dialAMQP(t, amqpURL).Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	return ch
}

func newHarness(t *testing.T, opts options) *harness {
	t.Helper()
	h := &harness{t: t, dbURL: itest.NewDatabase(t), amqpURL: itest.NewVHost(t)}
	if opts.fakeClock {
		h.clock = &fakeClock{now: time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)}
	}
	h.db = openDB(t, h.dbURL, h.clock)
	if err := postgres.Migrate(t.Context(), h.db, migrationSpec()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	h.ob, h.in = postgres.NewOutbox(h.db), postgres.NewInbox(h.db)

	ch := adminChannel(t, h.amqpURL)
	if err := rabbitmq.Declare(ch, fastTopology()); err != nil {
		t.Fatalf("declare topology: %v", err)
	}
	if !opts.noEventQueue {
		if _, err := ch.QueueDeclare(eventsQueue, true, false, false, false, nil); err != nil {
			t.Fatalf("declare %s: %v", eventsQueue, err)
		}
		if err := ch.QueueBind(eventsQueue, rabbitmq.WarehouseBinding, rabbitmq.ExchangeEvents, false, nil); err != nil {
			t.Fatalf("bind %s: %v", eventsQueue, err)
		}
	}
	return h
}

func eventually(t testing.TB, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// viaProxy returns a URL like raw that goes through a new fault-injecting proxy.
func viaProxy(t testing.TB, raw, defaultPort string) (*itest.TCPProxy, string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("parse URL")
	}
	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	p := itest.NewTCPProxy(t, net.JoinHostPort(u.Hostname(), port))
	u.Host = p.Addr()
	return p, u.String()
}

// ---- broker side ------------------------------------------------------------

func (h *harness) startPublisher(amqpURL string) relay.Publisher {
	h.t.Helper()
	pub := rabbitmq.NewPublisher(&rabbitmq.Connector{
		URL: rabbitmq.BrokerURL(amqpURL), Name: "itest-relay-publisher", Heartbeat: time.Second,
		Backoff: func(int) time.Duration { return 50 * time.Millisecond },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := pub.Run(ctx); !errors.Is(err, context.Canceled) {
			h.t.Errorf("publisher Run: %v", err)
		}
	}()
	h.t.Cleanup(func() {
		cancel()
		<-done
	})
	eventually(h.t, "publisher ready", waitLong, pub.Ready)
	return mqPub{pub}
}

// recorder notes every delivery a consumer handler sees.
type recorder struct {
	mu    sync.Mutex
	ids   []string
	first map[string]time.Time
}

func newRecorder() *recorder { return &recorder{first: map[string]time.Time{}} }

func (r *recorder) add(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	if _, ok := r.first[id]; !ok {
		r.first[id] = time.Now()
	}
}

func (r *recorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ids)
}

func (r *recorder) unique() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.ids)
	slices.Sort(out)
	return slices.Compact(out)
}

func (r *recorder) firstSeen(id string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.first[id]
	return at, ok
}

// applyHandler is the consumer of the reference: Inbox.Apply records the
// eventID and inserts the effect in one transaction. hook runs at the named
// stage ("before-apply", "after-apply") and may fail the handler.
func (h *harness) applyHandler(rec *recorder, hook func(stage string, d rabbitmq.Delivery) error) rabbitmq.Handler {
	return func(ctx context.Context, d rabbitmq.Delivery) error {
		rec.add(d.MessageID)
		if hook != nil {
			if err := hook("before-apply", d); err != nil {
				return err
			}
		}
		_, err := h.in.Apply(ctx, consumerName, d.MessageID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO effects (event_id) VALUES ($1)", d.MessageID)
			return err
		})
		if err != nil {
			return err
		}
		if hook != nil {
			return hook("after-apply", d)
		}
		return nil
	}
}

type consumer struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (c *consumer) stop() {
	c.cancel()
	<-c.done
}

// startConsumer reads queue until stopped. Events queues are fan-out style (no
// retry chain); the work queue auth.mail uses the full chain.
func (h *harness) startConsumer(amqpURL, queue string, handler rabbitmq.Handler) *consumer {
	h.t.Helper()
	var opts []rabbitmq.ConsumeOption
	if queue == eventsQueue {
		opts = append(opts, rabbitmq.WithFanout())
	}
	c := &rabbitmq.Connector{
		URL: rabbitmq.BrokerURL(amqpURL), Name: "itest-relay-consumer", Heartbeat: time.Second,
		Backoff: func(int) time.Duration { return 50 * time.Millisecond },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cons := &consumer{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(cons.done)
		if err := c.Consume(ctx, queue, 4, handler, opts...); !errors.Is(err, context.Canceled) {
			h.t.Errorf("Consume: %v", err)
		}
	}()
	h.t.Cleanup(cons.stop)
	return cons
}

// ---- relay side -------------------------------------------------------------

func (h *harness) newRelay(owner string, store relay.OutboxStore, pub relay.Publisher) *relay.Relay {
	return &relay.Relay{
		Store: store, Pub: pub, Owner: owner,
		Batch: 10, Lease: testLeaseTerm, Poll: 50 * time.Millisecond,
		RetryBase: 50 * time.Millisecond, RetryMax: 200 * time.Millisecond,
	}
}

type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (x *runner) stop() {
	x.cancel()
	<-x.done
}

func (h *harness) run(r *relay.Relay) *runner {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	x := &runner{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(x.done)
		if err := r.Run(ctx); !errors.Is(err, context.Canceled) {
			h.t.Errorf("relay Run: %v", err)
		}
	}()
	h.t.Cleanup(x.stop)
	return x
}

// ---- data -------------------------------------------------------------------

func eventFor(key string) postgres.Event {
	return postgres.Event{
		Exchange:   rabbitmq.ExchangeEvents,
		RoutingKey: "warehouse.OrderPlaced",
		Headers: map[string]string{
			"x-umir-schema": "1", "x-umir-restaurant-id": "r1", "x-umir-generation": testGen,
		},
		Body:         fmt.Appendf(nil, `{"eventID":%q,"schemaVersion":1,"generation":%q}`, key, testGen),
		OrgID:        "o1",
		RestaurantID: "r1",
	}
}

func taskFor(key string) postgres.Event {
	return postgres.Event{
		Exchange:   rabbitmq.ExchangeTasks,
		RoutingKey: rabbitmq.QueueAuthMail,
		Headers:    map[string]string{"x-umir-schema": "1", "x-umir-generation": testGen},
		Body:       fmt.Appendf(nil, `{"messageID":%q,"schemaVersion":1,"generation":%q}`, key, testGen),
	}
}

// commit writes the business row and its event in one transaction of db.
func commit(ctx context.Context, db *postgres.DB, ob *postgres.Outbox, key string, ev postgres.Event) error {
	return db.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO biz (key) VALUES ($1)", key); err != nil {
			return err
		}
		_, err := ob.AddEvent(ctx, tx, ev)
		return err
	})
}

func (h *harness) commit(key string) {
	h.t.Helper()
	if err := commit(h.t.Context(), h.db, h.ob, key, eventFor(key)); err != nil {
		h.t.Fatalf("commit %s: %v", key, err)
	}
}

func (h *harness) query(sql string, args ...any) []string {
	h.t.Helper()
	var out []string
	err := h.db.InTx(h.t.Context(), pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		out = nil
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		h.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (h *harness) count(sql string, args ...any) int {
	h.t.Helper()
	var n int
	err := h.db.InTx(h.t.Context(), pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(&n)
	})
	if err != nil {
		h.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// effectCounts maps eventID to how often its effect was applied.
func (h *harness) effectCounts() map[string]int {
	h.t.Helper()
	out := map[string]int{}
	for _, id := range h.query("SELECT event_id FROM effects") {
		out[id]++
	}
	return out
}

func (h *harness) assertEffectsOnce(keys ...string) {
	h.t.Helper()
	got := h.effectCounts()
	if len(got) != len(keys) {
		h.t.Fatalf("effects = %v, want exactly one for each of %v", got, keys)
	}
	for _, k := range keys {
		if got[k] != 1 {
			h.t.Fatalf("effects = %v, want one effect for %s", got, k)
		}
	}
}

func (h *harness) pendingRows() int {
	h.t.Helper()
	return h.count("SELECT count(*) FROM outbox WHERE published_at IS NULL")
}

func relayStats(t testing.TB, r *relay.Relay) relay.Stats {
	t.Helper()
	st, err := r.Stats(t.Context())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	return st
}
