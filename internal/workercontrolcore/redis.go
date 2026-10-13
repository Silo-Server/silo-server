package workercontrolcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

// RedisStore keeps assignment generations and live/candidate transitions on the
// core's shared Redis. Lua never decodes JSON: int64 recipe timestamps retain
// their exact representation instead of passing through Lua's float64 numbers.
type RedisStore struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisStore(client redis.UniversalClient, prefix string) *RedisStore {
	if prefix == "" {
		prefix = "silo:worker-control:"
	}
	return &RedisStore{client: client, prefix: prefix}
}
func (s *RedisStore) keys(node int) []string {
	base := s.prefix + "{" + strconv.Itoa(node) + "}:"
	return []string{base + "generation", base + "candidate", base + "live", base + "leases"}
}

var beginScript = redis.NewScript(`
if ARGV[3]~='' then
 local live=redis.call('HGET',KEYS[3],ARGV[1])
 if redis.call('HEXISTS',KEYS[2],ARGV[1])==1 then return -2 end
 if ARGV[3]=='0' then
  if live or redis.call('HEXISTS',KEYS[1],ARGV[1])==1 then return -2 end
 elseif not live or string.sub(live,1,string.len(ARGV[3])+1)~=ARGV[3]..'\n' then return -2 end
end
local old=tonumber(redis.call('HGET',KEYS[1],ARGV[1]) or '0')
if old>=9007199254740991 then return -1 end
local next=redis.call('HINCRBY',KEYS[1],ARGV[1],1)
local nextString=redis.call('HGET',KEYS[1],ARGV[1])
local previous=redis.call('HGET',KEYS[2],ARGV[1]);if previous then redis.call('HDEL',KEYS[4],ARGV[1]..'\31'..string.match(previous,'^(%d+)\n')) end
redis.call('HSET',KEYS[2],ARGV[1],nextString..'\npending\n'..ARGV[2])
return next`)
var completeScript = redis.NewScript(`
local current=redis.call('HGET',KEYS[2],ARGV[1])
local generation=redis.call('HGET',KEYS[1],ARGV[1])
if not current or generation~=ARGV[2] or string.sub(current,1,string.len(ARGV[2])+1)~=ARGV[2]..'\n' then return 0 end
redis.call('HSET',KEYS[2],ARGV[1],ARGV[2]..'\nready\n'..ARGV[3])
return 1`)
var activateScript = redis.NewScript(`
local current=redis.call('HGET',KEYS[2],ARGV[1])
local prefix=ARGV[2]..'\nready\n'
if not current or redis.call('HGET',KEYS[1],ARGV[1])~=ARGV[2] or string.sub(current,1,string.len(prefix))~=prefix then return 0 end
local old=redis.call('HGET',KEYS[3],ARGV[1]);if old then redis.call('HDEL',KEYS[4],ARGV[1]..'\31'..string.match(old,'^(%d+)\n')) end
redis.call('HSET',KEYS[3],ARGV[1],current)
redis.call('HDEL',KEYS[2],ARGV[1])
return 1`)
var stopScript = redis.NewScript(`
local old=tonumber(redis.call('HGET',KEYS[1],ARGV[1]) or '0')
if old>=9007199254740991 then return -1 end
redis.call('HINCRBY',KEYS[1],ARGV[1],1)
for _,key in ipairs({KEYS[2],KEYS[3]}) do local old=redis.call('HGET',key,ARGV[1]);if old then redis.call('HDEL',KEYS[4],ARGV[1]..'\31'..string.match(old,'^(%d+)\n')) end;redis.call('HDEL',key,ARGV[1]) end;return 1`)
var cancelScript = redis.NewScript(`
local current=redis.call('HGET',KEYS[2],ARGV[1])
if not current or string.sub(current,1,string.len(ARGV[2])+1)~=ARGV[2]..'\n' then return 0 end
redis.call('HDEL',KEYS[4],ARGV[1]..'\31'..ARGV[2]);redis.call('HDEL',KEYS[2],ARGV[1]);return 1`)

func (s *RedisStore) Begin(ctx context.Context, a workerwire.Assignment) (workerwire.Assignment, error) {
	if s.client == nil {
		return a, ErrUnavailable
	}
	if !validAssignment(a) {
		return a, ErrDenied
	}
	encoded, err := json.Marshal(a)
	if err != nil {
		return a, err
	}
	expected := ""
	if value, ok := ctx.Value(recoveryKey{}).(uint64); ok {
		expected = strconv.FormatUint(value, 10)
	}
	generation, err := beginScript.Run(ctx, s.client, s.keys(a.NodeID), a.WorkID, string(encoded), expected).Int64()
	if err != nil {
		return a, err
	}
	if generation == -2 {
		return a, ErrFenced
	}
	if generation <= 0 {
		return a, ErrUnavailable
	}
	a.Generation = uint64(generation)
	return copyAssignment(a), nil
}
func (s *RedisStore) Complete(ctx context.Context, a workerwire.Assignment) error {
	if s.client == nil {
		return ErrUnavailable
	}
	if !validAssignment(a) || a.Generation == 0 {
		return ErrDenied
	}
	encoded, err := json.Marshal(a)
	if err != nil {
		return err
	}
	ok, err := completeScript.Run(ctx, s.client, s.keys(a.NodeID), a.WorkID, strconv.FormatUint(a.Generation, 10), string(encoded)).Int()
	if err != nil {
		return err
	}
	if ok != 1 {
		return ErrFenced
	}
	return nil
}
func (s *RedisStore) Activate(ctx context.Context, node int, work string, generation uint64) error {
	if s.client == nil {
		return ErrUnavailable
	}
	ok, err := activateScript.Run(ctx, s.client, s.keys(node), work, strconv.FormatUint(generation, 10)).Int()
	if err != nil {
		return err
	}
	if ok != 1 {
		return ErrFenced
	}
	return nil
}
func decodeRecord(raw string) (record, error) {
	parts := strings.SplitN(raw, "\n", 3)
	if len(parts) != 3 {
		return record{}, ErrUnavailable
	}
	generation, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || generation == 0 {
		return record{}, ErrUnavailable
	}
	var a workerwire.Assignment
	if err = json.Unmarshal([]byte(parts[2]), &a); err != nil || !validAssignment(a) {
		return record{}, ErrUnavailable
	}
	a.Generation = generation
	if parts[1] != "ready" && parts[1] != "pending" {
		return record{}, ErrUnavailable
	}
	return record{assignment: a, ready: parts[1] == "ready"}, nil
}
func (s *RedisStore) getRecord(ctx context.Context, node int, work string, index int) (record, bool, error) {
	if s.client == nil {
		return record{}, false, ErrUnavailable
	}
	raw, err := s.client.HGet(ctx, s.keys(node)[index], work).Result()
	if errors.Is(err, redis.Nil) {
		return record{}, false, nil
	}
	if err != nil {
		return record{}, false, err
	}
	r, err := decodeRecord(raw)
	if err != nil {
		return record{}, false, err
	}
	if r.assignment.NodeID != node || r.assignment.WorkID != work {
		return record{}, false, ErrUnavailable
	}
	return r, true, nil
}
func (s *RedisStore) Get(ctx context.Context, node int, work string) (workerwire.Assignment, bool, error) {
	r, ok, err := s.getRecord(ctx, node, work, 2)
	if err != nil {
		return workerwire.Assignment{}, false, err
	}
	if !ok {
		r, ok, err = s.getRecord(ctx, node, work, 1)
	}
	if err != nil || !ok || !r.ready {
		return workerwire.Assignment{}, false, err
	}
	return r.assignment, true, nil
}
func (s *RedisStore) Candidate(ctx context.Context, node int, work string) (workerwire.Assignment, bool, error) {
	r, ok, err := s.getRecord(ctx, node, work, 1)
	return r.assignment, ok, err
}
func (s *RedisStore) generations(ctx context.Context, node, index int) (map[string]uint64, error) {
	if s.client == nil {
		return nil, ErrUnavailable
	}
	raw, err := s.client.HGetAll(ctx, s.keys(node)[index]).Result()
	if err != nil {
		return nil, err
	}
	out := map[string]uint64{}
	for work, value := range raw {
		r, err := decodeRecord(value)
		if err != nil || r.assignment.WorkID != work || r.assignment.NodeID != node {
			return nil, ErrUnavailable
		}
		if index == 1 || r.ready {
			out[work] = r.assignment.Generation
		}
	}
	return out, nil
}
func (s *RedisStore) Active(ctx context.Context, node int) (map[string]uint64, error) {
	return s.generations(ctx, node, 2)
}
func (s *RedisStore) Pending(ctx context.Context, node int) (map[string]uint64, error) {
	return s.generations(ctx, node, 1)
}
func (s *RedisStore) IsActive(ctx context.Context, node int, work string, generation uint64) (bool, error) {
	for _, index := range []int{2, 1} {
		r, ok, err := s.getRecord(ctx, node, work, index)
		if err != nil {
			return false, err
		}
		if ok && generation > 0 && r.assignment.Generation == generation {
			return true, nil
		}
	}
	return false, nil
}
func (s *RedisStore) Stop(ctx context.Context, node int, work string) error {
	if s.client == nil {
		return ErrUnavailable
	}
	if node <= 0 || !validWork(work) {
		return ErrDenied
	}
	n, err := stopScript.Run(ctx, s.client, s.keys(node), work).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUnavailable
	}
	return nil
}
func (s *RedisStore) Cancel(ctx context.Context, node int, work string, generation uint64) error {
	if s.client == nil {
		return ErrUnavailable
	}
	n, err := cancelScript.Run(ctx, s.client, s.keys(node), work, strconv.FormatUint(generation, 10)).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFenced
	}
	return nil
}

var nonceScript = redis.NewScript(`
local now=redis.call('TIME');local ms=now[1]*1000+math.floor(now[2]/1000)
redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',ms)
if redis.call('ZSCORE',KEYS[1],ARGV[1]) then return 0 end
if redis.call('ZCARD',KEYS[1])>=65536 then return -1 end
redis.call('ZADD',KEYS[1],ms+ARGV[2],ARGV[1]);redis.call('PEXPIRE',KEYS[1],61000);return 1`)

func (s *RedisStore) ClaimNonce(ctx context.Context, node int, nonce string, expires time.Time) error {
	if s.client == nil {
		return ErrUnavailable
	}
	ttl := time.Until(expires)
	if node <= 0 || len(nonce) != 32 || ttl <= 0 || ttl > time.Minute+time.Second {
		return ErrDenied
	}
	n, err := nonceScript.Run(ctx, s.client, []string{s.prefix + fmt.Sprintf("{%d}:nonces", node)}, nonce, ttl.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrDenied
	}
	if n != 1 {
		return ErrUnavailable
	}
	return nil
}
