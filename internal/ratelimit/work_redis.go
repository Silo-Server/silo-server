package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const workRedisPrefix = "silo:ratelimit:work:"

// One declared hash holds retained windows for a scope, with count and absolute
// microsecond expiry per field. TIME is read inside the atomic script; a return
// to a still-live window must reuse its count. Up to 128 windows are retained:
// normal forward time needs at most six. Extreme clock regressions that fill the
// hash refuse work instead of evicting live credit. Cleanup scans are bounded.
// Redis data loss or a return to an already expired window can reset capacity.
const workLua = `
local max_integer = 9007199254740991
local max_fields = 128
local function integer(text)
    if type(text) ~= 'string' or not string.match(text, '^%d+$') then return nil end
    local n = tonumber(text)
    if not n or n < 0 or n > max_integer or n ~= math.floor(n) then return nil end
    if string.format('%.0f', n) ~= text then return nil end
    return n
end
local sec_limit = integer(ARGV[1])
local min_limit = integer(ARGV[2])
if not sec_limit or not min_limit or sec_limit < 1 or min_limit < 1
    or sec_limit > 2147483647 or min_limit > 2147483647 then
    return {-1, 0}
end
local stamp = redis.call('TIME')
local seconds = integer(stamp[1])
local micros = integer(stamp[2])
if not seconds or not micros or micros >= 1000000
    or seconds > math.floor((max_integer - 120999999) / 1000000) then
    return {-1, 0}
end
local now = seconds * 1000000 + micros
local sec = 's:' .. string.format('%.0f', math.floor(now / 1000000))
local minute = 'm:' .. string.format('%.0f', math.floor(now / 60000000))
if redis.call('HLEN', KEYS[1]) > max_fields then return {-1, 0} end
local fields = redis.call('HGETALL', KEYS[1])
local expired = {}
local live = 0
local sec_count, min_count = 0, 0
local sec_expiry, min_expiry = now + 2000000, now + 120000000
local last_expiry = 0
for i = 1, #fields, 2 do
    local field, value = fields[i], fields[i + 1]
    local kind, index = string.match(field, '^([sm]):(%d+)$')
    local count_text, expiry_text = string.match(value, '^(%d+):(%d+)$')
    local count, expiry = integer(count_text), integer(expiry_text)
    if not kind or not integer(index) or not count or count < 1
        or count > 2147483647 or not expiry or expiry < 1 then return {-1, 0} end
    if expiry <= now then
        expired[#expired + 1] = field
    else
        live = live + 1
        last_expiry = math.max(last_expiry, expiry)
        if field == sec then sec_count, sec_expiry = count, expiry end
        if field == minute then min_count, min_expiry = count, expiry end
    end
end
if #expired > 0 then redis.call('HDEL', KEYS[1], unpack(expired)) end
local retry = 0
if sec_count >= sec_limit then retry = 1000000 - (now % 1000000) end
if min_count >= min_limit then retry = math.max(retry, 60000000 - (now % 60000000)) end
if retry > 0 then return {0, math.ceil(retry / 1000)} end
local needed = 0
if sec_count == 0 then needed = needed + 1 end
if min_count == 0 then needed = needed + 1 end
if live + needed > max_fields then return {-1, 0} end
redis.call('HSET', KEYS[1],
    sec, string.format('%.0f:%.0f', sec_count + 1, sec_expiry),
    minute, string.format('%.0f:%.0f', min_count + 1, min_expiry))
last_expiry = math.max(last_expiry, sec_expiry, min_expiry)
redis.call('PEXPIREAT', KEYS[1], math.ceil(last_expiry / 1000))
return {1, 0}
`

var workScript = redis.NewScript(workLua)

type redisWorkStore struct{ client *redis.Client }

// NewRedisWorkStore creates a fail-closed store. A nil client is unavailable,
// never a memory fallback. The caller owns the client and must configure it to
// honor context deadlines (including socket I/O and retries).
func NewRedisWorkStore(client *redis.Client) WorkStore { //nolint:iface // The public constructor contract returns WorkStore.
	return &redisWorkStore{client: client}
}

func (s *redisWorkStore) AllowWork(ctx context.Context, key string, limit WorkLimit) WorkResult {
	if ctx.Err() != nil || s.client == nil || key == "" || !validWorkLimit(limit) {
		return workUnavailable()
	}
	result, err := workScript.Run(ctx, s.client, []string{workRedisPrefix + key},
		limit.PerSecond, limit.PerMinute).Result()
	if err != nil || ctx.Err() != nil {
		return workUnavailable()
	}
	return parseWorkResult(result)
}

func parseWorkResult(raw any) WorkResult {
	values, ok := raw.([]any)
	if !ok || len(values) != 2 {
		return workUnavailable()
	}
	allowed, ok := values[0].(int64)
	if !ok {
		return workUnavailable()
	}
	retryMS, ok := values[1].(int64)
	if !ok {
		return workUnavailable()
	}
	switch {
	case allowed == 1 && retryMS == 0:
		return WorkResult{Allowed: true}
	case allowed == 0 && retryMS > 0 && retryMS <= 60_000:
		return WorkResult{RetryAfter: time.Duration(retryMS) * time.Millisecond}
	default:
		return workUnavailable()
	}
}
