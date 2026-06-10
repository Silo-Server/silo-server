package presence

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/15"
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Skipf("redis url parse: %v", err)
	}
	c := redis.NewClient(opt)
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	return c
}

func TestRedisRegistry_ConnectedAndRelease(t *testing.T) {
	c := testRedis(t)
	ctx := context.Background()
	c.FlushDB(ctx)
	r := NewRedisRegistry(c, 60*time.Second)

	if r.Connected(ctx, 42) {
		t.Fatal("absent initially")
	}
	rel := r.Add(ctx, 42)
	if !r.Connected(ctx, 42) {
		t.Fatal("present after Add")
	}
	rel()
	if r.Connected(ctx, 42) {
		t.Fatal("absent after release")
	}
}

func TestRedisRegistry_RefcountTwoConnections(t *testing.T) {
	c := testRedis(t)
	ctx := context.Background()
	c.FlushDB(ctx)
	r := NewRedisRegistry(c, 60*time.Second)
	rel1 := r.Add(ctx, 7)
	rel2 := r.Add(ctx, 7)
	rel1()
	if !r.Connected(ctx, 7) {
		t.Fatal("one connection remains")
	}
	rel2()
	if r.Connected(ctx, 7) {
		t.Fatal("absent after both released")
	}
}

func TestRedisRegistry_ImplementsInterface(t *testing.T) {
	var _ Registry = (*RedisRegistry)(nil)
}
