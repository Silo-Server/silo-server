package workersource

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisStore struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisStore(client redis.UniversalClient, prefix string) *RedisStore {
	if prefix == "" {
		prefix = "silo:{workersource}:"
	} else if !strings.Contains(prefix, "{") {
		prefix += "{workersource}:"
	}
	return &RedisStore{client: client, prefix: prefix}
}
func (s *RedisStore) keys() []string {
	return []string{s.prefix + "records", s.prefix + "expiry", s.prefix + "sizes", s.prefix + "scopes", s.prefix + "bytes", s.prefix + "revoked"}
}

// All keys share a cluster hash tag. Raw record JSON is never decoded/re-encoded
// in Lua: doing that could round a uint64 execution generation.
const redisHelpers = `
local clock=redis.call('TIME')
local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
local function drop(id)
 local size=redis.call('HGET',KEYS[3],id)
 if size then redis.call('INCRBY',KEYS[5],'-'..size) end
 redis.call('HDEL',KEYS[1],id)
 redis.call('HDEL',KEYS[3],id)
 redis.call('HDEL',KEYS[4],id)
 redis.call('ZREM',KEYS[2],id)
end
`

var redisCreate = redis.NewScript(redisHelpers + `
local expired=redis.call('ZRANGEBYSCORE',KEYS[2],'-inf',now)
for _,id in ipairs(expired) do drop(id) end
redis.call('ZREMRANGEBYSCORE',KEYS[6],'-inf',now)
if tonumber(ARGV[3])<=now then return 'expired' end
local revoked=redis.call('ZSCORE',KEYS[6],ARGV[5])
if revoked and tonumber(revoked)>now then return 'inactive' end
if redis.call('HEXISTS',KEYS[1],ARGV[1])==1 then return 'denied' end
local bytes=tonumber(redis.call('GET',KEYS[5]) or '0')
if redis.call('ZCARD',KEYS[2])>=tonumber(ARGV[6]) or tonumber(ARGV[4])>tonumber(ARGV[7])-bytes then return 'quota' end
redis.call('HSET',KEYS[1],ARGV[1],ARGV[2])
redis.call('HSET',KEYS[3],ARGV[1],ARGV[4])
redis.call('HSET',KEYS[4],ARGV[1],ARGV[5])
redis.call('ZADD',KEYS[2],ARGV[3],ARGV[1])
redis.call('INCRBY',KEYS[5],ARGV[4])
return 'ok'
`)
var redisGet = redis.NewScript(redisHelpers + `
local data=redis.call('HGET',KEYS[1],ARGV[1])
local expires=redis.call('ZSCORE',KEYS[2],ARGV[1])
if not data or not expires then return {'missing'} end
if tonumber(expires)<=now then drop(ARGV[1]);return {'expired'} end
return {'ok',data,expires}
`)
var redisExtend = redis.NewScript(redisHelpers + `
local expires=redis.call('ZSCORE',KEYS[2],ARGV[1])
if not expires or redis.call('HEXISTS',KEYS[1],ARGV[1])==0 then return 'missing' end
if tonumber(expires)<=now or tonumber(ARGV[2])<=now then drop(ARGV[1]);return 'expired' end
redis.call('ZADD',KEYS[2],ARGV[2],ARGV[1])
return 'ok'
`)
var redisDelete = redis.NewScript(redisHelpers + `drop(ARGV[1]);return 'ok'`)
var redisRevoke = redis.NewScript(redisHelpers + `
redis.call('ZADD',KEYS[6],now+tonumber(ARGV[2]),ARGV[1])
local scopes=redis.call('HGETALL',KEYS[4])
for i=1,#scopes,2 do if scopes[i+1]==ARGV[1] then drop(scopes[i]) end end
return 'ok'
`)

func redisStatus(status string) error {
	switch status {
	case "ok":
		return nil
	case "missing":
		return ErrNotFound
	case "expired":
		return ErrExpired
	case "quota":
		return ErrQuota
	case "inactive":
		return ErrInactive
	case "denied":
		return ErrDenied
	default:
		return ErrUnavailable
	}
}
func (s *RedisStore) run(ctx context.Context, script *redis.Script, args ...any) (any, error) {
	if s == nil || s.client == nil {
		return nil, ErrUnavailable
	}
	result, err := script.Run(ctx, s.client, s.keys(), args...).Result()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	return result, nil
}
func (s *RedisStore) status(ctx context.Context, script *redis.Script, args ...any) error {
	result, err := s.run(ctx, script, args...)
	if err != nil {
		return err
	}
	status, ok := result.(string)
	if !ok {
		return ErrUnavailable
	}
	return redisStatus(status)
}
func (s *RedisStore) Create(ctx context.Context, r Record, l Limits) error {
	if s == nil || s.client == nil {
		return ErrUnavailable
	}
	if r.Source.Size < 0 || l.MaxGrants < 1 || l.MaxGrants > 65536 || l.MaxSourceBytes < 1 || l.MaxSourceBytes > 1<<52 || !validWork(r.Source.NodeID, r.Source.WorkID, r.Source.Generation) {
		return ErrDenied
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return ErrUnavailable
	}
	return s.status(ctx, redisCreate, r.Source.Grant, string(encoded), r.ExpiresAt.UnixMilli(), r.Source.Size, workKey(r.Source.NodeID, r.Source.WorkID, r.Source.Generation), l.MaxGrants, l.MaxSourceBytes)
}
func (s *RedisStore) Get(ctx context.Context, grant string) (Record, error) {
	result, err := s.run(ctx, redisGet, grant)
	if err != nil {
		return Record{}, err
	}
	values, ok := result.([]any)
	if !ok || len(values) < 1 {
		return Record{}, ErrUnavailable
	}
	status, ok := values[0].(string)
	if !ok {
		return Record{}, ErrUnavailable
	}
	if err := redisStatus(status); err != nil {
		return Record{}, err
	}
	if len(values) != 3 {
		return Record{}, ErrUnavailable
	}
	encoded, ok := values[1].(string)
	if !ok {
		return Record{}, ErrUnavailable
	}
	expires, ok := values[2].(string)
	if !ok {
		return Record{}, ErrUnavailable
	}
	expiry, err := strconv.ParseFloat(expires, 64)
	if err != nil {
		return Record{}, ErrUnavailable
	}
	var r Record
	if err := json.Unmarshal([]byte(encoded), &r); err != nil || r.Source.Grant != grant {
		return Record{}, ErrUnavailable
	}
	r.ExpiresAt = time.UnixMilli(int64(expiry))
	return r, nil
}
func (s *RedisStore) Extend(ctx context.Context, grant string, expires time.Time) error {
	return s.status(ctx, redisExtend, grant, expires.UnixMilli())
}
func (s *RedisStore) Delete(ctx context.Context, grant string) error {
	return s.status(ctx, redisDelete, grant)
}
func (s *RedisStore) RevokeWork(ctx context.Context, node int, work string, generation uint64) error {
	if !validWork(node, work, generation) {
		return ErrDenied
	}
	return s.status(ctx, redisRevoke, workKey(node, work, generation), DefaultTTL.Milliseconds())
}

var _ Store = (*RedisStore)(nil)
