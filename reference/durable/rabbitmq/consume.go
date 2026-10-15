package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// errShutdown tells the consume loop that ctx ended while a Handler ran.
var errShutdown = errors.New("rabbitmq: consumer is shutting down")

const (
	dlqPublishTimeout     = 10 * time.Second
	classAttemptsExceeded = "attempts_exceeded"
)

// Delivery is one message handed to a Handler.
type Delivery struct {
	Queue      string
	Exchange   string
	RoutingKey string
	// MessageID and Type are the AMQP message_id and type properties.
	MessageID string
	Type      string
	// Headers are the message headers (x-umir-*, x-death and others).
	Headers map[string]any
	// Attempt is the 1-based number of this processing attempt: 1 plus the sum
	// of the x-death counts of the work queue. Always 1 on fan-out queues.
	Attempt int
	// Redelivered is the broker's flag: the message was delivered before and
	// not acknowledged.
	Redelivered bool
	Body        []byte
}

// Handler processes one delivery. A nil result acknowledges the message; an
// error sends it to the retry chain (or drops it on a fan-out queue). The
// Handler must be idempotent: a message can be delivered again after a crash.
// An error may implement Class() string to name its class in logs; the error
// text is never logged because it may quote the body.
type Handler func(ctx context.Context, d Delivery) error

type consumeConfig struct {
	setup       func(*amqp.Channel) error
	maxAttempts int
	fanout      bool
	logger      *slog.Logger
}

// ConsumeOption configures Consume.
type ConsumeOption func(*consumeConfig)

// WithSetup runs fn on the consuming channel of every new connection before
// consumption starts: DeclareExclusive for a fan-out queue, CheckPassive for a
// durable one.
func WithSetup(fn func(*amqp.Channel) error) ConsumeOption {
	return func(c *consumeConfig) { c.setup = fn }
}

// WithMaxAttempts sets the attempt after which a failing message goes to
// <queue>.dlq instead of the retry queue. The default is DefaultDeliveryLimit,
// the x-delivery-limit of the topology.
func WithMaxAttempts(n int) ConsumeOption {
	return func(c *consumeConfig) { c.maxAttempts = n }
}

// WithFanout marks the queue as an exclusive fan-out queue: no .retry and no
// .dlq, so a Handler error rejects the message without requeue and drops it.
func WithFanout() ConsumeOption {
	return func(c *consumeConfig) { c.fanout = true }
}

// WithLogger sets the logger of Consume (Connector.Consume uses the connector's).
func WithLogger(l *slog.Logger) ConsumeOption {
	return func(c *consumeConfig) { c.logger = l }
}

func newConsumeConfig(opts []ConsumeOption, fallback *slog.Logger) (consumeConfig, error) {
	cfg := consumeConfig{maxAttempts: DefaultDeliveryLimit, logger: fallback}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.logger == nil {
		cfg.logger = slog.New(slog.DiscardHandler)
	}
	if cfg.maxAttempts < 1 {
		return cfg, errors.New("rabbitmq: max attempts must be positive")
	}
	return cfg, nil
}

// Consume reads queue through the connector, reconnecting with backoff, until
// ctx ends; it returns ctx.Err() (or a configuration error at once). See the
// package-level Consume for the processing rules.
func (c *Connector) Consume(ctx context.Context, queue string, prefetch int, h Handler, opts ...ConsumeOption) error {
	cfg, err := newConsumeConfig(opts, c.Logger)
	if err != nil {
		return err
	}
	return c.Run(ctx, func(ctx context.Context, conn *amqp.Connection, up func()) error {
		return consume(ctx, conn, queue, prefetch, h, cfg, up)
	})
}

// Consume reads queue on conn until ctx ends (nil) or the channel fails (error),
// handling one delivery at a time. prefetch is the unacknowledged limit.
//
//   - A Handler success acknowledges the message after the Handler returned.
//   - A Handler error nacks it without requeue: the queue's dead-letter
//     arguments send it through umir.dlx to <queue>.retry and, after the pause,
//     back to <queue>.
//   - When attempt number maxAttempts fails, Consume publishes a copy to
//     umir.dlx with the key <queue>.dlq, waits for the broker's confirm and only
//     then acknowledges the original. If the copy is not confirmed, the
//     original goes through the retry chain again.
//   - A message whose attempt number is already above maxAttempts before the
//     Handler runs goes to <queue>.dlq at once, without calling the Handler.
//   - Logs and the DLQ copy's metadata carry message_id, type, attempt, queue
//     and the error class, never the body or the error text.
//
// If ctx ends while a Handler runs, its error is not counted as a failed
// attempt: the message stays unacknowledged and returns to the queue.
func Consume(ctx context.Context, conn *amqp.Connection, queue string, prefetch int, h Handler, opts ...ConsumeOption) error {
	cfg, err := newConsumeConfig(opts, nil)
	if err != nil {
		return err
	}
	return consume(ctx, conn, queue, prefetch, h, cfg, func() {})
}

func consume(ctx context.Context, conn *amqp.Connection, queue string, prefetch int, h Handler, cfg consumeConfig, up func()) error {
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	closed := ch.NotifyClose(make(chan *amqp.Error, 1))
	if err := ch.Qos(prefetch, 0, false); err != nil {
		return fmt.Errorf("set prefetch: %w", err)
	}
	if cfg.setup != nil {
		if err := cfg.setup(ch); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
	}
	var dlq *confirmChannel
	if !cfg.fanout {
		if dlq, err = newConfirmChannel(conn); err != nil {
			return fmt.Errorf("dead-letter channel: %w", err)
		}
	}
	deliveries, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		return classify("consume "+queue, err)
	}
	up()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-closed:
			if e == nil {
				return errors.New("channel closed")
			}
			return fmt.Errorf("channel closed: %w", e)
		case raw, ok := <-deliveries:
			if !ok {
				return errors.New("deliveries closed")
			}
			if err := settle(ctx, raw, queue, h, cfg, dlq); err != nil {
				if errors.Is(err, errShutdown) {
					return nil
				}
				return err
			}
		}
	}
}

func toDelivery(raw amqp.Delivery, queue string, fanout bool) Delivery {
	d := Delivery{
		Queue: queue, Exchange: raw.Exchange, RoutingKey: raw.RoutingKey,
		MessageID: raw.MessageId, Type: raw.Type, Headers: raw.Headers,
		Attempt: 1, Redelivered: raw.Redelivered, Body: raw.Body,
	}
	if !fanout {
		d.Attempt = attemptFromDeath(raw.Headers, queue)
	}
	return d
}

// settle runs the Handler for one delivery and acknowledges, retries or dead-letters it.
func settle(ctx context.Context, raw amqp.Delivery, queue string, h Handler, cfg consumeConfig, dlq *confirmChannel) error {
	d := toDelivery(raw, queue, cfg.fanout)
	if !cfg.fanout && d.Attempt > cfg.maxAttempts {
		// The handler already failed (or crashed) maxAttempts times: do not run it again.
		log := cfg.logger.With("queue", queue, "message_id", d.MessageID, "type", d.Type, "attempt", d.Attempt, "error_class", classAttemptsExceeded)
		if deadLetter(ctx, raw, d, classAttemptsExceeded, dlq, log) {
			if err := raw.Ack(false); err != nil {
				return fmt.Errorf("ack: %w", err)
			}
			return nil
		}
		if err := raw.Nack(false, false); err != nil {
			return fmt.Errorf("nack: %w", err)
		}
		return nil
	}
	herr := h(ctx, d)
	if herr == nil {
		if err := raw.Ack(false); err != nil {
			return fmt.Errorf("ack: %w", err)
		}
		return nil
	}
	if ctx.Err() != nil {
		return errShutdown // leave the message unacknowledged, it comes back
	}
	class := errorClass(herr)
	log := cfg.logger.With("queue", queue, "message_id", d.MessageID, "type", d.Type, "attempt", d.Attempt, "error_class", class)
	if cfg.fanout {
		log.Warn("rabbitmq message rejected")
		if err := raw.Reject(false); err != nil {
			return fmt.Errorf("reject: %w", err)
		}
		return nil
	}
	if d.Attempt >= cfg.maxAttempts {
		if deadLetter(ctx, raw, d, class, dlq, log) {
			if err := raw.Ack(false); err != nil {
				return fmt.Errorf("ack: %w", err)
			}
			return nil
		}
		log.Error("rabbitmq dead-letter copy was not confirmed; message goes through the retry chain again")
	} else {
		log.Warn("rabbitmq message failed; retry follows")
	}
	if err := raw.Nack(false, false); err != nil {
		return fmt.Errorf("nack: %w", err)
	}
	return nil
}

// deadLetter publishes the copy of a failed message to <queue>.dlq and reports
// whether the broker confirmed it as routed.
func deadLetter(ctx context.Context, raw amqp.Delivery, d Delivery, class string, dlq *confirmChannel, log *slog.Logger) bool {
	headers := amqp.Table{}
	for k, v := range raw.Headers {
		if strings.HasPrefix(k, "x-umir-") {
			headers[k] = v
		}
	}
	headers["x-umir-attempt"] = int32(d.Attempt)
	headers["x-umir-error-class"] = class
	headers["x-umir-source-queue"] = d.Queue
	pub := amqp.Publishing{
		Headers: headers, ContentType: raw.ContentType, DeliveryMode: amqp.Persistent,
		MessageId: raw.MessageId, Type: raw.Type, Body: raw.Body,
	}
	pctx, cancel := context.WithTimeout(ctx, dlqPublishTimeout)
	defer cancel()
	out, err := dlq.publish(pctx, ExchangeDLX, d.Queue+".dlq", pub)
	if out == Confirmed {
		log.Error("rabbitmq message moved to the dead-letter queue")
		return true
	}
	if err != nil {
		log.Error("rabbitmq dead-letter publish failed", "outcome", out.String(), "error_class", errorClass(err))
	} else {
		log.Error("rabbitmq dead-letter publish failed", "outcome", out.String())
	}
	return false
}

// attemptFromDeath returns the number of the current processing attempt of a
// message on a work queue: 1 plus the sum of the counts of the x-death entries
// whose queue is the work queue itself, whatever the reason (rejected,
// delivery_limit). Entries of .retry (expired) are not counted: they are the
// same attempt seen from the other side.
func attemptFromDeath(headers amqp.Table, queue string) int {
	entries, ok := headers["x-death"].([]any)
	if !ok {
		return 1
	}
	attempt := 1
	for _, e := range entries {
		entry, ok := e.(amqp.Table)
		if !ok {
			continue
		}
		if name, ok := entry["queue"].(string); !ok || name != queue {
			continue
		}
		attempt += int(toInt64(entry["count"]))
	}
	return attempt
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case int16:
		return int64(n)
	case uint8:
		return int64(n)
	case uint16:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	default:
		return 0
	}
}

// errorClass names the class of an error for logs and DLQ metadata without its
// text, which may quote the message body: Class() when the error offers it, a
// fixed name for context errors, the innermost Go type otherwise.
func errorClass(err error) string {
	var classed interface{ Class() string }
	if errors.As(err, &classed) {
		return classed.Class()
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	for {
		next := errors.Unwrap(err)
		if next == nil {
			break
		}
		err = next
	}
	return fmt.Sprintf("%T", err)
}
