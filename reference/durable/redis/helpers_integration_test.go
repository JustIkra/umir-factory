//go:build integration

package redis

import (
	"errors"
	"net"
	"net/url"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/JustIkra/umir-factory/reference/durable/itest"
)

const testTimeout = 5 * time.Second

// env is one test client on the limiter Redis under a private key prefix.
type env struct {
	c    *Client
	rdb  *goredis.Client // same connection pool as c
	root string          // itest prefix; every key of the test starts with it
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvOn(t, itest.RedisLimiterURL(t))
}

func newEnvOn(t *testing.T, rawURL string) *env {
	t.Helper()
	root := itest.RedisPrefixOn(t, rawURL)
	c, err := Open(t.Context(), Config{URL: rawURL, Timeout: testTimeout, PoolSize: 80, KeyRoot: root, ClientName: "umir-test"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { closeClient(t, c) })
	return &env{c: c, rdb: c.rdb, root: root}
}

// key returns the full key of a name below umir:<owner>:.
func (e *env) key(owner, name string) string { return e.root + "umir:" + owner + ":" + name }

func (e *env) pttl(t *testing.T, key string) time.Duration {
	t.Helper()
	d, err := e.rdb.PTTL(t.Context(), key).Result()
	if err != nil {
		t.Fatalf("pttl: %v", err)
	}
	return d
}

func (e *env) get(t *testing.T, key string) (string, bool) {
	t.Helper()
	v, err := e.rdb.Get(t.Context(), key).Result()
	if errors.Is(err, goredis.Nil) {
		return "", false
	}
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return v, true
}

func (e *env) exists(t *testing.T, key string) bool {
	t.Helper()
	n, err := e.rdb.Exists(t.Context(), key).Result()
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	return n > 0
}

// viaProxy returns rawURL with the host replaced by the proxy address, keeping
// the credentials.
func viaProxy(t *testing.T, rawURL string, p *itest.TCPProxy) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal("parse redis url")
	}
	_, port, err := net.SplitHostPort(p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	u.Host = net.JoinHostPort("127.0.0.1", port)
	return u.String()
}

func targetOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal("parse redis url")
	}
	return u.Host
}
