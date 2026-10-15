package rabbitmq

import (
	"errors"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func death(queue, reason string, count any) amqp.Table {
	return amqp.Table{"queue": queue, "reason": reason, "count": count, "exchange": "umir.dlx"}
}

func TestAttemptFromDeath(t *testing.T) {
	cases := []struct {
		name    string
		headers amqp.Table
		want    int
	}{
		{"no headers", nil, 1},
		{"no x-death", amqp.Table{"x-umir-schema": int32(1)}, 1},
		{"one rejection", amqp.Table{"x-death": []any{death("auth.mail", "rejected", int64(1))}}, 2},
		{"rejected and expired", amqp.Table{"x-death": []any{
			death("auth.mail.retry", "expired", int64(2)),
			death("auth.mail", "rejected", int64(2)),
		}}, 3},
		{"delivery limit counts", amqp.Table{"x-death": []any{
			death("auth.mail", "delivery_limit", int64(2)),
			death("auth.mail", "rejected", int64(1)),
			death("auth.mail.retry", "expired", int64(3)),
		}}, 4},
		{"other queue ignored", amqp.Table{"x-death": []any{death("feedback.smtp", "rejected", int64(4))}}, 1},
		{"int32 count", amqp.Table{"x-death": []any{death("auth.mail", "rejected", int32(3))}}, 4},
		{"broken entries ignored", amqp.Table{"x-death": []any{"junk", amqp.Table{"queue": "auth.mail"}, death("auth.mail", "rejected", "x")}}, 1},
		{"wrong header type", amqp.Table{"x-death": "junk"}, 1},
	}
	for _, c := range cases {
		if got := attemptFromDeath(c.headers, "auth.mail"); got != c.want {
			t.Errorf("%s: attempt %d, want %d", c.name, got, c.want)
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
