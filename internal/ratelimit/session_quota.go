package ratelimit

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// QuotaLimiter spends requests from long-window token buckets: a bucket holds
// q.Burst requests and refills at q.RequestsPerHour. The per-second and
// per-minute limiters cannot express this; their windows are too short to
// notice a client that stays just under them all day.
type QuotaLimiter interface {
	Take(ctx context.Context, key string, q SessionQuotaConfig) AllowResult
}

// SessionQuotaIdleTTL is the longest a quota may take to refill from empty,
// which the admin settings check, and so the longest an untouched bucket is
// kept.
const SessionQuotaIdleTTL = 7 * 24 * time.Hour

// quotaIdleTTL is how long a bucket holding tokens of burst is kept untouched.
// The slowest rate the settings allow refills a whole burst in
// SessionQuotaIdleTTL, so after the share of it the bucket is missing, any
// allowed rate has refilled it. Dropping it then grants nothing a take
// wouldn't, whatever the rate changes to meanwhile: a drained bucket is kept a
// week, and one a client barely touched goes within seconds, so sessions that
// sign in, make a few requests and leave don't pile up.
func quotaIdleTTL(tokens float64, burst int) time.Duration {
	if burst <= 0 {
		return SessionQuotaIdleTTL
	}
	missing := math.Min(1, math.Max(0, 1-tokens/float64(burst)))
	return time.Duration(math.Ceil(missing*SessionQuotaIdleTTL.Seconds())) * time.Second
}

// SessionQuotaRefillsInTime reports whether q's bucket refills from empty
// within SessionQuotaIdleTTL.
func SessionQuotaRefillsInTime(q SessionQuotaConfig) bool {
	return q.RequestsPerHour > 0 && float64(q.Burst)/q.RequestsPerHour <= SessionQuotaIdleTTL.Hours()
}

// quotaBucket is one key's bucket.
type quotaBucket struct {
	tokens  float64
	updated time.Time
}

// take refills b to now and spends one token if it holds one. A clock that
// moved backwards refills nothing rather than draining the bucket.
func (b *quotaBucket) take(q SessionQuotaConfig, now time.Time) AllowResult {
	perSecond := q.RequestsPerHour / 3600
	burst := float64(q.Burst)
	if elapsed := now.Sub(b.updated).Seconds(); elapsed > 0 {
		b.tokens = math.Min(burst, b.tokens+elapsed*perSecond)
		b.updated = now
	}
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	return quotaResult(allowed, b.tokens, q, now)
}

func quotaResult(allowed bool, tokens float64, q SessionQuotaConfig, now time.Time) AllowResult {
	result := AllowResult{
		Allowed:   allowed,
		Limit:     int(q.RequestsPerHour),
		Remaining: int(math.Max(0, math.Floor(tokens))),
		ResetAt:   now,
	}
	if !allowed {
		result.RetryAfter = secondsDuration((1 - tokens) / (q.RequestsPerHour / 3600))
		result.ResetAt = roundUpToSecond(now.Add(result.RetryAfter))
	}
	return result
}

// secondsDuration converts seconds to a Duration, saturating instead of
// overflowing for a bucket that refills slowly.
func secondsDuration(seconds float64) time.Duration {
	if math.IsNaN(seconds) || seconds <= 0 {
		return 0
	}
	if ns := seconds * float64(time.Second); ns < float64(math.MaxInt64) {
		return time.Duration(math.Ceil(ns))
	}
	return time.Duration(math.MaxInt64)
}

// quotaEvictEvery is how often a take also drops idle buckets.
const quotaEvictEvery = time.Minute

// MemoryQuotaLimiter keeps quota buckets in this process. It drops buckets
// idle past quotaIdleTTL as it goes, at most once a minute, so it needs no
// goroutine.
type MemoryQuotaLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*quotaBucket
	lastEvict time.Time
}

func NewMemoryQuotaLimiter() *MemoryQuotaLimiter {
	return &MemoryQuotaLimiter{buckets: make(map[string]*quotaBucket)}
}

func (ml *MemoryQuotaLimiter) Take(_ context.Context, key string, q SessionQuotaConfig) AllowResult {
	return ml.takeAt(key, q, time.Now())
}

func (ml *MemoryQuotaLimiter) takeAt(key string, q SessionQuotaConfig, now time.Time) AllowResult {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	if now.Sub(ml.lastEvict) >= quotaEvictEvery {
		ml.evictLocked(now, q.Burst)
	}
	b := ml.buckets[key]
	if b == nil {
		b = &quotaBucket{tokens: float64(q.Burst), updated: now}
		ml.buckets[key] = b
	}
	return b.take(q, now)
}

func (ml *MemoryQuotaLimiter) evictLocked(now time.Time, burst int) {
	ml.lastEvict = now
	for key, b := range ml.buckets {
		if now.Sub(b.updated) >= quotaIdleTTL(b.tokens, burst) {
			delete(ml.buckets, key)
		}
	}
}

// quotaScript is quotaBucket.take in Redis, timed by the Redis clock so every
// API server refills a shared bucket at the same rate. The hash expires as
// quotaIdleTTL says: after the share of ARGV[3] the bucket is missing.
// KEYS[1] = bucket key; ARGV[1] = refill per second, ARGV[2] = burst,
// ARGV[3] = SessionQuotaIdleTTL in seconds.
// Returns {allowed (0/1), tokens left as a string}.
var quotaScript = redis.NewScript(`
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local t = redis.call('TIME')
local now = tonumber(t[1]) + tonumber(t[2]) / 1000000
local stored = redis.call('HMGET', KEYS[1], 'tokens', 'updated')
local tokens = tonumber(stored[1])
local updated = tonumber(stored[2])
if tokens == nil or updated == nil then
    tokens = burst
    updated = now
end
if now > updated then
    tokens = math.min(burst, tokens + (now - updated) * rate)
    updated = now
end
local allowed = 0
if tokens >= 1 then
    tokens = tokens - 1
    allowed = 1
end
redis.call('HSET', KEYS[1], 'tokens', tostring(tokens), 'updated', tostring(updated))
local missing = math.min(1, math.max(0, 1 - tokens / burst))
redis.call('EXPIRE', KEYS[1], math.max(1, math.ceil(missing * tonumber(ARGV[3]))))
return {allowed, tostring(tokens)}
`)

// RedisQuotaLimiter shares quota buckets across API servers. It shares the
// window limiter's outage pause: while that limiter counts locally, takes
// count in a per-process bucket without calling Redis, and a take that fails
// starts the pause the window limiter's background retry ends. A request's
// checks therefore stop waiting on Redis together.
type RedisQuotaLimiter struct {
	windows *RedisLimiter
	local   *MemoryQuotaLimiter
}

// NewRedisQuotaLimiter returns a quota limiter on the window limiter's
// client and outage state.
func NewRedisQuotaLimiter(windows *RedisLimiter) *RedisQuotaLimiter {
	return &RedisQuotaLimiter{windows: windows, local: NewMemoryQuotaLimiter()}
}

func (rl *RedisQuotaLimiter) Take(ctx context.Context, key string, q SessionQuotaConfig) AllowResult {
	now := time.Now()
	if rl.windows.pausedUntil.Load() != 0 {
		return rl.local.takeAt(key, q, now)
	}
	// The bucket is shared, so a caller that gave up must not leave it half
	// spent; the call timeout still bounds the wait.
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rl.windows.callTimeout)
	defer cancel()
	raw, err := quotaScript.Run(callCtx, rl.windows.client, []string{"silo:ratelimit:quota:" + key},
		strconv.FormatFloat(q.RequestsPerHour/3600, 'g', -1, 64), q.Burst, int(SessionQuotaIdleTTL/time.Second)).Slice()
	var tokens float64
	if err == nil {
		if len(raw) == 2 {
			text, _ := raw[1].(string)
			tokens, err = strconv.ParseFloat(text, 64)
		} else {
			err = errors.New("unexpected quota script reply")
		}
	}
	if err != nil {
		if rl.windows.pausedUntil.CompareAndSwap(0, pauseTime(now.Add(redisOutagePause))) {
			logRedisPause(ctx, err, key)
		}
		return rl.local.takeAt(key, q, now)
	}
	allowed, _ := raw[0].(int64)
	return quotaResult(allowed == 1, tokens, q, now)
}
