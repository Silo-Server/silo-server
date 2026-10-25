package ratelimit

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// QuotaLimiter spends requests from long-window token buckets: a bucket holds
// q.Burst requests and refills at q.RequestsPerHour. The per-second and
// per-minute limiters cannot express this; their windows are too short to
// notice a client that stays just under them all day.
type QuotaLimiter interface {
	Take(ctx context.Context, key string, q SessionQuotaConfig) AllowResult
	Close()
}

// quotaBucket is one key's bucket. full is when it refills completely, after
// which it is indistinguishable from a new bucket and can be dropped.
type quotaBucket struct {
	tokens  float64
	updated time.Time
	full    time.Time
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
	b.full = b.updated.Add(secondsDuration((burst - b.tokens) / perSecond))
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

// MemoryQuotaLimiter keeps quota buckets in this process.
type MemoryQuotaLimiter struct {
	mu      sync.Mutex
	buckets map[string]*quotaBucket
	done    chan struct{}
	once    sync.Once
}

// NewMemoryQuotaLimiter returns a limiter that drops refilled buckets in the
// background.
func NewMemoryQuotaLimiter() *MemoryQuotaLimiter {
	ml := &MemoryQuotaLimiter{buckets: make(map[string]*quotaBucket), done: make(chan struct{})}
	go ml.evictLoop()
	return ml
}

func (ml *MemoryQuotaLimiter) Take(_ context.Context, key string, q SessionQuotaConfig) AllowResult {
	return ml.takeAt(key, q, time.Now())
}

func (ml *MemoryQuotaLimiter) takeAt(key string, q SessionQuotaConfig, now time.Time) AllowResult {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	b := ml.buckets[key]
	if b == nil {
		b = &quotaBucket{tokens: float64(q.Burst), updated: now}
		ml.buckets[key] = b
	}
	return b.take(q, now)
}

func (ml *MemoryQuotaLimiter) Close() { ml.once.Do(func() { close(ml.done) }) }

func (ml *MemoryQuotaLimiter) evictLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ml.done:
			return
		case now := <-ticker.C:
			ml.evict(now)
		}
	}
}

func (ml *MemoryQuotaLimiter) evict(now time.Time) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	for key, b := range ml.buckets {
		if !now.Before(b.full) {
			delete(ml.buckets, key)
		}
	}
}

// quotaScript is quotaBucket.take in Redis, timed by the Redis clock so every
// API server refills a shared bucket at the same rate. The hash expires once
// the bucket would be full again.
// KEYS[1] = bucket key; ARGV[1] = refill per second, ARGV[2] = burst.
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
redis.call('EXPIRE', KEYS[1], math.ceil((burst - tokens) / rate) + 60)
return {allowed, tostring(tokens)}
`)

// RedisQuotaLimiter shares quota buckets across API servers. A take Redis
// cannot answer is counted on this node instead, and calls pause for
// redisOutagePause after a failure, as RedisLimiter does.
type RedisQuotaLimiter struct {
	client      *redis.Client
	callTimeout time.Duration
	pausedUntil atomic.Int64
	local       *MemoryQuotaLimiter
}

// NewRedisQuotaLimiter returns a Redis-backed quota limiter.
func NewRedisQuotaLimiter(client *redis.Client) *RedisQuotaLimiter {
	return &RedisQuotaLimiter{client: client, callTimeout: redisCallTimeout, local: NewMemoryQuotaLimiter()}
}

func (rl *RedisQuotaLimiter) Take(ctx context.Context, key string, q SessionQuotaConfig) AllowResult {
	now := time.Now()
	if rl.paused(ctx, now) {
		return rl.local.takeAt(key, q, now)
	}
	// The bucket is shared, so a caller that gave up must not leave it half
	// spent; the call timeout still bounds the wait.
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rl.callTimeout)
	defer cancel()
	raw, err := quotaScript.Run(callCtx, rl.client, []string{"silo:ratelimit:quota:" + key},
		strconv.FormatFloat(q.RequestsPerHour/3600, 'g', -1, 64), q.Burst).Slice()
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
		if rl.pausedUntil.CompareAndSwap(0, pauseTime(now.Add(redisOutagePause))) {
			logQuotaRedisPause(ctx, err)
		}
		return rl.local.takeAt(key, q, now)
	}
	allowed, _ := raw[0].(int64)
	return quotaResult(allowed == 1, tokens, q, now)
}

// paused reports whether a take at now skips Redis. The first take after a
// pause runs out starts the next pause and pings Redis in the background,
// which ends the pause if Redis answers, so no request waits on a Redis that
// is down.
func (rl *RedisQuotaLimiter) paused(ctx context.Context, now time.Time) bool {
	until := rl.pausedUntil.Load()
	if until == 0 {
		return false
	}
	if pauseTime(now) >= until {
		next := pauseTime(now.Add(redisOutagePause))
		if rl.pausedUntil.CompareAndSwap(until, next) {
			go rl.probe(context.WithoutCancel(ctx), next)
		}
	}
	return true
}

func (rl *RedisQuotaLimiter) probe(ctx context.Context, pause int64) {
	ctx, cancel := context.WithTimeout(ctx, rl.callTimeout)
	defer cancel()
	if err := rl.client.Ping(ctx).Err(); err != nil {
		logQuotaRedisPause(ctx, err)
		return
	}
	if rl.pausedUntil.CompareAndSwap(pause, 0) {
		slog.InfoContext(ctx, "session quota Redis answering again", "component", "ratelimit")
	}
}

func logQuotaRedisPause(ctx context.Context, err error) {
	slog.WarnContext(ctx, "session quota Redis error, counting requests on this node until it answers",
		"component", "ratelimit", "error", err, "retry_every", redisOutagePause)
}

func (rl *RedisQuotaLimiter) Close() { rl.local.Close() }
