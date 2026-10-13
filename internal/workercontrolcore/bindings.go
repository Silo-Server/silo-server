package workercontrolcore

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/redis/go-redis/v9"
)

func copyBindings(bindings map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range bindings {
		out[k] = v
	}
	return out
}
func (s *MemoryStore) BindInputs(ctx context.Context, node int, work string, generation uint64, bindings map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	r, ok := s.candidates[k]
	if !ok || r.assignment.Generation != generation {
		return ErrFenced
	}
	r.bindings = copyBindings(bindings)
	s.candidates[k] = r
	return nil
}
func (s *MemoryStore) InputBindings(ctx context.Context, node int, work string, generation uint64) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	for _, records := range []map[workKey]record{s.live, s.candidates} {
		if r, ok := records[k]; ok && r.assignment.Generation == generation {
			return copyBindings(r.bindings), nil
		}
	}
	return nil, ErrFenced
}

var bindingsScript = redis.NewScript(`
local current=redis.call('HGET',KEYS[1],ARGV[1])
if not current or string.sub(current,1,string.len(ARGV[2])+1)~=ARGV[2]..'\n' then return 0 end
redis.call('HSET',KEYS[2],ARGV[1]..'\31'..ARGV[2],ARGV[3]);return 1`)

// Private bindings live exactly as long as the candidate/live generation. The
// generation transitions remove their hash field atomically with ownership.
func (s *RedisStore) BindInputs(ctx context.Context, node int, work string, generation uint64, bindings map[string]string) error {
	if s.client == nil {
		return ErrUnavailable
	}
	encoded, err := json.Marshal(bindings)
	if err != nil {
		return err
	}
	keys := s.keys(node)
	n, err := bindingsScript.Run(ctx, s.client, []string{keys[1], keys[4]}, work, strconv.FormatUint(generation, 10), string(encoded)).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFenced
	}
	return nil
}

var inputBindingsScript = redis.NewScript(`
for _,key in ipairs({KEYS[2],KEYS[3]}) do
 local current=redis.call('HGET',key,ARGV[1])
 if current and string.sub(current,1,string.len(ARGV[2])+1)==ARGV[2]..'\n' then
  local value=redis.call('HGET',KEYS[5],ARGV[1]..'\31'..ARGV[2])
  if not value then return -2 end
  return value
 end
end
return -1`)

func (s *RedisStore) InputBindings(ctx context.Context, node int, work string, generation uint64) (map[string]string, error) {
	if s.client == nil {
		return nil, ErrUnavailable
	}
	value, err := inputBindingsScript.Run(ctx, s.client, s.keys(node), work, strconv.FormatUint(generation, 10)).Result()
	if err != nil {
		return nil, err
	}
	if code, ok := value.(int64); ok && code == -1 {
		return nil, ErrFenced
	}
	raw, ok := value.(string)
	if !ok || raw == "" {
		return nil, ErrUnavailable
	}
	var bindings map[string]string
	if err = json.Unmarshal([]byte(raw), &bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}
