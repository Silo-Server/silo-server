package workercontrolcore

import (
	"context"
	"errors"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// GenerationStore binds destructive commands to the authority they observed.
// A delayed command/callback cannot revoke a newer dispatch under the same ID.
type GenerationStore interface {
	Generation(context.Context, int, string) (uint64, error)
	StopIfGeneration(context.Context, int, string, uint64) error
}

func (s *MemoryStore) Generation(ctx context.Context, node int, work string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.versions[workKey{node, work}], nil
}
func (s *MemoryStore) StopIfGeneration(ctx context.Context, node int, work string, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if node <= 0 || !validWork(work) {
		return ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	if s.versions[k] != generation {
		return ErrFenced
	}
	if s.versions[k] < 1<<53-1 {
		s.versions[k]++
	}
	delete(s.live, k)
	delete(s.candidates, k)
	return nil
}
func (s *RedisStore) Generation(ctx context.Context, node int, work string) (uint64, error) {
	if s.client == nil {
		return 0, ErrUnavailable
	}
	raw, err := s.client.HGet(ctx, s.keys(node)[0], work).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(raw, 10, 64)
}

var stopIfGenerationScript = redis.NewScript(`
local current=redis.call('HGET',KEYS[1],ARGV[1]) or '0'
if current~=ARGV[2] then return 0 end
if tonumber(current)<9007199254740991 then redis.call('HINCRBY',KEYS[1],ARGV[1],1) end
for _,key in ipairs({KEYS[2],KEYS[3]}) do local old=redis.call('HGET',key,ARGV[1]);if old then local field=ARGV[1]..'\31'..string.match(old,'^(%d+)\n');redis.call('HDEL',KEYS[4],field);redis.call('HDEL',KEYS[5],field) end;redis.call('HDEL',key,ARGV[1]) end
return 1`)

func (s *RedisStore) StopIfGeneration(ctx context.Context, node int, work string, generation uint64) error {
	if s.client == nil {
		return ErrUnavailable
	}
	if node <= 0 || !validWork(work) {
		return ErrDenied
	}
	n, err := stopIfGenerationScript.Run(ctx, s.client, s.keys(node), work, strconv.FormatUint(generation, 10)).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFenced
	}
	return nil
}
