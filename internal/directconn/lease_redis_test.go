package directconn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
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

// leaseOp names the Redis script a fake answers.
type leaseOp int

const (
	leaseOther leaseOp = iota
	leaseCensusTouch
	leaseCensusCount
	leaseAcquire
	leaseRenew
	leaseRelease
)

// classifyLease identifies a script command by its key and hash. Script.Run
// sends EVALSHA first, which the fakes answer without a NOSCRIPT fallback.
func classifyLease(cmd *redis.Cmd) leaseOp {
	args := cmd.Args()
	if len(args) < 4 {
		return leaseOther
	}
	switch fmt.Sprint(args[3]) {
	case "silo:api-replicas":
		if fmt.Sprint(args[len(args)-1]) == "touch" {
			return leaseCensusTouch
		}
		return leaseCensusCount
	case engineLeaseKey:
		switch fmt.Sprint(args[1]) {
		case acquireEngineLease.Hash():
			return leaseAcquire
		case renewEngineLease.Hash():
			return leaseRenew
		case releaseEngineLease.Hash():
			return leaseRelease
		}
	}
	return leaseOther
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

// A renewal pushes the expiry out: a second process is refused until the renewed
// TTL passes, not the original one.
func TestRedisEngineLeaseRenewalExtendsTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := t.Context()
	holder := &redisEngineLease{client: client, id: uuid.NewString()}
	other := &redisEngineLease{client: client, id: uuid.NewString()}
	if ok, err := holder.acquire(ctx, time.Now()); err != nil || !ok {
		t.Fatalf("acquire = %v, %v", ok, err)
	}
	mr.FastForward(60 * time.Second)
	if ok, err := holder.renew(ctx, time.Now()); err != nil || !ok {
		t.Fatalf("renew = %v, %v", ok, err)
	}
	// 120 seconds in: past the original 90-second TTL, inside the renewed one.
	mr.FastForward(60 * time.Second)
	if ok, err := other.acquire(ctx, time.Now()); err != nil || ok {
		t.Fatalf("second acquired = %v (err %v) 60s after a renewal; the renewal did not extend the lease", ok, err)
	}
	mr.FastForward(31 * time.Second)
	if ok, err := other.acquire(ctx, time.Now()); err != nil || !ok {
		t.Fatalf("second acquire after the renewed TTL = %v, %v; want true", ok, err)
	}
}

// A grant is one stretch of ownership. A confirmed acquisition opens it; an
// attempt that fails, even one whose write applied, opens nothing, so the retry
// that Redis confirms dates the grant. A renewal answered 0 and a release close it.
func TestRedisEngineLeaseGrantIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	var drop atomic.Bool
	drop.Store(true)
	client.AddHook(lostReplyHook{drop: &drop})
	// Load the script, so the commands reach the hook as EVALSHA.
	if _, err := acquireEngineLease.Load(t.Context(), client).Result(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	l := &redisEngineLease{client: client, id: uuid.NewString()}
	// The write applies and its reply is lost: no grant opens.
	if ok, err := l.acquire(ctx, time.Now()); err == nil || ok || l.grant != 0 || !l.grantStart.IsZero() || !l.lastRenew.IsZero() {
		t.Fatalf("acquire with a lost reply = %v, %v, grant %d, start %v; want false, an error, and no grant", ok, err, l.grant, l.grantStart)
	}
	assertRedisLeaseOwner(t, ctx, client, l.id)
	// The retry finds its own value, which counts as a confirmed acquisition.
	drop.Store(false)
	sent := time.Now()
	if ok, err := l.acquire(ctx, sent); err != nil || !ok || l.grant != 1 || !l.grantStart.Equal(sent) {
		t.Fatalf("retry = %v, %v, grant %d, start %v; want true, nil, 1, %v", ok, err, l.grant, l.grantStart, sent)
	}
	mr.FastForward(engineLeaseTTL)
	if ok, err := l.renew(ctx, time.Now()); err != nil || ok {
		t.Fatalf("renew after expiry = %v, %v; want false, nil", ok, err)
	}
	if !l.grantStart.IsZero() || !l.lastRenew.IsZero() {
		t.Fatal("a renewal answered 0 left its grant open")
	}
	if ok, err := l.acquire(ctx, time.Now()); err != nil || !ok || l.grant != 2 {
		t.Fatalf("reacquire = %v, %v, grant %d; want true, nil, 2", ok, err, l.grant)
	}
}

func assertRedisLeaseOwner(t *testing.T, ctx context.Context, client *redis.Client, want string) {
	t.Helper()
	if got, err := client.Get(ctx, engineLeaseKey).Result(); err != nil || got != want {
		t.Fatalf("lease owner = %q, %v; want %q", got, err, want)
	}
}
