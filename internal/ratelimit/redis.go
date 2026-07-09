package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
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

// RedisLimiter is a Redis-backed rate limiter using fixed window counters.
type RedisLimiter struct {
	client *redis.Client
}

// NewRedisLimiter creates a new Redis-backed rate limiter.
func NewRedisLimiter(client *redis.Client) *RedisLimiter {
	return &RedisLimiter{client: client}
}

func (rl *RedisLimiter) Allow(ctx context.Context, key string, limit Rate) AllowResult {
	now := time.Now()
	secTs := now.Unix()
	minTs := now.Unix() / 60

	secKey := fmt.Sprintf("silo:ratelimit:%s:s:%d", key, secTs)
	minKey := fmt.Sprintf("silo:ratelimit:%s:m:%d", key, minTs)

	secLimit := int(limit.RequestsPerSecond)
	minLimit := int(limit.RequestsPerMinute)

	// For sub-1 rps rates (e.g. auth endpoints: 5 req/min = 0.083 rps),
	// int truncation gives secLimit=0 which makes the Lua script reject
	// every request. Use the per-minute limit as the effective limit and
	// skip the per-second constraint by setting it to the per-minute limit
	// (the per-minute window is the real constraint).
	subSecondRate := secLimit == 0 && limit.RequestsPerSecond > 0
	if subSecondRate {
		secLimit = minLimit
	}

	// Effective limit for response headers: use per-minute when the rate
	// is defined in minutes (sub-second), per-second otherwise.
	effectiveLimit := secLimit
	if subSecondRate {
		effectiveLimit = minLimit
	}

	result, err := rateLimitScript.Run(ctx, rl.client,
		[]string{secKey, minKey},
		secLimit, minLimit,
	).Int64Slice()

	if err != nil {
		// Fail-open: allow request if Redis is unreachable
		slog.WarnContext(ctx, "rate limit Redis error, allowing request", "component", "ratelimit", "error", err, "key", key)
		return AllowResult{
			Allowed:   true,
			Limit:     effectiveLimit,
			Remaining: -1, // unknown -- signals fail-open to callers
			ResetAt:   now.Add(time.Second).Truncate(time.Second),
		}
	}

	allowed := result[0] == 1
	secCount := int(result[1])
	minCount := int(result[3])
	secTTL := time.Duration(result[5]) * time.Second

	if !allowed {
		// Determine which limit was hit for RetryAfter
		retryAfter := secTTL
		if secCount < secLimit && minCount >= minLimit {
			retryAfter = time.Duration(60-(now.Unix()%60)) * time.Second
		}
		return AllowResult{
			Allowed:    false,
			RetryAfter: retryAfter,
			Limit:      effectiveLimit,
			Remaining:  0,
			ResetAt:    now.Add(retryAfter),
		}
	}

	remaining := effectiveLimit - minCount
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

func (rl *RedisLimiter) Close() {
	// Redis client lifecycle managed externally
}
