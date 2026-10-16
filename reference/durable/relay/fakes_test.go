package relay_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-factory/reference/durable/relay"
)

type markCall struct {
	Owner  string
	Fence  int64
	IDs    []string
	Routed bool
}

type releaseCall struct {
	Owner string
	Fence int64
	IDs   []string
	Delay time.Duration
}

// fakeStore is an in-memory OutboxStore: rows are claimed in order and
// released or published like the postgres outbox does, minus time.
type fakeStore struct {
	mu       sync.Mutex
	rows     []relay.Row
	state    map[string]string // "", "claimed", "published"
	fence    int64
	claims   int
	marks    []markCall
	releases []releaseCall
	markErr  func(markCall) error
	relErr   error
	claimErr error
	backlog  relay.Backlog
	statsErr error
}

func newFakeStore(rows ...relay.Row) *fakeStore {
	s := &fakeStore{state: map[string]string{}}
	for i := range rows {
		if rows[i].Position == 0 {
			rows[i].Position = int64(i + 1)
		}
		s.rows = append(s.rows, rows[i])
	}
	return s
}

func (s *fakeStore) Claim(_ context.Context, _ string, limit int, _ time.Duration) ([]relay.Row, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	if s.claimErr != nil {
		return nil, 0, s.claimErr
	}
	var out []relay.Row
	for i := range s.rows {
		if len(out) == limit {
			break
		}
		if s.state[s.rows[i].ID] != "" {
			continue
		}
		s.state[s.rows[i].ID] = "claimed"
		s.rows[i].Attempts++
		out = append(out, s.rows[i])
	}
	if len(out) == 0 {
		return nil, 0, nil
	}
	s.fence++
	return out, s.fence, nil
}

func (s *fakeStore) MarkPublished(_ context.Context, owner string, fence int64, ids []string, routed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := markCall{owner, fence, slices.Clone(ids), routed}
	s.marks = append(s.marks, call)
	if s.markErr != nil {
		if err := s.markErr(call); err != nil {
			return err
		}
	}
	for _, id := range ids {
		s.state[id] = "published"
	}
	return nil
}

func (s *fakeStore) Release(_ context.Context, owner string, fence int64, ids []string, delay time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases = append(s.releases, releaseCall{owner, fence, slices.Clone(ids), delay})
	if s.relErr != nil {
		return s.relErr
	}
	for _, id := range ids {
		if s.state[id] == "claimed" {
			s.state[id] = ""
		}
	}
	return nil
}

func (s *fakeStore) Stats(context.Context) (relay.Backlog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backlog, s.statsErr
}

func (s *fakeStore) snapshot() (marks []markCall, releases []releaseCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.marks), slices.Clone(s.releases)
}

func (s *fakeStore) stateOf(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state[id]
}

// fakePub publishes through script; nil script confirms everything.
type fakePub struct {
	mu     sync.Mutex
	msgs   []relay.Message
	script func(call int, ctx context.Context, m relay.Message) (relay.Outcome, error)
}

func (p *fakePub) Publish(ctx context.Context, m relay.Message) (relay.Outcome, error) {
	p.mu.Lock()
	p.msgs = append(p.msgs, m)
	call := len(p.msgs)
	script := p.script
	p.mu.Unlock()
	if script == nil {
		return relay.Confirmed, nil
	}
	return script(call, ctx, m)
}

func (p *fakePub) messages() []relay.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.msgs)
}

func (p *fakePub) ids() []string {
	var out []string
	for _, m := range p.messages() {
		out = append(out, m.MessageID)
	}
	return out
}

// eventRow builds a valid warehouse event row; its outbox ID is "row<n>" and
// its eventID "evt<n>".
func eventRow(n int) relay.Row {
	return relay.Row{
		ID:           fmt.Sprintf("row%d", n),
		Position:     int64(n),
		Exchange:     "umir.events",
		RoutingKey:   "warehouse.OrderPlaced",
		Headers:      map[string]string{"x-umir-schema": "1", "x-umir-restaurant-id": "r1", "x-umir-generation": "g1"},
		Body:         []byte(fmt.Sprintf(`{"eventID":"evt%d","schemaVersion":1,"generation":"g1"}`, n)),
		OrgID:        "o1",
		RestaurantID: "r1",
	}
}

func taskRow(n int) relay.Row {
	return relay.Row{
		ID:         fmt.Sprintf("row%d", n),
		Position:   int64(n),
		Exchange:   "umir.tasks",
		RoutingKey: "auth.mail",
		Headers:    map[string]string{"x-umir-schema": "1", "x-umir-generation": "g1"},
		Body:       []byte(fmt.Sprintf(`{"messageID":"msg%d","schemaVersion":1,"generation":"g1"}`, n)),
	}
}

// runCycles runs the relay until it has waited `cycles` times (one wait follows
// each idle or unfinished cycle), then cancels it and returns Run's result.
func runCycles(t *testing.T, r *relay.Relay, cycles int) error {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var mu sync.Mutex
	n := 0
	r.Sleep = func(ctx context.Context, _ time.Duration) error {
		mu.Lock()
		n++
		done := n >= cycles
		mu.Unlock()
		if done {
			cancel()
		}
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
		return nil
	}
}

func newRelay(store relay.OutboxStore, pub relay.Publisher) *relay.Relay {
	return &relay.Relay{Store: store, Pub: pub, Owner: "relay-a", Batch: 10, Lease: 30 * time.Second, Poll: 200 * time.Millisecond}
}
