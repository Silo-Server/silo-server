package ratelimit

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// This hook exercises the actual Redis adapter without opening a connection.
// It establishes command/result mapping, not execution or atomicity of Lua.
type workRedisHook struct {
	t       *testing.T
	process func(context.Context, redis.Cmder) error
}

func (h workRedisHook) DialHook(redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) {
		h.t.Error("stub adapter attempted a network connection")
		return nil, errors.New("network forbidden in work adapter tests")
	}
}

func (h workRedisHook) ProcessHook(redis.ProcessHook) redis.ProcessHook { return h.process }

func (h workRedisHook) ProcessPipelineHook(redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(context.Context, []redis.Cmder) error {
		h.t.Error("unexpected pipeline")
		return errors.New("pipeline forbidden in work adapter tests")
	}
}

func stubWorkRedis(t *testing.T, process func(context.Context, redis.Cmder) error) WorkStore {
	t.Helper()
	client := redis.NewClient(&redis.Options{})
	client.AddHook(workRedisHook{t, process})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisWorkStore(client)
}

func TestWorkRedisStrictResultMapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  any
		want WorkResult
	}{
		{"allowed", []any{int64(1), int64(0)}, WorkResult{Allowed: true}},
		{"denied", []any{int64(0), int64(1234)}, WorkResult{RetryAfter: 1234 * time.Millisecond}},
		{"unavailable", []any{int64(-1), int64(0)}, workUnavailable()},
		{"nil", nil, workUnavailable()},
		{"missing", []any{int64(1)}, workUnavailable()},
		{"extra", []any{int64(1), int64(0), int64(0)}, workUnavailable()},
		{"string flag", []any{"1", int64(0)}, workUnavailable()},
		{"string retry", []any{int64(0), "1"}, workUnavailable()},
		{"unknown flag", []any{int64(2), int64(0)}, workUnavailable()},
		{"ambiguous", []any{int64(1), int64(1)}, workUnavailable()},
		{"missing delay", []any{int64(0), int64(0)}, workUnavailable()},
		{"negative delay", []any{int64(0), int64(-1)}, workUnavailable()},
		{"overflow delay", []any{int64(0), int64(1 << 62)}, workUnavailable()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := stubWorkRedis(t, func(_ context.Context, cmd redis.Cmder) error {
				want := []any{"evalsha", workScript.Hash(), 1, workRedisPrefix + "synthetic", 1, 2}
				if !reflect.DeepEqual(cmd.Args(), want) {
					t.Fatalf("command = %#v, want %#v", cmd.Args(), want)
				}
				cmd.(*redis.Cmd).SetVal(tc.raw)
				return nil
			})
			if got := store.AllowWork(t.Context(), "synthetic", WorkLimit{1, 2}); got != tc.want {
				t.Fatalf("result = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestWorkRedisFailureCancellationAndRecovery(t *testing.T) {
	limit := WorkLimit{1, 2}
	if got := NewRedisWorkStore(nil).AllowWork(t.Context(), "synthetic", limit); !got.Unavailable || got.Allowed {
		t.Fatalf("nil Redis granted fallback: %+v", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	store := stubWorkRedis(t, func(_ context.Context, cmd redis.Cmder) error {
		calls++
		if calls == 1 {
			return errors.New("synthetic store failure")
		}
		cmd.(*redis.Cmd).SetVal([]any{int64(1), int64(0)})
		if calls == 2 {
			cancel() // Ambiguous: the store may have spent the permit.
		}
		return nil
	})
	if got := store.AllowWork(ctx, "synthetic", limit); !got.Unavailable || got.Allowed {
		t.Fatalf("error granted work: %+v", got)
	}
	if got := store.AllowWork(ctx, "synthetic", limit); !got.Unavailable || got.Allowed {
		t.Fatalf("canceled completion granted work: %+v", got)
	}
	store.AllowWork(ctx, "synthetic", limit)
	store.AllowWork(t.Context(), "synthetic", WorkLimit{})
	store.AllowWork(t.Context(), "", limit)
	if calls != 2 {
		t.Fatalf("canceled/invalid input reached store: %d calls", calls)
	}
	requireWork(t, store.AllowWork(t.Context(), "synthetic", limit), true, 0)
}
