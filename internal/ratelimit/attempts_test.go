package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestMemoryAttemptLimiter(p AttemptPolicy) (*AttemptLimiter, *fakeClock) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	return &AttemptLimiter{policy: p, backend: newMemoryAttempts(clock.Now)}, clock
}

func reserveN(t *testing.T, l *AttemptLimiter, key string, n int) {
	t.Helper()
	for i := range n {
		if _, ok := l.Reserve(context.Background(), key); !ok {
			t.Fatalf("attempt %d refused, want allowed", i+1)
		}
	}
}

func TestAttemptLimiterLocksAfterMaxAttempts(t *testing.T) {
	l, clock := newTestMemoryAttemptLimiter(ProfilePINPolicy)
	ctx := context.Background()
	reserveN(t, l, "k", 5)

	clock.Advance(10 * time.Second)
	retry, ok := l.Reserve(ctx, "k")
	if ok {
		t.Fatal("sixth attempt allowed, want locked")
	}
	if retry != 5*time.Minute-10*time.Second {
		t.Fatalf("retryAfter = %v, want the full lockout less the elapsed 10s", retry)
	}
	// Locked attempts are not counted, so the lockout does not extend.
	if retry2, _ := l.Reserve(ctx, "k"); retry2 != retry {
		t.Fatalf("retryAfter moved to %v while locked, want %v", retry2, retry)
	}

	clock.Advance(retry)
	if _, ok := l.Reserve(ctx, "k"); !ok {
		t.Fatal("attempt after the lockout refused")
	}
}

// The lockout runs a full window from the attempt that reached the limit,
// not from the first attempt of the window.
func TestAttemptLimiterLockoutStartsAtLastAttempt(t *testing.T) {
	l, clock := newTestMemoryAttemptLimiter(ProfilePINPolicy)
	reserveN(t, l, "k", 4)
	clock.Advance(4 * time.Minute)
	reserveN(t, l, "k", 1)
	retry, ok := l.Reserve(context.Background(), "k")
	if ok || retry != 5*time.Minute {
		t.Fatalf("Reserve = (%v, %v), want locked for 5m", retry, ok)
	}
}

// The PIN limiter follows Redis availability, not the request limiter's
// backend: any configured Redis client gives the shared backend.
func TestProfilePINAttemptLimiterUsesRedisWhenAvailable(t *testing.T) {
	if _, ok := NewProfilePINAttemptLimiter(nil).backend.(*memoryAttempts); !ok {
		t.Fatal("no Redis: want the memory backend")
	}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	l := NewProfilePINAttemptLimiter(client)
	if _, ok := l.backend.(redisAttempts); !ok {
		t.Fatal("Redis configured: want the Redis backend")
	}
	if l.policy != ProfilePINPolicy {
		t.Fatalf("policy = %+v, want ProfilePINPolicy", l.policy)
	}
}

// failingAttempts is a backend whose every call fails with err.
type failingAttempts struct{ err error }

func (f failingAttempts) reserve(context.Context, string, AttemptPolicy) (bool, time.Duration, error) {
	return false, 0, f.err
}

func (f failingAttempts) reset(context.Context, string) error { return f.err }

func TestAttemptLimiterBackendErrorFailsOpen(t *testing.T) {
	l := &AttemptLimiter{policy: ProfilePINPolicy, backend: failingAttempts{err: errors.New("redis down")}}
	if _, ok := l.Reserve(context.Background(), "k"); !ok {
		t.Fatal("Reserve refused on a backend outage, want fail open")
	}
}

func TestAttemptLimiterCanceledRequestIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := &AttemptLimiter{policy: ProfilePINPolicy, backend: failingAttempts{err: ctx.Err()}}
	if _, ok := l.Reserve(ctx, "k"); ok {
		t.Fatal("Reserve allowed a canceled request, want it refused so the guess is not uncounted")
	}
}
