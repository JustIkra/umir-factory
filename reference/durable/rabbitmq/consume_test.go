package rabbitmq

import (
	"context"
	"errors"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestAttemptFromHeaders(t *testing.T) {
	cases := []struct {
		name    string
		headers amqp.Table
		want    int
	}{
		{"no headers", nil, 1},
		{"no counter", amqp.Table{"x-umir-schema": int32(1)}, 1},
		{"one failure", amqp.Table{"x-umir-attempt": int32(1)}, 2},
		{"int64", amqp.Table{"x-umir-attempt": int64(4)}, 5},
		{"four failures", amqp.Table{"x-umir-attempt": int32(4)}, 5},
		{"negative", amqp.Table{"x-umir-attempt": int32(-3)}, 1},
		{"wrong type", amqp.Table{"x-umir-attempt": "3"}, 1},
		{"huge is capped", amqp.Table{"x-umir-attempt": int64(1) << 40}, 1<<20 + 1},
		// The broker's own markers are not the counter: a crash loop is the broker's business.
		{"broker headers ignored", amqp.Table{"x-delivery-count": int64(3), "x-death": []any{amqp.Table{"queue": "auth.mail", "count": int64(2)}}}, 1},
	}
	for _, c := range cases {
		if got := attemptFromHeaders(c.headers); got != c.want {
			t.Errorf("%s: attempt %d, want %d", c.name, got, c.want)
		}
	}
}

func TestBuildCopy(t *testing.T) {
	raw := amqp.Delivery{
		MessageId: "m1", Type: "auth.mail", ContentType: "application/json", Body: []byte(`{"a":1}`),
		Headers: amqp.Table{
			"x-umir-schema": int32(1), "x-umir-generation": "g", "x-umir-attempt": int32(2),
			"x-umir-error-class": "stale", "x-umir-source-queue": "stale",
			"x-death": []any{amqp.Table{"queue": "auth.mail.retry"}}, "x-delivery-count": int64(1),
		},
	}
	retry := buildCopy(raw, "auth.mail", 3, ".retry", "boom")
	if retry.Headers["x-umir-attempt"] != int32(3) || retry.Headers["x-umir-schema"] != int32(1) || retry.Headers["x-umir-generation"] != "g" {
		t.Fatalf("retry headers: %v", retry.Headers)
	}
	for _, k := range []string{"x-umir-error-class", "x-umir-source-queue", "x-death", "x-delivery-count"} {
		if _, ok := retry.Headers[k]; ok {
			t.Errorf("retry copy must not carry %s", k)
		}
	}
	if retry.MessageId != "m1" || retry.Type != "auth.mail" || retry.DeliveryMode != amqp.Persistent || string(retry.Body) != `{"a":1}` {
		t.Fatalf("retry identity: %+v", retry)
	}
	dead := buildCopy(raw, "auth.mail", 5, ".dlq", "boom")
	if dead.Headers["x-umir-failed-attempts"] != int32(5) || dead.Headers["x-umir-attempt"] != nil || dead.Headers["x-umir-error-class"] != "boom" || dead.Headers["x-umir-source-queue"] != "auth.mail" {
		t.Fatalf("dlq headers: %v", dead.Headers)
	}
	if _, ok := dead.Headers["x-death"]; ok {
		t.Error("dlq copy must not carry x-death")
	}
	if raw.Headers["x-umir-attempt"] != int32(2) {
		t.Error("buildCopy must not change the original headers")
	}
}

func TestConsumeRejectsBadConfiguration(t *testing.T) {
	c := &Connector{URL: "amqp://u:p@127.0.0.1:1/x"} // never dialed: the configuration error comes first
	h := func(context.Context, Delivery) error { return nil }
	for name, call := range map[string]func() error{
		"zero prefetch":     func() error { return c.Consume(t.Context(), "q", 0, h) },
		"negative prefetch": func() error { return c.Consume(t.Context(), "q", -1, h) },
		"zero attempts":     func() error { return c.Consume(t.Context(), "q", 1, h, WithMaxAttempts(0)) },
		"zero copy pause":   func() error { return c.Consume(t.Context(), "q", 1, h, WithCopyPause(0)) },
		"package Consume":   func() error { return Consume(t.Context(), nil, "q", 0, h) },
	} {
		if err := call(); err == nil {
			t.Errorf("%s: want a configuration error", name)
		}
	}
}

type classed struct{}

func (classed) Error() string { return "secret body text" }
func (classed) Class() string { return "unreadable" }

func TestErrorClassNeverUsesMessageText(t *testing.T) {
	if got := errorClass(classed{}); got != "unreadable" {
		t.Fatalf("class: %q", got)
	}
	if got := errorClass(errors.New("secret body text")); got == "secret body text" || got == "" {
		t.Fatalf("class must not be the error text: %q", got)
	}
}

func TestExponentialBackoffBounds(t *testing.T) {
	want := []string{"500ms", "1s", "2s", "4s", "8s", "16s", "30s", "30s"}
	for i, w := range want {
		if got := ExponentialBackoff(i).String(); got != w {
			t.Errorf("attempt %d: %s, want %s", i, got, w)
		}
	}
	if ExponentialBackoff(-1).String() != "30s" || ExponentialBackoff(1000).String() != "30s" {
		t.Fatal("out-of-range attempts must be capped at 30s")
	}
}

func TestOutcomeZeroValueIsNotConfirmed(t *testing.T) {
	var o Outcome
	if o == Confirmed {
		t.Fatal("the zero Outcome must not be Confirmed")
	}
	for o, s := range map[Outcome]string{Unknown: "unknown", Confirmed: "confirmed", Returned: "returned", Nacked: "nacked"} {
		if o.String() != s {
			t.Errorf("String(%d) = %q, want %q", o, o.String(), s)
		}
	}
}

func TestRedactErrorHidesCredentials(t *testing.T) {
	err := redact(errors.New(`parse "amqp://user:secretpw@mq:x/": invalid port`), "amqp://user:secretpw@mq:x/")
	if msg := err.Error(); containsAny(msg, "secretpw", "user:") {
		t.Fatalf("leak: %s", msg)
	}
	inner := errors.New("boom")
	if !errors.Is(redact(inner, "amqp://u:p@h/"), inner) {
		t.Fatal("redact must keep the cause for errors.Is")
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
