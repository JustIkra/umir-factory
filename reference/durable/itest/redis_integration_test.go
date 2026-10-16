//go:build integration

package itest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

func openRedis(t *testing.T, raw string) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatalf("parse redis url")
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return c
}

func TestRedisPrefixRemovesOnlyOwnKeys(t *testing.T) {
	for name, raw := range map[string]string{"limiter": RedisLimiterURL(t), "cache": RedisCacheURL(t)} {
		t.Run(name, func(t *testing.T) {
			c := openRedis(t, raw)
			ctx := t.Context()
			foreign := RedisPrefixOn(t, raw) + "foreign" // another test's prefix: must survive
			t.Cleanup(func() {
				if err := c.Del(context.Background(), foreign).Err(); err != nil {
					t.Errorf("remove foreign probe: %v", err)
				}
			})
			if err := c.Set(ctx, foreign, "keep", 0).Err(); err != nil {
				t.Fatalf("set foreign: %v", err)
			}
			var prefix string
			t.Run("use", func(t *testing.T) {
				prefix = RedisPrefixOn(t, raw)
				if !strings.HasPrefix(prefix, "t:factory:") || !strings.HasSuffix(prefix, ":") {
					t.Fatalf("unexpected prefix %q", prefix)
				}
				// More keys than one SCAN batch, to exercise the cursor loop.
				for i := range 450 {
					if err := c.Set(ctx, fmt.Sprintf("%sk%d", prefix, i), i, 0).Err(); err != nil {
						t.Fatalf("set: %v", err)
					}
				}
			})
			left, _, err := c.Scan(ctx, 0, prefix+"*", 1000).Result()
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if len(left) != 0 {
				t.Fatalf("%d keys under %s survived the cleanup", len(left), prefix)
			}
			if got, err := c.Get(ctx, foreign).Result(); err != nil || got != "keep" {
				t.Fatalf("foreign key must survive: value=%q err=%v", got, err)
			}
		})
	}
}

func TestRemovePrefixRefusesForeignPrefixes(t *testing.T) {
	for _, prefix := range []string{"", "t:", "t:factory:", "umir:", "t:other:12345678:"} {
		if err := removePrefix(context.Background(), RedisLimiterURL(t), prefix); err == nil {
			t.Fatalf("removePrefix accepted %q", prefix)
		}
	}
}
