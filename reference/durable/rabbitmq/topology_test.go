package rabbitmq

import (
	"slices"
	"testing"
	"time"
)

func queueByName(t *testing.T, topo Topology, name string) QueueSpec {
	t.Helper()
	for _, q := range topo.Queues() {
		if q.Name == name {
			return q
		}
	}
	t.Fatalf("queue %q is not in the topology", name)
	return QueueSpec{}
}

func hasBinding(topo Topology, queue, exchange, key string) bool {
	return slices.Contains(topo.Bindings(), BindingSpec{Queue: queue, Exchange: exchange, Key: key})
}

func TestDefaultTopologyExchanges(t *testing.T) {
	topo := DefaultTopology(TopologyOptions{})
	want := map[string]string{"umir.events": "topic", "umir.auth": "topic", "umir.tasks": "direct", "umir.dlx": "direct"}
	if len(topo.Exchanges) != len(want) {
		t.Fatalf("got %d exchanges, want %d", len(topo.Exchanges), len(want))
	}
	for _, e := range topo.Exchanges {
		if want[e.Name] != e.Kind {
			t.Fatalf("exchange %s kind %q, want %q", e.Name, e.Kind, want[e.Name])
		}
	}
}

func TestDefaultTopologyTaskQueues(t *testing.T) {
	topo := DefaultTopology(TopologyOptions{})
	for _, q := range []string{"auth.mail", "feedback.smtp", "feedback.telegram"} {
		work := queueByName(t, topo, q)
		if work.Args["x-queue-type"] != "quorum" {
			t.Fatalf("%s must be quorum", q)
		}
		if work.Args["x-delivery-limit"] != int64(5) {
			t.Fatalf("%s delivery limit: %v", q, work.Args["x-delivery-limit"])
		}
		if work.Args["x-overflow"] != "reject-publish" {
			t.Fatalf("%s overflow: %v", q, work.Args["x-overflow"])
		}
		// The broker dead-letters a work queue message only when x-delivery-limit
		// is hit (the consumer crashed without ack): that goes straight to .dlq.
		// Handler failures take the retry path by a publish of the consumer.
		if work.Args["x-dead-letter-exchange"] != "umir.dlx" || work.Args["x-dead-letter-routing-key"] != q+".dlq" {
			t.Fatalf("%s dead-letter target: %v", q, work.Args)
		}
		for _, name := range []string{q, q + ".retry"} {
			// at-least-once dead-lettering needs a quorum queue with reject-publish.
			if got := queueByName(t, topo, name).Args["x-dead-letter-strategy"]; got != "at-least-once" {
				t.Fatalf("%s dead-letter strategy: %v", name, got)
			}
		}
		retry := queueByName(t, topo, q+".retry")
		if retry.Args["x-message-ttl"] != int64(30000) {
			t.Fatalf("%s.retry ttl: %v", q, retry.Args["x-message-ttl"])
		}
		if retry.Args["x-dead-letter-exchange"] != "umir.dlx" || retry.Args["x-dead-letter-routing-key"] != q {
			t.Fatalf("%s.retry dead-letter target: %v", q, retry.Args)
		}
		if retry.Args["x-overflow"] != "reject-publish" || retry.Args["x-queue-type"] != "quorum" {
			t.Fatalf("%s.retry args: %v", q, retry.Args)
		}
		dlq := queueByName(t, topo, q+".dlq")
		if dlq.Args["x-queue-type"] != "quorum" {
			t.Fatalf("%s.dlq must be quorum", q)
		}
		if _, ok := dlq.Args["x-dead-letter-exchange"]; ok {
			t.Fatalf("%s.dlq must not dead-letter further", q)
		}
		for _, key := range []string{q, q + ".retry", q + ".dlq"} {
			bound := q
			if key != q {
				bound = key
			}
			if !hasBinding(topo, bound, "umir.dlx", key) {
				t.Fatalf("umir.dlx key %s is not bound to %s", key, bound)
			}
		}
		if !hasBinding(topo, q, "umir.tasks", q) {
			t.Fatalf("umir.tasks key %s is not bound to %s", q, q)
		}
	}
}

func TestDefaultTopologyFeedbackProfile(t *testing.T) {
	without := DefaultTopology(TopologyOptions{})
	for _, q := range without.Queues() {
		if q.Name == "feedback.auth" || q.Name == "feedback.auth.retry" || q.Name == "feedback.auth.dlq" {
			t.Fatalf("feedback.auth must be absent without the Feedback profile, found %s", q.Name)
		}
	}
	with := DefaultTopology(TopologyOptions{Feedback: true})
	for _, name := range []string{"feedback.auth", "feedback.auth.retry", "feedback.auth.dlq"} {
		queueByName(t, with, name)
	}
	for _, key := range []string{"auth.restaurant.deleted", "auth.organization.deleted"} {
		if !hasBinding(with, "feedback.auth", "umir.auth", key) {
			t.Fatalf("feedback.auth is not bound to umir.auth with %s", key)
		}
	}
	if hasBinding(with, "feedback.auth", "umir.auth", "auth.restaurant.deleting") {
		t.Fatal("feedback.auth must handle only the deleted events")
	}
}

func TestDefaultTopologyHasNoFanoutQueues(t *testing.T) {
	for _, q := range DefaultTopology(TopologyOptions{Feedback: true}).Queues() {
		if len(q.Name) > 8 && (q.Name[:8] == "gateway." || q.Name[:8] == "serving.") {
			t.Fatalf("durable topology must not declare the exclusive queue %s", q.Name)
		}
	}
}

func TestTopologyRetryTTLOverride(t *testing.T) {
	topo := DefaultTopology(TopologyOptions{})
	topo.RetryTTL = 250 * time.Millisecond
	if got := queueByName(t, topo, "auth.mail.retry").Args["x-message-ttl"]; got != int64(250) {
		t.Fatalf("ttl: %v", got)
	}
	topo.DeliveryLimit = 3
	if got := queueByName(t, topo, "auth.mail").Args["x-delivery-limit"]; got != int64(3) {
		t.Fatalf("limit: %v", got)
	}
}

func TestTopologyValidate(t *testing.T) {
	good := DefaultTopology(TopologyOptions{Feedback: true})
	if err := good.Validate(); err != nil {
		t.Fatalf("default topology must be valid: %v", err)
	}
	bad := good
	bad.RetryTTL = 0
	if bad.Validate() == nil {
		t.Fatal("zero retry TTL must be invalid")
	}
	bad = good
	bad.Exchanges = []ExchangeSpec{{Name: "umir.events", Kind: "topic"}}
	if bad.Validate() == nil {
		t.Fatal("binding to an undeclared exchange must be invalid")
	}
}

func TestExclusiveQueues(t *testing.T) {
	live, err := GatewayLiveQueue("gateway-a")
	if err != nil {
		t.Fatal(err)
	}
	if live.Name != "gateway.live.gateway-a" || !slices.Equal(live.Bindings, []BindingSpec{{Queue: live.Name, Exchange: "umir.events", Key: "warehouse.#"}}) {
		t.Fatalf("live queue: %+v", live)
	}
	gAuth, err := GatewayAuthQueue("gateway-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(gAuth.Bindings) != 7 || gAuth.Name != "gateway.auth.gateway-a" {
		t.Fatalf("gateway auth queue: %+v", gAuth)
	}
	sAuth, err := ServingAuthQueue("serving_b")
	if err != nil {
		t.Fatal(err)
	}
	wantServing := []string{"auth.restaurant.deleting", "auth.restaurant.deleted", "auth.organization.deleting", "auth.organization.deleted", "auth.organization.settings_changed"}
	var got []string
	for _, b := range sAuth.Bindings {
		if b.Exchange != "umir.auth" {
			t.Fatalf("serving must bind only umir.auth, got %s", b.Exchange)
		}
		got = append(got, b.Key)
	}
	if !slices.Equal(got, wantServing) {
		t.Fatalf("serving keys: %v", got)
	}
	for _, bad := range []string{"", "a.b", "a b", "a/b", "тест"} {
		if _, err := GatewayLiveQueue(bad); err == nil {
			t.Fatalf("instance %q must be rejected", bad)
		}
	}
}
