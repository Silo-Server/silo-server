package cache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
)

func TestRedisHookPreservesFailuresAndBoundsCommands(t *testing.T) {
	h := redisHook{role: "checks"}
	ctx := t.Context()
	for i := range 200 {
		cmd := redis.NewCmd(ctx, fmt.Sprintf("secret-command-%d", i), "secret-key")
		if redisOperation(cmd.Name()) != "other" {
			t.Fatal("arbitrary command became a label")
		}
		got := h.ProcessHook(func(context.Context, redis.Cmder) error { return redis.Nil })(ctx, cmd)
		if !errors.Is(got, redis.Nil) {
			t.Fatal("hook changed missing-key behavior")
		}
	}
	want := errors.New("key private-password connection refused")
	if got := h.ProcessHook(func(context.Context, redis.Cmder) error { return want })(ctx, redis.NewCmd(ctx, "get", "key")); !errors.Is(got, want) {
		t.Fatal("hook changed error identity")
	}
}

func TestRedisMetricsOutageAndRecovery(t *testing.T) {
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("SILO_TEST_REDIS_URL required for Redis operation integration")
	}
	client, err := NewRedisClientForRole(config.RedisConfig{URL: endpoint}, "checks")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseRedisClient(client) })
	key := fmt.Sprintf("observability-fixture-%d", time.Now().UnixNano())
	miss := testutil.ToFloat64(cacheLookups.WithLabelValues("checks", "miss"))
	if err := client.Get(t.Context(), key).Err(); !errors.Is(err, redis.Nil) {
		t.Fatalf("GET absent: %v", err)
	}
	if testutil.ToFloat64(cacheLookups.WithLabelValues("checks", "miss")) != miss+1 {
		t.Fatal("cache miss not counted")
	}
	if err := client.Set(t.Context(), key, "fixture", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Del(context.Background(), key) })
	hit := testutil.ToFloat64(cacheLookups.WithLabelValues("checks", "hit"))
	if got, err := client.Get(t.Context(), key).Result(); err != nil || got != "fixture" {
		t.Fatalf("GET recovery: %q %v", got, err)
	}
	if testutil.ToFloat64(cacheLookups.WithLabelValues("checks", "hit")) != hit+1 {
		t.Fatal("cache hit not counted")
	}
	before := client.PoolStats().Hits
	if n := testutil.CollectAndCount(redisPools); n == 0 {
		t.Fatal("missing pool metrics")
	}
	if client.PoolStats().Hits != before {
		t.Fatal("scrape performed a Redis operation")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := client.Get(ctx, key).Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation changed: %v", err)
	}
	dead := instrumentRedis(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 30 * time.Millisecond, MaxRetries: -1}), "checks")
	defer func() { _ = CloseRedisClient(dead) }()
	if err := dead.Ping(t.Context()).Err(); err == nil {
		t.Fatal("unreachable dependency succeeded")
	}
}

func TestRedisPoolSaturationAndRecovery(t *testing.T) {
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("SILO_TEST_REDIS_URL required for Redis pool saturation")
	}
	opts, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	opts.PoolSize, opts.MaxActiveConns, opts.MaxRetries = 1, 1, -1
	opts.PoolTimeout = 40 * time.Millisecond
	client := instrumentRedis(redis.NewClient(opts), "checks")
	defer func() { _ = CloseRedisClient(client) }()
	pinned := client.Conn()
	defer func() { _ = pinned.Close() }()
	if err := pinned.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(t.Context()).Err(); !errors.Is(err, redis.ErrPoolTimeout) {
		t.Fatalf("saturated pool error %v", err)
	}
	if client.PoolStats().Timeouts == 0 {
		t.Fatal("saturation not visible in pool snapshot")
	}
	if !errors.Is(redisObservationError(redis.ErrPoolTimeout), context.DeadlineExceeded) {
		t.Fatal("pool timeout counted as generic dependency error")
	}
	if err := pinned.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("pool did not recover: %v", err)
	}
}
