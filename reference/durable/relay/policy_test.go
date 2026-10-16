package relay_test

import (
	"testing"

	"github.com/JustIkra/umir-factory/reference/durable/relay"
)

func TestDefaultPoliciesFollowTheEventCatalog(t *testing.T) {
	p := relay.DefaultPolicies()
	for _, tc := range []struct {
		exchange, key string
		want          relay.ReturnPolicy
	}{
		{"umir.events", "warehouse.OrderPlaced", relay.Accept},
		{"umir.events", "warehouse.BatchExpired", relay.Accept},
		{"umir.auth", "auth.restaurant.deleted", relay.Accept},
		{"umir.auth", "auth.membership.revoked", relay.Accept},
		{"umir.tasks", "auth.mail", relay.Retry},
		{"umir.tasks", "feedback.smtp", relay.Retry},
		{"umir.tasks", "feedback.telegram", relay.Retry},
		{"umir.other", "anything", relay.Retry}, // unknown means a configuration error: never lose it
		{"", "", relay.Retry},
	} {
		if got := p.PolicyFor(tc.exchange, tc.key); got != tc.want {
			t.Errorf("PolicyFor(%q, %q) = %v, want %v", tc.exchange, tc.key, got, tc.want)
		}
	}
}

func TestZeroValuePolicyIsRetry(t *testing.T) {
	var zero relay.ReturnPolicy
	if zero != relay.Retry {
		t.Fatalf("zero ReturnPolicy = %v, want Retry", zero)
	}
	if relay.Retry.String() != "retry" || relay.Accept.String() != "accept" {
		t.Fatalf("names: %q %q", relay.Retry, relay.Accept)
	}
}

func TestPolicyTableMatching(t *testing.T) {
	p := relay.PolicyTable{
		Default: relay.Retry,
		Rules: []relay.PolicyRule{
			{Exchange: "x", Pattern: "a.b.c", Policy: relay.Accept},
			{Exchange: "x", Pattern: "a.*.c", Policy: relay.Retry},
			{Exchange: "x", Pattern: "a.#", Policy: relay.Accept},
			{Pattern: "any.*", Policy: relay.Accept}, // empty Exchange matches every exchange
		},
	}
	for _, tc := range []struct {
		exchange, key string
		want          relay.ReturnPolicy
	}{
		{"x", "a.b.c", relay.Accept},   // first rule wins
		{"x", "a.z.c", relay.Retry},    // star is exactly one word
		{"x", "a.z.d.e", relay.Accept}, // hash is any number of words
		{"x", "a", relay.Accept},       // hash may be empty
		{"x", "b.b.c", relay.Retry},    // default
		{"y", "a.b.c", relay.Retry},    // other exchange
		{"y", "any.thing", relay.Accept},
		{"y", "any.thing.more", relay.Retry}, // star is one word only
		{"y", "any", relay.Retry},
	} {
		if got := p.PolicyFor(tc.exchange, tc.key); got != tc.want {
			t.Errorf("PolicyFor(%q, %q) = %v, want %v", tc.exchange, tc.key, got, tc.want)
		}
	}
}

func TestTopicPatterns(t *testing.T) {
	for _, tc := range []struct {
		pattern, key string
		want         bool
	}{
		{"#", "", true},
		{"#", "a.b.c", true},
		{"*", "a", true},
		{"*", "a.b", false},
		{"*", "", true}, // an empty key is one empty word, as in AMQP
		{"a.#.d", "a.d", true},
		{"a.#.d", "a.b.c.d", true},
		{"a.#.d", "a.b.c", false},
		{"#.d", "d", true},
		{"a.#.#", "a", true},
		{"a.*.#", "a.b", true},
		{"a.*.#", "a", false},
		{"warehouse.#", "warehouse.OrderPlaced", true},
		{"warehouse.#", "auth.x", false},
		{"a.b", "a.b.c", false},
	} {
		table := relay.PolicyTable{Default: relay.Retry, Rules: []relay.PolicyRule{{Pattern: tc.pattern, Policy: relay.Accept}}}
		got := table.PolicyFor("e", tc.key) == relay.Accept
		if got != tc.want {
			t.Errorf("pattern %q key %q: match = %v, want %v", tc.pattern, tc.key, got, tc.want)
		}
	}
}
