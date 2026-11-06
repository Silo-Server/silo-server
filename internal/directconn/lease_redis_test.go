package directconn

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type leaseCommandHook struct {
	process func(redis.Cmder) error
}

func (h leaseCommandHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h leaseCommandHook) ProcessHook(_ redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error { return h.process(cmd) }
}
func (h leaseCommandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestEngineLeaseRenewalDeadline(t *testing.T) {
	client := redis.NewClient(&redis.Options{Dialer: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unexpected network call")
	}})
	t.Cleanup(func() { _ = client.Close() })
	failed := false
	client.AddHook(leaseCommandHook{process: func(cmd redis.Cmder) error {
		if failed {
			return errors.New("redis unavailable")
		}
		switch cmd := cmd.(type) {
		case *redis.BoolCmd:
			cmd.SetVal(true)
		case *redis.Cmd:
			cmd.SetVal(int64(1))
		default:
			t.Fatalf("unexpected command %T", cmd)
		}
		return nil
	}})
	lease := &redisEngineLease{client: client, id: "process"}
	now := time.Now()
	if ok, err := lease.acquire(t.Context(), now); err != nil || !ok {
		t.Fatalf("acquire = %v, %v; want true, nil", ok, err)
	}
	failed = true
	if _, err := lease.renew(t.Context(), now.Add(30*time.Second)); err == nil {
		t.Fatal("renew should fail")
	}
	if !lease.held(now.Add(59*time.Second)) || lease.held(now.Add(60*time.Second)) {
		t.Fatal("failed renewal must preserve the lease before 60s and stop at 60s")
	}
	failed = false
	if ok, err := lease.renew(t.Context(), now.Add(60*time.Second)); err != nil || !ok {
		t.Fatalf("renew = %v, %v", ok, err)
	}
	if !lease.held(now.Add(119*time.Second)) || lease.held(now.Add(120*time.Second)) {
		t.Fatal("successful renewal must move the self-stop deadline to 120s")
	}
}

// TestRedisEngineLease runs the lease scripts against miniredis. FastForward
// stands in for the 90-second TTL expiry.
func TestRedisEngineLease(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := t.Context()
	first := &redisEngineLease{client: client, id: uuid.NewString()}
	second := &redisEngineLease{client: client, id: uuid.NewString()}
	if ok, err := first.acquire(ctx, time.Now()); err != nil || !ok {
		t.Fatalf("first acquire = %v, %v", ok, err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := first.release(cleanup); err != nil {
			t.Error(err)
		}
		if err := second.release(cleanup); err != nil {
			t.Error(err)
		}
	})
	assertRedisLeaseOwner(t, ctx, client, first.id)
	if ok, err := second.acquire(ctx, time.Now()); err != nil || ok {
		t.Fatalf("second acquire = %v, %v; want false, nil", ok, err)
	}
	ttlBefore, err := client.PTTL(ctx, engineLeaseKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := second.renew(ctx, time.Now()); err != nil || ok {
		t.Fatalf("nonholder renew = %v, %v; want false, nil", ok, err)
	}
	if ttlAfter, err := client.PTTL(ctx, engineLeaseKey).Result(); err != nil || ttlAfter > ttlBefore {
		t.Fatalf("nonholder changed TTL: before %s, after %s, error %v", ttlBefore, ttlAfter, err)
	}
	if err := second.release(ctx); err != nil {
		t.Fatal(err)
	}
	assertRedisLeaseOwner(t, ctx, client, first.id)
	if ok, err := first.renew(ctx, time.Now()); err != nil || !ok {
		t.Fatalf("holder renew = %v, %v; want true, nil", ok, err)
	}
	if err := first.release(ctx); err != nil {
		t.Fatal(err)
	}
	if exists, err := client.Exists(ctx, engineLeaseKey).Result(); err != nil || exists != 0 {
		t.Fatalf("released key exists = %d, %v", exists, err)
	}
	if ok, err := first.acquire(ctx, time.Now()); err != nil || !ok {
		t.Fatalf("reacquire = %v, %v", ok, err)
	}
	mr.FastForward(engineLeaseTTL)
	if ok, err := second.acquire(ctx, time.Now()); err != nil || !ok {
		t.Fatalf("acquire after expiry = %v, %v; want true, nil", ok, err)
	}
	assertRedisLeaseOwner(t, ctx, client, second.id)
}

func assertRedisLeaseOwner(t *testing.T, ctx context.Context, client *redis.Client, want string) {
	t.Helper()
	if got, err := client.Get(ctx, engineLeaseKey).Result(); err != nil || got != want {
		t.Fatalf("lease owner = %q, %v; want %q", got, err, want)
	}
}
