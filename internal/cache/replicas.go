package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// apiReplicaKeyPrefix keys one live API replica's presence marker.
const apiReplicaKeyPrefix = "silo:api-replica:"

// APIReplicaTTL is how long a presence marker outlives its last refresh. It is
// three refresh intervals so one missed refresh does not drop a live replica.
const APIReplicaTTL = 90 * time.Second

// apiReplicaRefreshInterval is how often a registered replica renews its marker.
const apiReplicaRefreshInterval = 30 * time.Second

// apiReplicaWriteTimeout bounds each marker write. Only a client that honors
// context deadlines, such as NewDeadlineRedisClientForRole's, ends a stalled
// write at this bound.
const apiReplicaWriteTimeout = 2 * time.Second

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
	_ = p.client.Del(cleanup, p.key()).Err()
}

func (p *APIReplicaPresence) key() string { return apiReplicaKeyPrefix + p.id }

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
	if err := p.client.Set(ctx, p.key(), time.Now().UTC().Format(time.RFC3339), p.ttl).Err(); err != nil {
		return fmt.Errorf("register api replica presence: %w", err)
	}
	return nil
}

// Count returns how many API replicas currently hold a marker, including this
// one. A nil presence reports one.
func (p *APIReplicaPresence) Count(ctx context.Context) (int, error) {
	if p == nil {
		return 1, nil
	}
	var (
		cursor uint64
		count  int
	)
	for {
		keys, next, err := p.client.Scan(ctx, cursor, apiReplicaKeyPrefix+"*", 100).Result()
		if err != nil {
			return 0, fmt.Errorf("count api replicas: %w", err)
		}
		count += len(keys)
		if next == 0 {
			return count, nil
		}
		cursor = next
	}
}
