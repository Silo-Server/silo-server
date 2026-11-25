package cache

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/redis/go-redis/v9"
)

type commandHook struct {
	process func(redis.Cmder) error
}

func (h commandHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h commandHook) ProcessHook(_ redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error { return h.process(cmd) }
}
func (h commandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestAPIReplicaPresenceSeparatesIdenticalNodeNames(t *testing.T) {
	client := redis.NewClient(&redis.Options{})
	t.Cleanup(func() { _ = client.Close() })
	first := NewAPIReplicaPresence(client, "shared-api-name")
	second := NewAPIReplicaPresence(client, "shared-api-name")
	if first.ID() == second.ID() {
		t.Fatal("replicas with the same node name share a presence marker")
	}
}

func TestAPIReplicaPresenceRegisterRenewsAfterFailedFirstWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := redis.NewClient(&redis.Options{})
		t.Cleanup(func() { _ = client.Close() })
		var down atomic.Bool
		down.Store(true)
		var writes atomic.Int32
		client.AddHook(commandHook{process: func(cmd redis.Cmder) error {
			if cmd.Name() != "evalsha" {
				return nil
			}
			if down.Load() {
				return errors.New("redis down")
			}
			writes.Add(1)
			cmd.(*redis.Cmd).SetVal(int64(1))
			return nil
		}})
		ctx, cancel := context.WithCancel(t.Context())
		presence := NewAPIReplicaPresence(client, "api")
		if err := presence.Register(ctx); err == nil {
			t.Fatal("Register reported no error while Redis was down")
		}
		down.Store(false)
		time.Sleep(apiReplicaRefreshInterval + time.Second) //nolint:forbidigo // Advance synctest virtual time past one renewal interval.
		synctest.Wait()
		if got := writes.Load(); got != 1 {
			t.Fatalf("marker writes after Redis recovered = %d, want 1", got)
		}
		cancel()
		synctest.Wait()
	})
}

// contextHook runs process with the command's context, so a test can stall a
// command until that context ends, as a deadline-enabled client does.
type contextHook struct {
	process func(context.Context, redis.Cmder) error
}

func (h contextHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h contextHook) ProcessHook(_ redis.ProcessHook) redis.ProcessHook {
	return h.process
}
func (h contextHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// A presence write that Redis never answers ends at its own bound, so the
// synchronous first write cannot hold API startup open.
func TestAPIReplicaPresenceRegisterBoundsStalledWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
		t.Cleanup(func() { _ = client.Close() })
		client.AddHook(contextHook{process: func(ctx context.Context, cmd redis.Cmder) error {
			if cmd.Name() != "evalsha" {
				return nil
			}
			<-ctx.Done()
			cmd.SetErr(ctx.Err())
			return ctx.Err()
		}})
		ctx, cancel := context.WithCancel(t.Context())
		defer func() {
			cancel()
			synctest.Wait()
		}()
		presence := NewAPIReplicaPresence(client, "api")
		start := time.Now()
		done := make(chan error, 1)
		go func() { done <- presence.Register(ctx) }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("Register reported no error for a write that never completed")
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("Register returned after %s, want a bound of at most 5s", elapsed)
			}
		case <-time.After(time.Minute):
			t.Fatal("Register still blocked on a stalled write after a minute")
		}
	})
}

// A marker write that starts after withdrawal is dropped: neither Refresh nor a
// renewal puts the marker back once the Register context has ended.
func TestAPIReplicaPresenceDoesNotRewriteMarkerAfterWithdrawal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
		t.Cleanup(func() { _ = client.Close() })
		var mu sync.Mutex
		var ops []string
		client.AddHook(commandHook{process: func(cmd redis.Cmder) error {
			mu.Lock()
			defer mu.Unlock()
			ops = append(ops, cmd.Name())
			if c, ok := cmd.(*redis.Cmd); ok {
				c.SetVal(int64(1))
			}
			return nil
		}})
		ctx, cancel := context.WithCancel(t.Context())
		presence := NewAPIReplicaPresence(client, "api")
		if err := presence.Register(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
		synctest.Wait()
		if err := presence.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(apiReplicaRefreshInterval + time.Second) //nolint:forbidigo // Advance synctest virtual time past one renewal interval.
		synctest.Wait()
		mu.Lock()
		defer mu.Unlock()
		if want := []string{"evalsha", "zrem"}; !slices.Equal(ops, want) {
			t.Fatalf("Redis commands = %v, want %v: no write after the withdrawal delete", ops, want)
		}
	})
}
