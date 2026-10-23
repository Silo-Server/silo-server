package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Lua script: check-then-increment for two window counters.
// KEYS[1] = per-second key, KEYS[2] = per-minute key
// ARGV[1] = per-second limit, ARGV[2] = per-minute limit
// Returns: {allowed(0/1), sec_count, sec_limit, min_count, min_limit, sec_ttl}
var rateLimitScript = redis.NewScript(`
local sec_key = KEYS[1]
local min_key = KEYS[2]
local sec_limit = tonumber(ARGV[1])
local min_limit = tonumber(ARGV[2])

-- Check current counts before incrementing
local sec_count = tonumber(redis.call('GET', sec_key) or "0")
local min_count = tonumber(redis.call('GET', min_key) or "0")

-- Deny if either limit is exceeded
if sec_count >= sec_limit then
    local sec_ttl = redis.call('TTL', sec_key)
    if sec_ttl < 0 then sec_ttl = 1 end
    return {0, sec_count, sec_limit, min_count, min_limit, sec_ttl}
end
if min_count >= min_limit then
    local sec_ttl = redis.call('TTL', sec_key)
    if sec_ttl < 0 then sec_ttl = 1 end
    return {0, sec_count, sec_limit, min_count, min_limit, sec_ttl}
end

-- Allowed: increment both counters
sec_count = redis.call('INCR', sec_key)
if sec_count == 1 then
    redis.call('EXPIRE', sec_key, 2)
end

min_count = redis.call('INCR', min_key)
if min_count == 1 then
    redis.call('EXPIRE', min_key, 120)
end

local sec_ttl = redis.call('TTL', sec_key)
return {1, sec_count, sec_limit, min_count, min_limit, sec_ttl}
`)

const (
	// redisCallTimeout bounds one limiter call. A healthy Redis answers the
	// script in about a millisecond. A call on a new connection also waits for
	// its dial and handshake, several round trips, so this leaves room for a
	// distant Redis while keeping a check from waiting out go-redis's dial and
	// command retries, about two seconds, when Redis is down.
	redisCallTimeout = time.Second
	// redisOutagePause is how long the limiter fails open without calling
	// Redis after a call fails. When it runs out, the next check counts its
	// request in the background to find out whether Redis answers again, so
	// requests do not wait on Redis while it is down.
	redisOutagePause = 5 * time.Second
)

// pauseClock is the origin the outage pause counts from. Times measured from
// it use the monotonic clock, so a wall-clock step cannot stretch a pause.
var pauseClock = time.Now()

func pauseTime(t time.Time) int64 { return int64(t.Sub(pauseClock)) }

// RedisLimiter is a Redis-backed rate limiter using fixed window counters.
type RedisLimiter struct {
	client      *redis.Client
	callTimeout time.Duration
	// pausedUntil is the pauseTime before which Allow fails open without
	// calling Redis. It is zero while Redis answers.
	pausedUntil atomic.Int64
}

// redisWindowLimits maps the shared rate model onto Redis fixed windows. The
// second window must honor Burst just like the in-memory token bucket; using
// only int(RequestsPerSecond) previously collapsed a 60/minute, burst-30
// webhook limit into one request per second and rejected legitimate batches.
func redisWindowLimits(limit Rate) (secLimit, minLimit, effectiveLimit int) {
	secLimit = int(math.Ceil(limit.RequestsPerSecond))
	minLimit = int(math.Ceil(limit.RequestsPerMinute))
	if limit.Burst > secLimit {
		secLimit = limit.Burst
	}
	if secLimit <= 0 && minLimit > 0 {
		secLimit = minLimit
	}
	if minLimit <= 0 && secLimit > 0 {
		minLimit = secLimit * 60
	}
	return secLimit, minLimit, secLimit
}

// NewRedisLimiter creates a new Redis-backed rate limiter.
func NewRedisLimiter(client *redis.Client) *RedisLimiter {
	return &RedisLimiter{client: client, callTimeout: redisCallTimeout}
}

func (rl *RedisLimiter) Allow(ctx context.Context, key string, limit Rate) AllowResult {
	return rl.allowAt(ctx, key, limit, time.Now())
}

func (rl *RedisLimiter) allowAt(ctx context.Context, key string, limit Rate, now time.Time) AllowResult {
	secLimit, minLimit, effectiveLimit := redisWindowLimits(limit)

	if rl.paused(ctx, key, secLimit, minLimit, now) {
		return redisFailOpen(effectiveLimit, now)
	}

	result, err := rl.count(ctx, key, secLimit, minLimit, now)
	if err != nil {
		// Fail-open: allow request if Redis is unreachable. A caller that
		// gave up says nothing about Redis, so it does not pause the limiter.
		if ctx.Err() != nil {
			slog.WarnContext(ctx, "rate limit Redis error, allowing request", "component", "ratelimit", "error", err, "key", key)
		} else if rl.pausedUntil.Swap(pauseTime(now.Add(redisOutagePause))) == 0 {
			logRedisPause(ctx, err, key)
		}
		return redisFailOpen(effectiveLimit, now)
	}

	allowed := result[0] == 1
	secCount := int(result[1])
	minCount := int(result[3])
	secTTL := time.Duration(result[5]) * time.Second

	if !allowed {
		retryAfter := redisRetryAfter(now, minCount, minLimit, secTTL)
		return AllowResult{
			Allowed:    false,
			RetryAfter: retryAfter,
			Limit:      effectiveLimit,
			Remaining:  0,
			ResetAt:    now.Add(retryAfter),
		}
	}

	remaining := minLimit - minCount
	if secRemaining := secLimit - secCount; secRemaining < remaining {
		remaining = secRemaining
	}
	if remaining < 0 {
		remaining = 0
	}

	return AllowResult{
		Allowed:   true,
		Limit:     effectiveLimit,
		Remaining: remaining,
		ResetAt:   now.Add(time.Second).Truncate(time.Second),
	}
}

// count runs the window script for key, giving up after the call timeout.
func (rl *RedisLimiter) count(ctx context.Context, key string, secLimit, minLimit int, now time.Time) ([]int64, error) {
	secTs := now.Unix()
	minTs := now.Unix() / 60

	secKey := fmt.Sprintf("silo:ratelimit:%s:s:%d", key, secTs)
	minKey := fmt.Sprintf("silo:ratelimit:%s:m:%d", key, minTs)

	ctx, cancel := context.WithTimeout(ctx, rl.callTimeout)
	defer cancel()
	return rateLimitScript.Run(ctx, rl.client,
		[]string{secKey, minKey},
		secLimit, minLimit,
	).Int64Slice()
}

// paused reports whether a check at now skips Redis. The first check after a
// pause runs out starts the next pause and counts its request in the
// background, which ends the pause if Redis answers. A background count that
// never returns cannot keep the limiter paused past that pause.
func (rl *RedisLimiter) paused(ctx context.Context, key string, secLimit, minLimit int, now time.Time) bool {
	until := rl.pausedUntil.Load()
	if until == 0 {
		return false
	}
	if pauseTime(now) >= until {
		next := pauseTime(now.Add(redisOutagePause))
		if rl.pausedUntil.CompareAndSwap(until, next) {
			go rl.retry(context.WithoutCancel(ctx), next, key, secLimit, minLimit, now)
		}
	}
	return true
}

// retry counts a request allowed while the limiter was paused and ends the
// pause if Redis answers.
func (rl *RedisLimiter) retry(ctx context.Context, pause int64, key string, secLimit, minLimit int, now time.Time) {
	if _, err := rl.count(ctx, key, secLimit, minLimit, now); err != nil {
		logRedisPause(ctx, err, key)
		return
	}
	if rl.pausedUntil.CompareAndSwap(pause, 0) {
		slog.InfoContext(ctx, "rate limit Redis answering again", "component", "ratelimit")
	}
}

func logRedisPause(ctx context.Context, err error, key string) {
	slog.WarnContext(ctx, "rate limit Redis error, allowing requests until it answers", "component", "ratelimit", "error", err, "key", key, "retry_every", redisOutagePause)
}

// redisFailOpen allows a request the limiter could not count.
func redisFailOpen(effectiveLimit int, now time.Time) AllowResult {
	return AllowResult{
		Allowed:   true,
		Limit:     effectiveLimit,
		Remaining: -1, // unknown -- signals fail-open to callers
		ResetAt:   now.Add(time.Second).Truncate(time.Second),
	}
}

// redisRetryAfter returns how long a refused request has to wait. Both window
// keys roll over on wall-clock boundaries, so a full minute window outlasts the
// second window and its remainder covers both; a client honoring Retry-After
// is not refused again.
func redisRetryAfter(now time.Time, minCount, minLimit int, secTTL time.Duration) time.Duration {
	if minCount >= minLimit {
		return time.Duration(60-(now.Unix()%60)) * time.Second
	}
	return secTTL
}

func (rl *RedisLimiter) Close() {
	// Redis client lifecycle managed externally
}
