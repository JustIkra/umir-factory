//go:build integration

package rabbitmq

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/JustIkra/umir-factory/reference/durable/itest"
)

const (
	waitLong  = 30 * time.Second
	testRetry = 300 * time.Millisecond
)

// safeBuffer is a log sink that tests read while the code under test writes.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testLogger(sink *safeBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func fastBackoff(int) time.Duration { return 50 * time.Millisecond }

func fastTopology(opts TopologyOptions) Topology {
	topo := DefaultTopology(opts)
	topo.RetryTTL = testRetry
	return topo
}

func dialDirect(t *testing.T, amqpURL string) *amqp.Connection {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
			t.Errorf("close connection: %v", err)
		}
	})
	return conn
}

func openChannel(t *testing.T, conn *amqp.Connection) *amqp.Channel {
	t.Helper()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	return ch
}

// adminChannel opens a fresh channel on its own connection, so channel
// exceptions in a test step do not disturb the next one.
func adminChannel(t *testing.T, amqpURL string) *amqp.Channel {
	t.Helper()
	return openChannel(t, dialDirect(t, amqpURL))
}

func viaProxy(t *testing.T, amqpURL string, p *itest.TCPProxy) string {
	t.Helper()
	u, err := url.Parse(amqpURL)
	if err != nil {
		t.Fatalf("parse URL")
	}
	u.Host = p.Addr()
	return u.String()
}

func proxyFor(t *testing.T, amqpURL string) (*itest.TCPProxy, string) {
	t.Helper()
	u, err := url.Parse(amqpURL)
	if err != nil {
		t.Fatalf("parse URL")
	}
	p := itest.NewTCPProxy(t, net.JoinHostPort(u.Hostname(), u.Port()))
	return p, viaProxy(t, amqpURL, p)
}

func eventually(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// queueMessages returns the ready-message count of a queue, or -1 if it does
// not exist. It uses its own channel because a failed passive declare closes it.
func queueMessages(t *testing.T, amqpURL, name string) int {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
			t.Errorf("close: %v", err)
		}
	}()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	q, err := ch.QueueDeclarePassive(name, true, false, false, false, nil)
	if err != nil {
		var ae *amqp.Error
		if errors.As(err, &ae) && ae.Code == amqp.NotFound {
			return -1
		}
		t.Fatalf("passive declare %s: %v", name, err)
	}
	return q.Messages
}

func declareTopology(t *testing.T, amqpURL string, topo Topology) {
	t.Helper()
	if err := Declare(adminChannel(t, amqpURL), topo); err != nil {
		t.Fatalf("Declare: %v", err)
	}
}

func startPublisher(t *testing.T, amqpURL string, setup func(*amqp.Channel) error) (*Publisher, *safeBuffer) {
	t.Helper()
	logs := &safeBuffer{}
	var opts []PublisherOption
	if setup != nil {
		opts = append(opts, WithPublisherSetup(setup))
	}
	pub := NewPublisher(&Connector{URL: BrokerURL(amqpURL), Name: "itest-publisher", Heartbeat: time.Second, Backoff: fastBackoff, Logger: testLogger(logs)}, opts...)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := pub.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("publisher Run: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	eventually(t, "publisher ready", waitLong, pub.Ready)
	return pub, logs
}

func publishCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	t.Cleanup(cancel)
	return ctx
}

func taskMessage(id string) Message {
	return Message{
		Exchange: "umir.tasks", RoutingKey: "auth.mail", MessageID: id, Type: "auth.mail",
		Schema: 1, Generation: "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10", Body: []byte(`{"messageID":"` + id + `"}`),
	}
}

func eventMessage(id string) Message {
	return Message{
		Exchange: "umir.events", RoutingKey: "warehouse.OrderPlaced", MessageID: id, Type: "warehouse.OrderPlaced",
		Schema: 1, RestaurantID: "rest-1", Generation: "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10", Body: []byte(`{"eventID":"` + id + `"}`),
	}
}

func TestDeclareIsIdempotentAndFindsMismatch(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	topo := fastTopology(TopologyOptions{Feedback: true})
	ch := adminChannel(t, amqpURL)
	if err := Declare(ch, topo); err != nil {
		t.Fatalf("first Declare: %v", err)
	}
	if err := Declare(ch, topo); err != nil {
		t.Fatalf("second Declare must change nothing and succeed: %v", err)
	}
	for _, name := range []string{"auth.mail", "auth.mail.retry", "auth.mail.dlq", "feedback.smtp", "feedback.telegram.dlq", "feedback.auth", "feedback.auth.retry"} {
		if queueMessages(t, amqpURL, name) != 0 {
			t.Fatalf("queue %s must exist and be empty", name)
		}
	}

	different := topo
	different.RetryTTL = 2 * testRetry
	err := Declare(adminChannel(t, amqpURL), different)
	if err == nil {
		t.Fatal("a different retry TTL must be a mismatch error")
	}
	if !errors.Is(err, ErrTopologyMismatch) {
		t.Fatalf("want ErrTopologyMismatch, got %v", err)
	}

	different = topo
	different.DeliveryLimit = 7
	if err := Declare(adminChannel(t, amqpURL), different); !errors.Is(err, ErrTopologyMismatch) {
		t.Fatalf("a different delivery limit must be a mismatch, got %v", err)
	}
	// The existing queue is untouched by the failed declarations.
	if err := Declare(adminChannel(t, amqpURL), topo); err != nil {
		t.Fatalf("original topology must still declare: %v", err)
	}
}

func TestDeclareDetectsExchangeMismatch(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	ch := adminChannel(t, amqpURL)
	if err := ch.ExchangeDeclare("umir.tasks", "topic", true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	err := Declare(adminChannel(t, amqpURL), fastTopology(TopologyOptions{}))
	if !errors.Is(err, ErrTopologyMismatch) {
		t.Fatalf("want ErrTopologyMismatch, got %v", err)
	}
}

func TestCheckPassive(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	topo := fastTopology(TopologyOptions{})
	if err := CheckPassive(adminChannel(t, amqpURL), topo); !errors.Is(err, ErrTopologyMissing) {
		t.Fatalf("empty broker must report ErrTopologyMissing, got %v", err)
	}
	declareTopology(t, amqpURL, topo)
	if err := CheckPassive(adminChannel(t, amqpURL), topo); err != nil {
		t.Fatalf("declared topology must pass: %v", err)
	}
	bigger := DefaultTopology(TopologyOptions{Feedback: true})
	if err := CheckPassive(adminChannel(t, amqpURL), bigger); !errors.Is(err, ErrTopologyMissing) {
		t.Fatalf("missing feedback.auth must be reported, got %v", err)
	}
	ch := adminChannel(t, amqpURL)
	if _, err := ch.QueueDelete("feedback.telegram.dlq", false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := CheckPassive(adminChannel(t, amqpURL), topo); !errors.Is(err, ErrTopologyMissing) {
		t.Fatalf("a deleted .dlq must be reported, got %v", err)
	}
}

func TestPublishConfirmedCarriesContractProperties(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	pub, _ := startPublisher(t, amqpURL, nil)
	msg := taskMessage("task-1")
	out, err := pub.Publish(publishCtx(t, waitLong), msg)
	if err != nil || out != Confirmed {
		t.Fatalf("Publish: %v, %v", out, err)
	}
	ch := adminChannel(t, amqpURL)
	d, ok, err := ch.Get("auth.mail", true)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if d.MessageId != "task-1" || d.Type != "auth.mail" || d.ContentType != "application/json" || d.DeliveryMode != amqp.Persistent {
		t.Fatalf("properties: id=%q type=%q ct=%q mode=%d", d.MessageId, d.Type, d.ContentType, d.DeliveryMode)
	}
	if v, ok := d.Headers["x-umir-schema"]; !ok || fmt.Sprint(v) != "1" {
		t.Fatalf("x-umir-schema: %v", d.Headers["x-umir-schema"])
	}
	if d.Headers["x-umir-generation"] != msg.Generation {
		t.Fatalf("x-umir-generation: %v", d.Headers["x-umir-generation"])
	}
	if _, has := d.Headers["x-umir-restaurant-id"]; has {
		t.Fatal("a task message has no restaurant header")
	}
	if !bytes.Equal(d.Body, msg.Body) {
		t.Fatal("body changed")
	}

	if _, err := ch.QueueDeclare("probe", true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := ch.QueueBind("probe", "warehouse.#", "umir.events", false, nil); err != nil {
		t.Fatal(err)
	}
	out, err = pub.Publish(publishCtx(t, waitLong), eventMessage("evt-1"))
	if err != nil || out != Confirmed {
		t.Fatalf("event Publish: %v, %v", out, err)
	}
	d, ok, err = ch.Get("probe", true)
	if err != nil || !ok {
		t.Fatalf("Get event: ok=%v err=%v", ok, err)
	}
	if d.Headers["x-umir-restaurant-id"] != "rest-1" {
		t.Fatalf("x-umir-restaurant-id: %v", d.Headers["x-umir-restaurant-id"])
	}
}

func TestPublishRejectsInvalidMessage(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	pub, _ := startPublisher(t, amqpURL, nil)
	for name, mutate := range map[string]func(*Message){
		"no id":       func(m *Message) { m.MessageID = "" },
		"no exchange": func(m *Message) { m.Exchange = "" },
		"no key":      func(m *Message) { m.RoutingKey = "" },
		"no type":     func(m *Message) { m.Type = "" },
		"no schema":   func(m *Message) { m.Schema = 0 },
	} {
		msg := taskMessage("x")
		mutate(&msg)
		out, err := pub.Publish(publishCtx(t, waitLong), msg)
		if out == Confirmed || !errors.Is(err, ErrInvalidMessage) {
			t.Errorf("%s: outcome %v err %v", name, out, err)
		}
	}
}

func TestMandatoryWithoutBindingIsReturned(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	pub, _ := startPublisher(t, amqpURL, nil)
	// umir.events exists but has no queue bound.
	out, err := pub.Publish(publishCtx(t, waitLong), eventMessage("evt-lonely"))
	if err != nil || out != Returned {
		t.Fatalf("want Returned, got %v, %v", out, err)
	}
	// A returned message does not poison the next publish on the same session.
	out, err = pub.Publish(publishCtx(t, waitLong), taskMessage("task-after"))
	if err != nil || out != Confirmed {
		t.Fatalf("next publish: %v, %v", out, err)
	}
	// Once a queue is bound, the same event is routed and confirmed.
	ch := adminChannel(t, amqpURL)
	if _, err := ch.QueueDeclare("live", true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := ch.QueueBind("live", "warehouse.#", "umir.events", false, nil); err != nil {
		t.Fatal(err)
	}
	out, err = pub.Publish(publishCtx(t, waitLong), eventMessage("evt-lonely"))
	if err != nil || out != Confirmed {
		t.Fatalf("routed publish: %v, %v", out, err)
	}
}

func TestPublishNackedWhenQueueRejects(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	ch := adminChannel(t, amqpURL)
	if err := ch.ExchangeDeclare("umir.tasks", "direct", true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	// A classic queue enforces the limit exactly; a quorum queue only approximately.
	args := amqp.Table{"x-max-length": int64(1), "x-overflow": "reject-publish"}
	if _, err := ch.QueueDeclare("auth.mail", true, false, false, false, args); err != nil {
		t.Fatal(err)
	}
	if err := ch.QueueBind("auth.mail", "auth.mail", "umir.tasks", false, nil); err != nil {
		t.Fatal(err)
	}
	pub, _ := startPublisher(t, amqpURL, nil)
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("fits")); err != nil || out != Confirmed {
		t.Fatalf("first: %v, %v", out, err)
	}
	out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("overflow"))
	if err != nil || out != Nacked {
		t.Fatalf("want Nacked, got %v, %v", out, err)
	}
	// The session survives a nack.
	d, ok, gerr := ch.Get("auth.mail", true)
	if gerr != nil || !ok || d.MessageId != "fits" {
		t.Fatalf("drain: ok=%v err=%v", ok, gerr)
	}
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("later")); err != nil || out != Confirmed {
		t.Fatalf("after drain: %v, %v", out, err)
	}
}

func publishUntilConfirmed(t *testing.T, pub *Publisher, msg Message) {
	t.Helper()
	eventually(t, "confirmed publish after recovery", waitLong, func() bool {
		out := outcomeOf(pub.Publish(publishCtx(t, 2*time.Second), msg))
		return out == Confirmed
	})
}

func TestPublishUnknownWhenConfirmsAreLost(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	topo := fastTopology(TopologyOptions{})
	declareTopology(t, amqpURL, topo)
	proxy, proxied := proxyFor(t, amqpURL)
	pub, _ := startPublisher(t, proxied, nil)
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("warm")); err != nil || out != Confirmed {
		t.Fatalf("warm-up: %v, %v", out, err)
	}
	proxy.DropToClient(true)
	out, err := pub.Publish(publishCtx(t, time.Second), taskMessage("lost-confirm"))
	proxy.DropToClient(false)
	if out != Unknown || err == nil {
		t.Fatalf("want Unknown with an error, got %v, %v", out, err)
	}
	// The broker did receive it: Unknown means "maybe", and the sender repeats with the same ID.
	publishUntilConfirmed(t, pub, taskMessage("lost-confirm"))
	if got := queueMessages(t, amqpURL, "auth.mail"); got != 3 {
		t.Fatalf("auth.mail holds %d messages, want 3 (warm-up, lost confirm, repeat)", got)
	}
}

func TestPublishUnknownWhenConnectionDropsBeforeConfirm(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	proxy, proxied := proxyFor(t, amqpURL)
	pub, _ := startPublisher(t, proxied, nil)
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("warm")); err != nil || out != Confirmed {
		t.Fatalf("warm-up: %v, %v", out, err)
	}
	// basic.ack from the server: class 60, method 80. Cut right after it starts.
	proxy.CutAfterMatch(itest.ToClient, []byte{0x00, 0x3C, 0x00, 0x50})
	out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("cut"))
	if out != Unknown || err == nil {
		t.Fatalf("want Unknown with an error, got %v, %v", out, err)
	}
	publishUntilConfirmed(t, pub, taskMessage("cut"))
}

func TestPublisherPausedBrokerGivesUnknownThenRecovers(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	proxy, proxied := proxyFor(t, amqpURL)
	pub, _ := startPublisher(t, proxied, nil)
	proxy.Pause()
	out, err := pub.Publish(publishCtx(t, 500*time.Millisecond), taskMessage("paused"))
	if out != Unknown || err == nil {
		t.Fatalf("want Unknown with an error, got %v, %v", out, err)
	}
	proxy.Resume()
	publishUntilConfirmed(t, pub, taskMessage("paused"))
}

func TestPublisherReconnectsAndRedeclares(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	topo := fastTopology(TopologyOptions{})
	declareTopology(t, amqpURL, topo)
	proxy, proxied := proxyFor(t, amqpURL)
	var setups atomic.Int32
	pub, logs := startPublisher(t, proxied, func(ch *amqp.Channel) error {
		setups.Add(1)
		return Declare(ch, topo)
	})
	if got := setups.Load(); got != 1 {
		t.Fatalf("setups after start: %d", got)
	}
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("before")); err != nil || out != Confirmed {
		t.Fatalf("before: %v, %v", out, err)
	}
	// The broker "loses" a queue while the connection is cut; the new session declares it again.
	admin := adminChannel(t, amqpURL)
	if _, err := admin.QueueDelete("feedback.smtp.dlq", false, false, false); err != nil {
		t.Fatal(err)
	}
	proxy.CutAfterBytes(0)
	eventually(t, "second session", waitLong, func() bool { return setups.Load() >= 2 && pub.Ready() })
	if queueMessages(t, amqpURL, "feedback.smtp.dlq") != 0 {
		t.Fatal("the dead-letter queue must be declared again after reconnect")
	}
	publishUntilConfirmed(t, pub, taskMessage("after"))
	if logs.String() == "" {
		t.Fatal("reconnects must be logged")
	}
}

func TestPublisherNotReadyWithoutBroker(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	proxy, proxied := proxyFor(t, amqpURL)
	proxy.Close() // nothing listens any more
	logs := &safeBuffer{}
	pub := NewPublisher(&Connector{URL: BrokerURL(proxied), Name: "itest-down", Backoff: fastBackoff, Logger: testLogger(logs)})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := pub.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("Run: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)
	out, err := pub.Publish(publishCtx(t, time.Second), taskMessage("x"))
	cancel()
	<-done
	if out != Unknown || !errors.Is(err, ErrNotConnected) {
		t.Fatalf("want Unknown/ErrNotConnected, got %v, %v", out, err)
	}
}

// attempts records every delivery a handler sees.
type attempts struct {
	mu  sync.Mutex
	got []int
}

func (a *attempts) add(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, n)
}

func (a *attempts) snapshot() []int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]int(nil), a.got...)
}

func startConsumer(t *testing.T, amqpURL, queue string, h Handler, opts ...ConsumeOption) *safeBuffer {
	t.Helper()
	logs, _ := startStoppableConsumer(t, amqpURL, queue, h, opts...)
	return logs
}

// startStoppableConsumer is startConsumer with a stop function that ends the
// consumer and waits for it; the cleanup calls it too, so calling it twice is fine.
func startStoppableConsumer(t *testing.T, amqpURL, queue string, h Handler, opts ...ConsumeOption) (*safeBuffer, func()) {
	t.Helper()
	logs := &safeBuffer{}
	c := &Connector{URL: BrokerURL(amqpURL), Name: "itest-consumer", Heartbeat: time.Second, Backoff: fastBackoff, Logger: testLogger(logs)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := c.Consume(ctx, queue, 1, h, append([]ConsumeOption{WithCopyPause(50 * time.Millisecond)}, opts...)...); !errors.Is(err, context.Canceled) {
			t.Errorf("Consume: %v", err)
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	return logs, stop
}

func TestRetryChainEndsInDLQOnFifthAttempt(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	pub, _ := startPublisher(t, amqpURL, nil)

	const secret = "PAYLOAD-MUST-NOT-LEAK"
	var seen attempts
	logs := startConsumer(t, amqpURL, "auth.mail", func(_ context.Context, d Delivery) error {
		seen.add(d.Attempt)
		return fmt.Errorf("handler failed for %s", secret) // the error text itself must not reach the log either
	})
	msg := taskMessage("task-dead")
	msg.Body = []byte(`{"messageID":"task-dead","note":"` + secret + `"}`)
	if out, err := pub.Publish(publishCtx(t, waitLong), msg); err != nil || out != Confirmed {
		t.Fatalf("Publish: %v, %v", out, err)
	}
	eventually(t, "message in auth.mail.dlq", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail.dlq") == 1 })
	time.Sleep(3 * testRetry) // a sixth attempt would show up by now
	if got := seen.snapshot(); fmt.Sprint(got) != "[1 2 3 4 5]" {
		t.Fatalf("attempts %v, want exactly [1 2 3 4 5]", got)
	}
	for _, q := range []string{"auth.mail", "auth.mail.retry"} {
		if n := queueMessages(t, amqpURL, q); n != 0 {
			t.Fatalf("%s holds %d messages after the DLQ copy", q, n)
		}
	}
	ch := adminChannel(t, amqpURL)
	d, ok, err := ch.Get("auth.mail.dlq", true)
	if err != nil || !ok {
		t.Fatalf("Get dlq: ok=%v err=%v", ok, err)
	}
	if d.MessageId != "task-dead" || d.Type != "auth.mail" {
		t.Fatalf("dlq message identity: %q %q", d.MessageId, d.Type)
	}
	if d.Headers["x-umir-attempt"] == nil || fmt.Sprint(d.Headers["x-umir-attempt"]) != "5" {
		t.Fatalf("x-umir-attempt: %v", d.Headers["x-umir-attempt"])
	}
	if d.Headers["x-umir-source-queue"] != "auth.mail" || d.Headers["x-umir-error-class"] == nil {
		t.Fatalf("dlq metadata: %v", d.Headers)
	}
	for k, v := range d.Headers {
		if s := fmt.Sprint(v); bytes.Contains([]byte(s), []byte(secret)) {
			t.Fatalf("header %s carries the body", k)
		}
	}
	if !bytes.Equal(d.Body, msg.Body) {
		t.Fatal("the DLQ copy must keep the original body")
	}
	if bytes.Contains([]byte(logs.String()), []byte(secret)) {
		t.Fatalf("log carries the body or the error text:\n%s", logs.String())
	}
}

func TestMessageWithSpentAttemptsGoesToDLQWithoutCallingHandler(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	var calls atomic.Int32
	startConsumer(t, amqpURL, "auth.mail", func(context.Context, Delivery) error {
		calls.Add(1)
		return nil
	})
	// A message that arrives with five recorded failures: replayed from the DLQ
	// without clearing the header, or after the attempt limit was lowered.
	ch := adminChannel(t, amqpURL)
	err := ch.PublishWithContext(publishCtx(t, waitLong), "umir.tasks", "auth.mail", false, false, amqp.Publishing{
		Headers:     amqp.Table{"x-umir-schema": int32(1), "x-umir-generation": "g", "x-umir-attempt": int32(5)},
		ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: "task-spent", Type: "auth.mail", Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "message in auth.mail.dlq", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail.dlq") == 1 })
	eventually(t, "work queue drained", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail") == 0 })
	time.Sleep(testRetry)
	if n := calls.Load(); n != 0 {
		t.Fatalf("handler was called %d times for an exhausted message", n)
	}
	d, ok, gerr := adminChannel(t, amqpURL).Get("auth.mail.dlq", true)
	if gerr != nil || !ok || d.MessageId != "task-spent" || fmt.Sprint(d.Headers["x-umir-attempt"]) != "5" || d.Headers["x-umir-error-class"] != "attempts_exceeded" {
		t.Fatalf("dlq copy: ok=%v err=%v id=%q headers=%v", ok, gerr, d.MessageId, d.Headers)
	}
}

// crashOnce plays a consumer that dies: it takes one delivery of queue on its
// own connection and drops the connection without acknowledging.
func crashOnce(t *testing.T, amqpURL, queue string) (amqp.Delivery, bool) {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { ignoreErr(conn.Close()) }()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	deliveries, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	select {
	case d := <-deliveries:
		return d, true
	case <-time.After(2 * time.Second):
		return amqp.Delivery{}, false
	}
}

// TestCrashLoopWithoutAckEndsInDLQ is the broker half of the retry chain: a
// message whose consumers die without ack is dead-lettered by x-delivery-limit
// straight to .dlq, not into .retry, where RabbitMQ would drop it as a cycle.
func TestCrashLoopWithoutAckEndsInDLQ(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	topo := fastTopology(TopologyOptions{})
	declareTopology(t, amqpURL, topo)
	pub, _ := startPublisher(t, amqpURL, nil)
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("task-crash")); err != nil || out != Confirmed {
		t.Fatalf("Publish: %v, %v", out, err)
	}
	deliveries := 0
	for deliveries < 3*topo.DeliveryLimit {
		if _, ok := crashOnce(t, amqpURL, "auth.mail"); !ok {
			break
		}
		deliveries++
	}
	eventually(t, "message in auth.mail.dlq", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail.dlq") == 1 })
	// x-delivery-limit counts redeliveries: the first delivery plus DeliveryLimit more.
	if deliveries != topo.DeliveryLimit+1 {
		t.Fatalf("the broker delivered the message %d times before dead-lettering, want 1 + x-delivery-limit = %d", deliveries, topo.DeliveryLimit+1)
	}
	time.Sleep(2 * testRetry)
	for _, q := range []string{"auth.mail", "auth.mail.retry"} {
		if n := queueMessages(t, amqpURL, q); n != 0 {
			t.Fatalf("%s holds %d messages", q, n)
		}
	}
	d, ok, err := adminChannel(t, amqpURL).Get("auth.mail.dlq", true)
	if err != nil || !ok || d.MessageId != "task-crash" || !bytes.Equal(d.Body, taskMessage("task-crash").Body) {
		t.Fatalf("dlq message: ok=%v err=%v id=%q", ok, err, d.MessageId)
	}
}

// TestConsumeCrashLoopEndsInDLQ drops the consumer's connection in the middle of
// every Handler call: no ack is ever sent, and the message is not lost.
func TestConsumeCrashLoopEndsInDLQ(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	topo := fastTopology(TopologyOptions{})
	declareTopology(t, amqpURL, topo)
	pub, _ := startPublisher(t, amqpURL, nil)
	proxy, proxied := proxyFor(t, amqpURL)
	var calls atomic.Int32
	startConsumer(t, proxied, "auth.mail", func(ctx context.Context, _ Delivery) error {
		calls.Add(1)
		proxy.CutAfterBytes(0)
		<-ctx.Done()
		return ctx.Err()
	})
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("task-poison")); err != nil || out != Confirmed {
		t.Fatalf("Publish: %v, %v", out, err)
	}
	eventually(t, "message in auth.mail.dlq", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail.dlq") == 1 })
	time.Sleep(2 * testRetry)
	if got := int(calls.Load()); got != topo.DeliveryLimit+1 {
		t.Fatalf("handler ran %d times, want 1 + x-delivery-limit = %d", got, topo.DeliveryLimit+1)
	}
	for _, q := range []string{"auth.mail", "auth.mail.retry"} {
		if n := queueMessages(t, amqpURL, q); n != 0 {
			t.Fatalf("%s holds %d messages", q, n)
		}
	}
}

func TestRetrySucceedsOnThirdAttempt(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	pub, _ := startPublisher(t, amqpURL, nil)
	var seen attempts
	startConsumer(t, amqpURL, "auth.mail", func(_ context.Context, d Delivery) error {
		seen.add(d.Attempt)
		if d.Attempt < 3 {
			return errors.New("try again")
		}
		return nil
	})
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("task-ok")); err != nil || out != Confirmed {
		t.Fatalf("Publish: %v, %v", out, err)
	}
	eventually(t, "three attempts", waitLong, func() bool { return len(seen.snapshot()) == 3 })
	time.Sleep(2 * testRetry)
	if got := seen.snapshot(); fmt.Sprint(got) != "[1 2 3]" {
		t.Fatalf("attempts %v, want [1 2 3]", got)
	}
	for _, q := range []string{"auth.mail", "auth.mail.retry", "auth.mail.dlq"} {
		if n := queueMessages(t, amqpURL, q); n != 0 {
			t.Fatalf("%s holds %d messages after success", q, n)
		}
	}
}

func TestConsumeRedeliversWhenConnectionDropsMidHandler(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	pub, _ := startPublisher(t, amqpURL, nil)
	proxy, proxied := proxyFor(t, amqpURL)
	started := make(chan struct{}, 4)
	var calls atomic.Int32
	var redelivered atomic.Bool
	startConsumer(t, proxied, "auth.mail", func(ctx context.Context, d Delivery) error {
		if calls.Add(1) == 1 {
			started <- struct{}{}
			<-ctx.Done() // the connection is cut while the handler works: no ack may be sent
			return ctx.Err()
		}
		redelivered.Store(d.Redelivered)
		return nil
	})
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("slow")); err != nil || out != Confirmed {
		t.Fatalf("Publish: %v, %v", out, err)
	}
	select {
	case <-started:
	case <-time.After(waitLong):
		t.Fatal("handler did not start")
	}
	proxy.CutAfterBytes(0)
	eventually(t, "second delivery", waitLong, func() bool { return calls.Load() >= 2 })
	eventually(t, "queue drained", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail") == 0 })
	if !redelivered.Load() {
		t.Fatal("the message must come back flagged as redelivered")
	}
}

func waitLog(t *testing.T, logs *safeBuffer, what, fragment string) {
	t.Helper()
	eventually(t, what, waitLong, func() bool { return strings.Contains(logs.String(), fragment) })
}

// TestUnconfirmedRetryCopyKeepsOriginalUnacknowledged: a full .retry queue
// answers the copy with nack. The original must stay unacknowledged, so it is
// given back when the consumer stops; a later copy is confirmed and only then
// is the original acknowledged.
func TestUnconfirmedRetryCopyKeepsOriginalUnacknowledged(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	pub, _ := startPublisher(t, amqpURL, nil)
	admin := adminChannel(t, amqpURL)
	// A classic queue enforces the limit exactly and has no TTL: the blocker stays.
	if _, err := admin.QueueDelete("auth.mail.retry", false, false, false); err != nil {
		t.Fatal(err)
	}
	args := amqp.Table{"x-queue-type": "classic", "x-max-length": int64(1), "x-overflow": "reject-publish"}
	if _, err := admin.QueueDeclare("auth.mail.retry", true, false, false, false, args); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueueBind("auth.mail.retry", "auth.mail.retry", "umir.dlx", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := admin.PublishWithContext(publishCtx(t, waitLong), "umir.dlx", "auth.mail.retry", false, false, amqp.Publishing{MessageId: "blocker", Body: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "blocker queued", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail.retry") == 1 })

	var calls atomic.Int32
	fail := func(context.Context, Delivery) error {
		calls.Add(1)
		return errors.New("boom")
	}
	logs, stop := startStoppableConsumer(t, amqpURL, "auth.mail", fail)
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("task-nack")); err != nil || out != Confirmed {
		t.Fatalf("Publish: %v, %v", out, err)
	}
	waitLog(t, logs, "nacked copy", "outcome=nacked")
	if n := calls.Load(); n != 1 {
		t.Fatalf("the Handler ran %d times; the copy is published again, not the Handler", n)
	}
	stop()
	// Not acknowledged: the broker gave the message back, and no copy was stored.
	eventually(t, "original returned to auth.mail", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail") == 1 })
	if n := queueMessages(t, amqpURL, "auth.mail.retry"); n != 1 {
		t.Fatalf("auth.mail.retry holds %d messages, want only the blocker", n)
	}

	if _, err := admin.QueuePurge("auth.mail.retry", false); err != nil {
		t.Fatal(err)
	}
	startConsumer(t, amqpURL, "auth.mail", fail)
	eventually(t, "copy in auth.mail.retry", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail.retry") == 1 })
	eventually(t, "original acknowledged", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail") == 0 })
	d, ok, err := admin.Get("auth.mail.retry", true)
	if err != nil || !ok || d.MessageId != "task-nack" || fmt.Sprint(d.Headers["x-umir-attempt"]) != "1" {
		t.Fatalf("retry copy: ok=%v err=%v id=%q headers=%v", ok, err, d.MessageId, d.Headers)
	}
}

// TestRetryQueueMissingThenRepaired: without the .retry binding the copy is
// returned by the broker; after mq-init declares the topology again the message
// goes through the chain and succeeds on the second attempt.
func TestRetryQueueMissingThenRepaired(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	topo := fastTopology(TopologyOptions{})
	declareTopology(t, amqpURL, topo)
	pub, _ := startPublisher(t, amqpURL, nil)
	if _, err := adminChannel(t, amqpURL).QueueDelete("auth.mail.retry", false, false, false); err != nil {
		t.Fatal(err)
	}
	var seen attempts
	logs := startConsumer(t, amqpURL, "auth.mail", func(_ context.Context, d Delivery) error {
		seen.add(d.Attempt)
		if d.Attempt < 2 {
			return errors.New("boom")
		}
		return nil
	})
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("task-repair")); err != nil || out != Confirmed {
		t.Fatalf("Publish: %v, %v", out, err)
	}
	waitLog(t, logs, "returned copy", "outcome=returned")
	declareTopology(t, amqpURL, topo)
	eventually(t, "second attempt", waitLong, func() bool { return fmt.Sprint(seen.snapshot()) == "[1 2]" })
	eventually(t, "queues drained", waitLong, func() bool {
		return queueMessages(t, amqpURL, "auth.mail") == 0 && queueMessages(t, amqpURL, "auth.mail.retry") == 0
	})
	if n := queueMessages(t, amqpURL, "auth.mail.dlq"); n != 0 {
		t.Fatalf("auth.mail.dlq holds %d messages after a success", n)
	}
}

// TestDLQUnavailableThenRepaired: the last failure cannot be copied while .dlq
// is gone; the original is kept (the Handler does not run again) and the copy
// arrives after the topology is repaired.
func TestDLQUnavailableThenRepaired(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	topo := fastTopology(TopologyOptions{})
	declareTopology(t, amqpURL, topo)
	pub, _ := startPublisher(t, amqpURL, nil)
	if _, err := adminChannel(t, amqpURL).QueueDelete("auth.mail.dlq", false, false, false); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	logs := startConsumer(t, amqpURL, "auth.mail", func(context.Context, Delivery) error {
		calls.Add(1)
		return errors.New("boom")
	}, WithMaxAttempts(1))
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("task-last")); err != nil || out != Confirmed {
		t.Fatalf("Publish: %v, %v", out, err)
	}
	waitLog(t, logs, "unconfirmed dead-letter copy", "target=auth.mail.dlq")
	time.Sleep(200 * time.Millisecond)
	if n := queueMessages(t, amqpURL, "auth.mail"); n != 0 {
		t.Fatalf("auth.mail holds %d ready messages: the original must stay with the consumer", n)
	}
	declareTopology(t, amqpURL, topo)
	eventually(t, "message in auth.mail.dlq", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail.dlq") == 1 })
	if n := calls.Load(); n != 1 {
		t.Fatalf("the Handler ran %d times", n)
	}
	d, ok, err := adminChannel(t, amqpURL).Get("auth.mail.dlq", true)
	if err != nil || !ok || d.MessageId != "task-last" || fmt.Sprint(d.Headers["x-umir-attempt"]) != "1" {
		t.Fatalf("dlq copy: ok=%v err=%v id=%q headers=%v", ok, err, d.MessageId, d.Headers)
	}
}

// TestDeadLetterChannelFailureReconnects: publishing the copy to a missing
// umir.dlx closes the publishing channel. The consumer must notice, reconnect
// and, once the topology is back, move the message; it is not lost.
func TestDeadLetterChannelFailureReconnects(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	topo := fastTopology(TopologyOptions{})
	topo.DeliveryLimit = 50 // the redeliveries while the exchange is gone must not reach the broker limit
	declareTopology(t, amqpURL, topo)
	pub, _ := startPublisher(t, amqpURL, nil)
	if err := adminChannel(t, amqpURL).ExchangeDelete("umir.dlx", false, false); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	logs := startConsumer(t, amqpURL, "auth.mail", func(context.Context, Delivery) error {
		calls.Add(1)
		return errors.New("boom")
	}, WithMaxAttempts(1))
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("task-chan")); err != nil || out != Confirmed {
		t.Fatalf("Publish: %v, %v", out, err)
	}
	eventually(t, "second delivery after reconnect", waitLong, func() bool { return calls.Load() >= 2 })
	if !strings.Contains(logs.String(), "connection lost") {
		t.Fatalf("the session must end and be logged:\n%s", logs.String())
	}
	declareTopology(t, amqpURL, topo)
	eventually(t, "message in auth.mail.dlq", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail.dlq") == 1 })
	eventually(t, "work queue drained", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail") == 0 })
}

func TestConcurrentPublishConfirmsEveryMessage(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	pub, _ := startPublisher(t, amqpURL, nil)
	const workers, each = 8, 25
	var wg sync.WaitGroup
	var bad atomic.Int32
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				out, err := pub.Publish(publishCtx(t, waitLong), taskMessage(fmt.Sprintf("c-%d-%d", w, i)))
				if err != nil || out != Confirmed {
					bad.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if n := bad.Load(); n != 0 {
		t.Fatalf("%d publishes were not confirmed", n)
	}
	eventually(t, "all messages queued", waitLong, func() bool { return queueMessages(t, amqpURL, "auth.mail") == workers*each })
}

// TestCheckPassiveDoesNotSeeArgumentsOrBindings pins the limit documented in
// events.md: a service sees a missing exchange or queue, nothing else. Only
// Declare in mq-init finds other arguments; a lost binding shows itself as a
// Returned publish.
func TestCheckPassiveDoesNotSeeArgumentsOrBindings(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	topo := fastTopology(TopologyOptions{})
	declareTopology(t, amqpURL, topo)
	admin := adminChannel(t, amqpURL)
	if _, err := admin.QueueDelete("auth.mail", false, false, false); err != nil {
		t.Fatal(err)
	}
	// Same name, other arguments: no delivery limit, no dead-letter target, and the
	// bindings of the old queue (umir.tasks, umir.dlx) are gone with it.
	if _, err := admin.QueueDeclare("auth.mail", true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckPassive(adminChannel(t, amqpURL), topo); err != nil {
		t.Fatalf("CheckPassive must pass for a queue with other arguments and no bindings: %v", err)
	}
	if err := Declare(adminChannel(t, amqpURL), topo); !errors.Is(err, ErrTopologyMismatch) {
		t.Fatalf("Declare must report the mismatch, got %v", err)
	}
	pub, _ := startPublisher(t, amqpURL, nil)
	if out, err := pub.Publish(publishCtx(t, waitLong), taskMessage("lost-binding")); err != nil || out != Returned {
		t.Fatalf("without the umir.tasks binding the publish must be Returned, got %v, %v", out, err)
	}
}

func TestExclusiveQueueDisappearsWithItsConnection(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	q, err := GatewayLiveQueue("gateway-a")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	if err := DeclareExclusive(ch, q); err != nil {
		t.Fatalf("DeclareExclusive: %v", err)
	}
	// Exclusive: another connection cannot even look at it with a passive declare.
	other := adminChannel(t, amqpURL)
	if _, err := other.QueueDeclarePassive(q.Name, false, true, true, false, nil); err == nil {
		t.Fatal("an exclusive queue must be locked for other connections")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	eventually(t, "exclusive queue gone", waitLong, func() bool { return queueMessages(t, amqpURL, q.Name) == -1 })
	// Declaring it again on a new connection works (the reconnect path).
	conn2 := dialDirect(t, amqpURL)
	if err := DeclareExclusive(openChannel(t, conn2), q); err != nil {
		t.Fatalf("declare again: %v", err)
	}
}

func TestFanoutQueuesEachGetTheMessage(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	pub, _ := startPublisher(t, amqpURL, nil)

	recv := make(chan string, 8)
	var declared atomic.Int32
	for _, instance := range []string{"gateway-a", "gateway-b"} {
		q, err := GatewayLiveQueue(instance)
		if err != nil {
			t.Fatal(err)
		}
		startConsumer(t, amqpURL, q.Name, func(_ context.Context, d Delivery) error {
			recv <- instance + ":" + d.MessageID
			return nil
		}, WithFanout(), WithSetup(func(ch *amqp.Channel) error {
			err := DeclareExclusive(ch, q)
			declared.Add(1)
			return err
		}))
	}
	// DeclareExclusive binds the queue, so after both setups the event reaches both.
	eventually(t, "both fan-out queues declared", waitLong, func() bool { return declared.Load() == 2 })
	out, err := pub.Publish(publishCtx(t, waitLong), eventMessage("evt-fanout"))
	if err != nil || out != Confirmed {
		t.Fatalf("Publish: %v, %v", out, err)
	}
	got := map[string]bool{}
	for len(got) < 2 {
		select {
		case m := <-recv:
			got[m] = true
		case <-time.After(waitLong):
			t.Fatalf("only %v received the event", got)
		}
	}
	if !got["gateway-a:evt-fanout"] || !got["gateway-b:evt-fanout"] {
		t.Fatalf("received: %v", got)
	}
}

func TestFanoutHandlerErrorDropsMessage(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	pub, _ := startPublisher(t, amqpURL, nil)
	q, err := GatewayLiveQueue("gateway-a")
	if err != nil {
		t.Fatal(err)
	}
	var seen attempts
	startConsumer(t, amqpURL, q.Name, func(_ context.Context, d Delivery) error {
		seen.add(d.Attempt)
		return errors.New("unreadable")
	}, WithFanout(), WithSetup(func(ch *amqp.Channel) error { return DeclareExclusive(ch, q) }))
	eventually(t, "queue bound", waitLong, func() bool {
		out := outcomeOf(pub.Publish(publishCtx(t, 2*time.Second), eventMessage("e1")))
		return out == Confirmed
	})
	eventually(t, "first delivery", waitLong, func() bool { return len(seen.snapshot()) >= 1 })
	time.Sleep(testRetry * 2)
	n := len(seen.snapshot())
	time.Sleep(testRetry * 2)
	if len(seen.snapshot()) != n {
		t.Fatal("a rejected fan-out message must not be redelivered")
	}
}

func TestConsumerReconnectsAndDeclaresItsQueueAgain(t *testing.T) {
	amqpURL := itest.NewVHost(t)
	declareTopology(t, amqpURL, fastTopology(TopologyOptions{}))
	pub, _ := startPublisher(t, amqpURL, nil)
	proxy, proxied := proxyFor(t, amqpURL)
	q, err := ServingAuthQueue("serving-a")
	if err != nil {
		t.Fatal(err)
	}
	var setups atomic.Int32
	recv := make(chan string, 8)
	startConsumer(t, proxied, q.Name, func(_ context.Context, d Delivery) error {
		recv <- d.MessageID
		return nil
	}, WithFanout(), WithSetup(func(ch *amqp.Channel) error {
		setups.Add(1)
		return DeclareExclusive(ch, q)
	}))
	authEvent := func(id string) Message {
		return Message{
			Exchange: "umir.auth", RoutingKey: "auth.restaurant.deleted", MessageID: id, Type: "auth.restaurant.deleted",
			Schema: 1, Generation: "0d4a52c8-91f0-4e3b-8c77-2a6b5e0f9a31", Body: []byte(`{}`),
		}
	}
	eventually(t, "first event", waitLong, func() bool {
		out := outcomeOf(pub.Publish(publishCtx(t, 2*time.Second), authEvent("a1")))
		if out != Confirmed {
			return false
		}
		select {
		case <-recv:
			return true
		case <-time.After(500 * time.Millisecond):
			return false
		}
	})
	proxy.CutAfterBytes(0)
	eventually(t, "second session", waitLong, func() bool { return setups.Load() >= 2 })
	eventually(t, "event after reconnect", waitLong, func() bool {
		out := outcomeOf(pub.Publish(publishCtx(t, 2*time.Second), authEvent("a2")))
		if out != Confirmed {
			return false
		}
		select {
		case id := <-recv:
			return id == "a2"
		case <-time.After(500 * time.Millisecond):
			return false
		}
	})
}

// outcomeOf drops the error of a Publish whose Unknown outcome the test polls over.
func outcomeOf(out Outcome, _ error) Outcome { return out }
