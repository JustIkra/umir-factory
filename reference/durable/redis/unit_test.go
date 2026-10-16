package redis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validRequest() Request {
	return Request{
		ID:          "cmd-1",
		Fingerprint: "fp-1",
		Policy:      "feedback.anon",
		Dimensions: []Dimension{
			{Key: "client:abc", Limit: 5, Window: time.Minute},
			{Key: "global", Limit: 100, Window: time.Hour},
		},
	}
}

func TestRequestValidate(t *testing.T) {
	if err := validRequest().validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	tests := map[string]func(*Request){
		"empty id":            func(r *Request) { r.ID = "" },
		"long id":             func(r *Request) { r.ID = strings.Repeat("я", MaxIDLength+1) },
		"id not utf8":         func(r *Request) { r.ID = "a\xffb" },
		"empty fingerprint":   func(r *Request) { r.Fingerprint = "" },
		"long fingerprint":    func(r *Request) { r.Fingerprint = strings.Repeat("f", MaxFingerprintLength+1) },
		"empty policy":        func(r *Request) { r.Policy = "" },
		"policy with colon":   func(r *Request) { r.Policy = "a:b" },
		"policy with space":   func(r *Request) { r.Policy = "a b" },
		"long policy":         func(r *Request) { r.Policy = strings.Repeat("p", MaxPolicyLength+1) },
		"no dimensions":       func(r *Request) { r.Dimensions = nil },
		"too many dimensions": func(r *Request) { r.Dimensions = make([]Dimension, MaxDimensions+1) },
		"empty key":           func(r *Request) { r.Dimensions[0].Key = "" },
		"key with space":      func(r *Request) { r.Dimensions[0].Key = "a b" },
		"key with newline":    func(r *Request) { r.Dimensions[0].Key = "a\nb" },
		"long key":            func(r *Request) { r.Dimensions[0].Key = strings.Repeat("k", MaxKeyLength+1) },
		"zero limit":          func(r *Request) { r.Dimensions[0].Limit = 0 },
		"negative limit":      func(r *Request) { r.Dimensions[0].Limit = -1 },
		"zero window":         func(r *Request) { r.Dimensions[0].Window = 0 },
		"sub-millisecond":     func(r *Request) { r.Dimensions[0].Window = time.Microsecond },
		"huge window":         func(r *Request) { r.Dimensions[0].Window = MaxWindow + time.Millisecond },
		"duplicate key":       func(r *Request) { r.Dimensions[1].Key = r.Dimensions[0].Key },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := validRequest()
			if name == "too many dimensions" {
				for i := range r.Dimensions {
					r.Dimensions[i] = Dimension{Key: "k" + string(rune('a'+i)), Limit: 1, Window: time.Second}
				}
			}
			mutate(&r)
			err := r.validate()
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("got %v, want ErrInvalidRequest", err)
			}
		})
	}
	t.Run("max length id in code points", func(t *testing.T) {
		r := validRequest()
		r.ID = strings.Repeat("я", MaxIDLength)
		if err := r.validate(); err != nil {
			t.Fatalf("256 code points rejected: %v", err)
		}
	})
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadURL(t *testing.T) {
	const secret = "redis://:s3cr3t-value@127.0.0.1:6379/0"
	dir := t.TempDir()
	file := filepath.Join(dir, "url")
	if err := os.WriteFile(file, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("http://s3cr3t-value@host"), 0o600); err != nil {
		t.Fatal(err)
	}

	ok := []struct {
		name string
		env  map[string]string
		kind Kind
	}{
		{"limiter direct", map[string]string{"GATEWAY_REDIS_LIMITER_URL": secret}, KindLimiter},
		{"limiter file", map[string]string{"GATEWAY_REDIS_LIMITER_URL_FILE": file}, KindLimiter},
		{"cache direct", map[string]string{"WAREHOUSE_REDIS_CACHE_URL": secret}, KindCache},
		{"cache file", map[string]string{"WAREHOUSE_REDIS_CACHE_URL_FILE": file}, KindCache},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			prefix := "GATEWAY"
			if tc.kind == KindCache {
				prefix = "WAREHOUSE"
			}
			got, err := LoadURL(envOf(tc.env), prefix, tc.kind)
			if err != nil || got != secret {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}

	failures := []struct {
		name string
		env  map[string]string
		kind Kind
	}{
		{"none", map[string]string{}, KindLimiter},
		{"both", map[string]string{"GATEWAY_REDIS_LIMITER_URL": secret, "GATEWAY_REDIS_LIMITER_URL_FILE": file}, KindLimiter},
		{"other kind only", map[string]string{"GATEWAY_REDIS_CACHE_URL": secret}, KindLimiter},
		{"missing file", map[string]string{"GATEWAY_REDIS_LIMITER_URL_FILE": filepath.Join(dir, "absent")}, KindLimiter},
		{"empty file", map[string]string{"GATEWAY_REDIS_LIMITER_URL_FILE": empty}, KindLimiter},
		{"bad scheme", map[string]string{"GATEWAY_REDIS_LIMITER_URL": "http://s3cr3t-value@host"}, KindLimiter},
		{"bad scheme in file", map[string]string{"GATEWAY_REDIS_LIMITER_URL_FILE": bad}, KindLimiter},
		{"unknown kind", map[string]string{"GATEWAY_REDIS_LIMITER_URL": secret}, Kind(99)},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LoadURL(envOf(tc.env), "GATEWAY", tc.kind)
			if err == nil || got != "" {
				t.Fatalf("got %q, %v; want an error", got, err)
			}
			if strings.Contains(err.Error(), "s3cr3t-value") || strings.Contains(err.Error(), dir) {
				t.Fatalf("error leaks a value: %v", err)
			}
		})
	}
	if _, err := LoadURL(envOf(nil), "", KindLimiter); err == nil {
		t.Fatal("empty prefix accepted")
	}
	if _, err := LoadURL(nil, "GATEWAY", KindLimiter); err == nil {
		t.Fatal("nil getenv accepted")
	}
}

func TestConfigValidation(t *testing.T) {
	bad := map[string]Config{
		"empty url":        {},
		"not redis scheme": {URL: "http://127.0.0.1:6379"},
		"negative timeout": {URL: "redis://127.0.0.1:6379", Timeout: -time.Second},
		"negative pool":    {URL: "redis://127.0.0.1:6379", PoolSize: -1},
		"root no colon":    {URL: "redis://127.0.0.1:6379", KeyRoot: "t:abc"},
		"root with space":  {URL: "redis://127.0.0.1:6379", KeyRoot: "t abc:"},
	}
	for name, cfg := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := newClient(cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("got %v, want ErrInvalidConfig", err)
			}
		})
	}
	secret := Config{URL: "http://:s3cr3t-value@host"}
	if _, err := newClient(secret); err == nil || strings.Contains(err.Error(), "s3cr3t-value") {
		t.Fatalf("error must not carry the URL: %v", err)
	}
	c, err := newClient(Config{URL: "redis://127.0.0.1:1/0", KeyRoot: "t:x:"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient(t, c)
	for _, owner := range []string{"", "GW", "a:b", "a b", strings.Repeat("a", 33)} {
		if _, err := c.Limiter(owner); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("Limiter(%q): %v", owner, err)
		}
		if _, err := c.Lease(owner); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("Lease(%q): %v", owner, err)
		}
		if _, err := c.Slots(owner); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("Slots(%q): %v", owner, err)
		}
	}
	for _, w := range []time.Duration{-1, 2 * time.Hour} {
		if _, err := c.Continuity(ContinuityConfig{Window: w}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("Continuity window %v: %v", w, err)
		}
	}
}

func closeClient(tb testing.TB, c *Client) {
	tb.Helper()
	if err := c.Close(); err != nil {
		tb.Errorf("close: %v", err)
	}
}

// deadClient points at a port nobody listens on.
func deadClient(t *testing.T) *Client {
	t.Helper()
	c, err := newClient(Config{URL: "redis://127.0.0.1:1/0", Timeout: 300 * time.Millisecond, KeyRoot: "t:dead:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeClient(t, c) })
	return c
}

func TestOpenFailsWithoutServer(t *testing.T) {
	_, err := Open(context.Background(), Config{URL: "redis://:s3cr3t-value@127.0.0.1:1/0", Timeout: 300 * time.Millisecond})
	if err == nil {
		t.Fatal("Open succeeded without a server")
	}
	if strings.Contains(err.Error(), "s3cr3t-value") {
		t.Fatalf("error leaks the URL: %v", err)
	}
}

func TestUnavailableIsNeverAllowed(t *testing.T) {
	c := deadClient(t)
	ctx := context.Background()

	l, err := c.Limiter("gw")
	if err != nil {
		t.Fatal(err)
	}
	d, err := l.Admit(ctx, validRequest())
	if !errors.Is(err, ErrAdmissionUnavailable) || d.Allowed {
		t.Fatalf("Admit: %+v, %v", d, err)
	}
	// An invalid request is a caller bug, not an outage.
	if _, err := l.Admit(ctx, Request{}); !errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatalf("Admit(invalid): %v", err)
	}

	cont, err := c.Continuity(ContinuityConfig{Window: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cont.Check(ctx, "gen-1"); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatalf("Check: %v", err)
	}
	if _, err := cont.Check(ctx, ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Check(empty): %v", err)
	}

	lease, err := c.Lease("gw")
	if err != nil {
		t.Fatal(err)
	}
	if g, err := lease.Acquire(ctx, "expiry", "me", time.Second); err == nil || g.Acquired {
		t.Fatalf("Lease.Acquire: %+v, %v", g, err)
	}
	if ok, err := lease.Renew(ctx, "expiry", "me", time.Second); err == nil || ok {
		t.Fatalf("Lease.Renew: %v, %v", ok, err)
	}
	if err := lease.Release(ctx, "expiry", "me"); err == nil {
		t.Fatal("Lease.Release succeeded without a server")
	}

	slots, err := c.Slots("gw")
	if err != nil {
		t.Fatal(err)
	}
	req := SlotRequest{Holder: "h", TTL: time.Second, Dimensions: []SlotDimension{{Key: "sse:cell", MaxCount: 1}}}
	if g, err := slots.Acquire(ctx, req); err == nil || g.Granted {
		t.Fatalf("Slots.Acquire: %+v, %v", g, err)
	}
}

func TestSlotRequestValidate(t *testing.T) {
	good := SlotRequest{Holder: "h", TTL: time.Second, Dimensions: []SlotDimension{{Key: "sse:s:abc", MaxCount: 8}}}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*SlotRequest){
		"empty holder":    func(r *SlotRequest) { r.Holder = "" },
		"long holder":     func(r *SlotRequest) { r.Holder = strings.Repeat("h", MaxKeyLength+1) },
		"short ttl":       func(r *SlotRequest) { r.TTL = time.Microsecond },
		"no dimensions":   func(r *SlotRequest) { r.Dimensions = nil },
		"empty key":       func(r *SlotRequest) { r.Dimensions[0].Key = "" },
		"key with space":  func(r *SlotRequest) { r.Dimensions[0].Key = "a b" },
		"no limit":        func(r *SlotRequest) { r.Dimensions[0].MaxCount = 0 },
		"negative count":  func(r *SlotRequest) { r.Dimensions[0].MaxCount = -1 },
		"negative units":  func(r *SlotRequest) { r.Dimensions[0].Units = -1 },
		"units over max":  func(r *SlotRequest) { r.Dimensions[0].MaxUnits, r.Dimensions[0].Units = 10, 11 },
		"units no max":    func(r *SlotRequest) { r.Dimensions[0].Units = 1 },
		"duplicate key":   func(r *SlotRequest) { r.Dimensions = append(r.Dimensions, r.Dimensions[0]) },
		"reserved suffix": func(r *SlotRequest) { r.Dimensions[0].Key = "sse:cell" + unitsSuffix },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := good
			r.Dimensions = append([]SlotDimension(nil), good.Dimensions...)
			mutate(&r)
			if err := r.validate(); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("got %v, want ErrInvalidRequest", err)
			}
		})
	}
}
