package cache

import (
	"context"
	"errors"
	"fmt"
	"testing"

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
