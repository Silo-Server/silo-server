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
redis.call('SET',KEYS[2],ARGV[3],'EX',86400);return 1`)

func (s *RedisStore) bindingKey(node int, work string, generation uint64) string {
	return s.prefix + "{" + strconv.Itoa(node) + "}:input:" + work + ":" + strconv.FormatUint(generation, 10)
}
func (s *RedisStore) BindInputs(ctx context.Context, node int, work string, generation uint64, bindings map[string]string) error {
	if s.client == nil {
		return ErrUnavailable
	}
	encoded, err := json.Marshal(bindings)
	if err != nil {
		return err
	}
	n, err := bindingsScript.Run(ctx, s.client, []string{s.keys(node)[1], s.bindingKey(node, work, generation)}, work, strconv.FormatUint(generation, 10), string(encoded)).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFenced
	}
	return nil
}
func (s *RedisStore) InputBindings(ctx context.Context, node int, work string, generation uint64) (map[string]string, error) {
	active, err := s.IsActive(ctx, node, work, generation)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, ErrFenced
	}
	raw, err := s.client.Get(ctx, s.bindingKey(node, work, generation)).Bytes()
	if err != nil {
		return nil, err
	}
	var bindings map[string]string
	if err = json.Unmarshal(raw, &bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}
