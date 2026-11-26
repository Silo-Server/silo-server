package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// apiReplicaKey is the sorted set of live API replicas. Each member is one
// process's presence identity, scored with its expiry in Redis milliseconds.
const apiReplicaKey = "silo:api-replicas"

// APIReplicaTTL is how long a presence marker outlives its last refresh. It is
// three refresh intervals so one missed refresh does not drop a live replica.
const APIReplicaTTL = 90 * time.Second

// apiReplicaRefreshInterval is how often a registered replica renews its marker.
const apiReplicaRefreshInterval = 30 * time.Second

// apiReplicaWriteTimeout bounds each marker write. Only a client that honors
// context deadlines, such as NewDeadlineRedisClientForRole's, ends a stalled
// write at this bound.
const apiReplicaWriteTimeout = 2 * time.Second

// presenceScript maintains the replica set in one round trip. Expiry compares
// scores with Redis TIME, so no replica's clock enters the census. It first drops
// expired members. "touch" then writes the caller's member; "count" writes it
// only if it is missing, so the count always includes the caller; "read" writes
// nothing. It returns the number of live members.
var presenceScript = redis.NewScript(`
local t = redis.call('TIME')
local now = t[1] * 1000 + math.floor(t[2] / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
local op = ARGV[3]
if op == 'touch' or (op == 'count' and redis.call('ZSCORE', KEYS[1], ARGV[1]) == false) then
  redis.call('ZADD', KEYS[1], now + ARGV[2], ARGV[1])
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return redis.call('ZCARD', KEYS[1])
`)

// APIReplicaPresence is a census of live API processes shared by network access
// providers and the direct engine gate. Count reports Redis failures to callers.
type APIReplicaPresence struct {
	client  *redis.Client
	id      string
	ttl     time.Duration
	refresh time.Duration

	mu        sync.Mutex // orders marker writes against withdrawal
	withdrawn bool
}

// NewAPIReplicaPresence returns a unique presence for this process, even when
// several replicas use the same logical node id. A nil client or empty id yields
// a nil presence, on which every method is a no-op that reports one replica.
func NewAPIReplicaPresence(client *redis.Client, id string) *APIReplicaPresence {
	if client == nil || id == "" {
		return nil
	}
	return &APIReplicaPresence{client: client, id: id + ":" + uuid.NewString(), ttl: APIReplicaTTL, refresh: apiReplicaRefreshInterval}
}

// Register writes this replica's marker and renews it until ctx ends. A failed
// or timed-out first write is returned, and renewal still starts so the marker
// appears once Redis recovers. When ctx ends, the marker is deleted and neither renewal nor
// Refresh writes it again; a presence registers once per process.
func (p *APIReplicaPresence) Register(ctx context.Context) error {
	if p == nil {
		return nil
	}
	err := p.touch(ctx)
	go func() {
		ticker := time.NewTicker(p.refresh)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				p.withdraw()
				return
			case <-ticker.C:
				_ = p.touch(ctx)
			}
		}
	}()
	return err
}

// withdraw stops every later marker write and deletes the marker. A write that
// started first completes before the delete.
func (p *APIReplicaPresence) withdraw() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.withdrawn = true
	cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.client.ZRem(cleanup, apiReplicaKey, p.id).Err()
}

// ID returns this process's unique presence identity, or empty for nil presence.
// Engine leases use the same identity as the process's presence marker.
func (p *APIReplicaPresence) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

// Refresh writes this process's marker once, with its full TTL. It starts no
// goroutine; Register owns renewal. A nil presence, or one whose Register
// context has ended, is a no-op.
func (p *APIReplicaPresence) Refresh(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return p.touch(ctx)
}

func (p *APIReplicaPresence) touch(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.withdrawn {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, apiReplicaWriteTimeout)
	defer cancel()
	if _, err := p.run(ctx, "touch"); err != nil {
		return fmt.Errorf("register api replica presence: %w", err)
	}
	return nil
}

func (p *APIReplicaPresence) run(ctx context.Context, op string) (int, error) {
	return presenceScript.Run(ctx, p.client, []string{apiReplicaKey}, p.id, p.ttl.Milliseconds(), op).Int()
}

// Count returns how many API replicas currently hold a marker, including this
// one: a missing marker of this process is written first, in the same call, so
// a lone marker of another process is never read as this one. A withdrawn
// presence writes nothing. A nil presence reports one.
func (p *APIReplicaPresence) Count(ctx context.Context) (int, error) {
	if p == nil {
		return 1, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	op := "count"
	if p.withdrawn {
		op = "read"
	}
	count, err := p.run(ctx, op)
	if err != nil {
		return 0, fmt.Errorf("count api replicas: %w", err)
	}
	return count, nil
}
