package workercontrolcore

import (
	"context"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func (s *MemoryStore) Snapshot(ctx context.Context, node int) (AuthoritySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return AuthoritySnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := AuthoritySnapshot{Active: map[string]workerwire.Assignment{}, Pending: map[string]workerwire.Assignment{}}
	for k, r := range s.live {
		if k.node == node && r.ready {
			out.Active[k.work] = copyAssignment(r.assignment)
		}
	}
	for k, r := range s.candidates {
		if k.node == node && r.ready {
			out.Pending[k.work] = copyAssignment(r.assignment)
		}
	}
	return out, nil
}

var snapshotScript = redis.NewScript(`return {redis.call('HGETALL',KEYS[3]),redis.call('HGETALL',KEYS[2])}`)

func snapshotRecords(raw any, node int) (map[string]workerwire.Assignment, error) {
	values, ok := raw.([]any)
	if !ok || len(values)%2 != 0 {
		return nil, ErrUnavailable
	}
	out := map[string]workerwire.Assignment{}
	for i := 0; i < len(values); i += 2 {
		work, ok := values[i].(string)
		if !ok {
			return nil, ErrUnavailable
		}
		value, ok := values[i+1].(string)
		if !ok {
			return nil, ErrUnavailable
		}
		r, err := decodeRecord(value)
		if err != nil || r.assignment.NodeID != node || r.assignment.WorkID != work {
			return nil, ErrUnavailable
		}
		if r.ready {
			out[work] = r.assignment
		}
	}
	return out, nil
}
func (s *RedisStore) Snapshot(ctx context.Context, node int) (AuthoritySnapshot, error) {
	if s.client == nil {
		return AuthoritySnapshot{}, ErrUnavailable
	}
	raw, err := snapshotScript.Run(ctx, s.client, s.keys(node)).Slice()
	if err != nil {
		return AuthoritySnapshot{}, err
	}
	if len(raw) != 2 {
		return AuthoritySnapshot{}, ErrUnavailable
	}
	active, err := snapshotRecords(raw[0], node)
	if err != nil {
		return AuthoritySnapshot{}, err
	}
	pending, err := snapshotRecords(raw[1], node)
	if err != nil {
		return AuthoritySnapshot{}, err
	}
	return AuthoritySnapshot{Active: active, Pending: pending}, nil
}
