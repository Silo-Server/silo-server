package workercontrolcore

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

const admissionLeaseTTL = 2 * time.Second

// AdmissionLeases are short, core-owned leases renewed only while the
// originating dispatch lives. Durable candidate records alone grant nothing.
type AdmissionLeases interface {
	TouchLease(context.Context, int, string, uint64, time.Duration) error
	HasLease(context.Context, int, string, uint64) (bool, error)
}

func (s *MemoryStore) TouchLease(ctx context.Context, node int, work string, generation uint64, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ttl <= 0 || ttl > admissionLeaseTTL {
		return ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	for _, records := range []map[workKey]record{s.live, s.candidates} {
		if r, ok := records[k]; ok && r.assignment.Generation == generation {
			r.lease = time.Now().Add(ttl)
			records[k] = r
			return nil
		}
	}
	return ErrFenced
}
func (s *MemoryStore) HasLease(ctx context.Context, node int, work string, generation uint64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	for _, records := range []map[workKey]record{s.live, s.candidates} {
		if r, ok := records[k]; ok && (generation == 0 || r.assignment.Generation == generation) && time.Now().Before(r.lease) {
			return true, nil
		}
	}
	return false, nil
}

var leaseScript = redis.NewScript(`
local prefix=ARGV[2]..'\n'
local live=redis.call('HGET',KEYS[3],ARGV[1]);local candidate=redis.call('HGET',KEYS[2],ARGV[1])
if (not live or string.sub(live,1,string.len(prefix))~=prefix) and (not candidate or string.sub(candidate,1,string.len(prefix))~=prefix) then return 0 end
local now=redis.call('TIME');local ms=now[1]*1000+math.floor(now[2]/1000)
redis.call('HSET',KEYS[4],ARGV[1]..'\31'..ARGV[2],ms+ARGV[3]);return 1`)
var hasLeaseScript = redis.NewScript(`
local now=redis.call('TIME');local ms=now[1]*1000+math.floor(now[2]/1000)
for _,key in ipairs({KEYS[3],KEYS[2]}) do
 local record=redis.call('HGET',key,ARGV[1])
 if record then
  local generation=string.match(record,'^(%d+)\n')
  if ARGV[2]=='0' or ARGV[2]==generation then
   local expiry=tonumber(redis.call('HGET',KEYS[4],ARGV[1]..'\31'..generation) or '0')
   if expiry>ms then return 1 end
  end
 end
end
return 0`)

func (s *RedisStore) TouchLease(ctx context.Context, node int, work string, generation uint64, ttl time.Duration) error {
	if s.client == nil {
		return ErrUnavailable
	}
	if node <= 0 || !validWork(work) || generation == 0 || ttl <= 0 || ttl > admissionLeaseTTL {
		return ErrDenied
	}
	n, err := leaseScript.Run(ctx, s.client, s.keys(node), work, strconv.FormatUint(generation, 10), ttl.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFenced
	}
	return nil
}
func (s *RedisStore) HasLease(ctx context.Context, node int, work string, generation uint64) (bool, error) {
	if s.client == nil {
		return false, ErrUnavailable
	}
	n, err := hasLeaseScript.Run(ctx, s.client, s.keys(node), work, strconv.FormatUint(generation, 10)).Int()
	return err == nil && n == 1, err
}
func (s *Service) sharedLease(ctx context.Context, node int, work string, generation uint64) bool {
	leases, ok := s.options.Store.(AdmissionLeases)
	if !ok {
		return false
	}
	active, err := leases.HasLease(ctx, node, work, generation)
	return err == nil && active
}
func (s *Service) renewAdmission(ctx context.Context, a workerwire.Assignment) {
	leases, ok := s.options.Store.(AdmissionLeases)
	if !ok {
		return
	}
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		renewCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := leases.TouchLease(renewCtx, a.NodeID, a.WorkID, a.Generation, admissionLeaseTTL)
		cancel()
		if err != nil {
			s.clearAdmission(a)
			return
		}
	}
}
