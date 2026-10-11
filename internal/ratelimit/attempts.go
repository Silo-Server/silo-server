package ratelimit

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// AttemptPolicy bounds guesses at a short secret such as a profile PIN.
//
// Up to MaxAttempts attempts are allowed per key. The first attempt opens a
// Window-long counting window; the attempt that brings the count to
// MaxAttempts restarts that window, so the key is then locked for a full
// Window. While locked every attempt is refused, a correct secret included.
// A correct secret entered while not locked clears the count (Reset), and
// the count disappears when its window expires.
type AttemptPolicy struct {
	MaxAttempts int
	Window      time.Duration
}

// ProfilePINPolicy is the household profile PIN limit: five attempts, then
// five minutes locked.
var ProfilePINPolicy = AttemptPolicy{MaxAttempts: 5, Window: 5 * time.Minute}

// ProfilePINKey is the attempt key for one profile's PIN. It is per profile,
// not per client address: whoever guesses a PIN already shares the account's
// sign-in, usually on the same device.
func ProfilePINKey(userID int, profileID string) string {
	return "profile_pin:" + strconv.Itoa(userID) + ":" + profileID
}

// AttemptLimiter counts attempts at a secret per key under an AttemptPolicy.
// The Redis backend shares the count across every node of a cluster; the
// memory backend is per process and serves Redis-less single-node
// deployments.
//
// Callers Reserve before checking the secret and Reset after a match.
// Counting the attempt before the check means concurrent guesses cannot
// overrun the limit. A nil *AttemptLimiter allows everything.
type AttemptLimiter struct {
	policy  AttemptPolicy
	backend attemptBackend
}

type attemptBackend interface {
	reserve(ctx context.Context, key string, p AttemptPolicy) (allowed bool, retryAfter time.Duration, err error)
	reset(ctx context.Context, key string) error
}

// NewMemoryAttemptLimiter returns a process-local AttemptLimiter.
func NewMemoryAttemptLimiter(p AttemptPolicy) *AttemptLimiter {
	return &AttemptLimiter{policy: p, backend: newMemoryAttempts(time.Now)}
}

// NewRedisAttemptLimiter returns an AttemptLimiter whose counts live in Redis.
func NewRedisAttemptLimiter(client *redis.Client, p AttemptPolicy) *AttemptLimiter {
	return &AttemptLimiter{policy: p, backend: redisAttempts{client: client}}
}

// NewProfilePINAttemptLimiter returns the profile PIN limiter: on Redis
// whenever the deployment has Redis, so every node shares one count per
// profile regardless of the request rate limiter's backend or whether request
// rate limiting is enabled, and process-local only on a Redis-less (single
// node) deployment.
func NewProfilePINAttemptLimiter(client *redis.Client) *AttemptLimiter {
	if client != nil {
		return NewRedisAttemptLimiter(client, ProfilePINPolicy)
	}
	return NewMemoryAttemptLimiter(ProfilePINPolicy)
}

// Reserve counts one attempt for key. It reports false with the time left on
// the lockout when the key is locked; the attempt is then not counted and the
// secret must not be checked. Like the request limiter, a backend error fails
// open (logged), so a Redis outage does not lock every profile. A request
// whose own context is done is refused instead: a client that drops its
// connection mid-request must not get an uncounted guess.
func (l *AttemptLimiter) Reserve(ctx context.Context, key string) (retryAfter time.Duration, ok bool) {
	if l == nil {
		return 0, true
	}
	allowed, retryAfter, err := l.backend.reserve(ctx, key, l.policy)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false
		}
		slog.WarnContext(ctx, "attempt limit backend error, allowing attempt", "component", "ratelimit", "error", err, "key", key)
		return 0, true
	}
	return retryAfter, allowed
}

// Reset clears key's count after a correct secret.
func (l *AttemptLimiter) Reset(ctx context.Context, key string) {
	if l == nil {
		return
	}
	if err := l.backend.reset(ctx, key); err != nil {
		slog.WarnContext(ctx, "attempt limit reset failed", "component", "ratelimit", "error", err, "key", key)
	}
}

// RetryAfterSeconds rounds a lockout remainder up to whole seconds for a
// Retry-After header, never below one.
func RetryAfterSeconds(d time.Duration) int {
	s := int((d + time.Second - 1) / time.Second)
	if s < 1 {
		return 1
	}
	return s
}

// --- memory backend ---

type attemptEntry struct {
	count   int
	expires time.Time
}

type memoryAttempts struct {
	mu        sync.Mutex
	entries   map[string]attemptEntry
	now       func() time.Time
	lastSweep time.Time
}

func newMemoryAttempts(now func() time.Time) *memoryAttempts {
	return &memoryAttempts{entries: map[string]attemptEntry{}, now: now}
}

func (m *memoryAttempts) reserve(_ context.Context, key string, p AttemptPolicy) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	// Expired entries are dropped lazily, at most once per window, so the
	// map stays bounded without a background goroutine.
	if now.Sub(m.lastSweep) >= p.Window {
		for k, e := range m.entries {
			if !now.Before(e.expires) {
				delete(m.entries, k)
			}
		}
		m.lastSweep = now
	}
	e := m.entries[key]
	if !now.Before(e.expires) {
		e = attemptEntry{}
	}
	if e.count >= p.MaxAttempts {
		return false, e.expires.Sub(now), nil
	}
	e.count++
	if e.count == 1 || e.count >= p.MaxAttempts {
		e.expires = now.Add(p.Window)
	}
	m.entries[key] = e
	return true, 0, nil
}

func (m *memoryAttempts) reset(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.entries, key)
	m.mu.Unlock()
	return nil
}

// --- Redis backend ---

// attemptScript reserves one attempt atomically.
// KEYS[1] = counter key; ARGV[1] = max attempts; ARGV[2] = window in ms.
// Returns {allowed(0/1), lockout ms remaining}.
var attemptScript = redis.NewScript(`
local key = KEYS[1]
local max = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local count = tonumber(redis.call('GET', key) or "0")
if count >= max then
    local ttl = redis.call('PTTL', key)
    if ttl < 0 then
        redis.call('PEXPIRE', key, window)
        ttl = window
    end
    return {0, ttl}
end
count = redis.call('INCR', key)
if count == 1 or count >= max then
    redis.call('PEXPIRE', key, window)
end
return {1, 0}
`)

type redisAttempts struct {
	client *redis.Client
}

func redisAttemptKey(key string) string { return "silo:attempts:" + key }

func (r redisAttempts) reserve(ctx context.Context, key string, p AttemptPolicy) (bool, time.Duration, error) {
	res, err := attemptScript.Run(ctx, r.client, []string{redisAttemptKey(key)}, p.MaxAttempts, p.Window.Milliseconds()).Int64Slice()
	if err != nil {
		return false, 0, err
	}
	return res[0] == 1, time.Duration(res[1]) * time.Millisecond, nil
}

func (r redisAttempts) reset(ctx context.Context, key string) error {
	return r.client.Del(ctx, redisAttemptKey(key)).Err()
}
