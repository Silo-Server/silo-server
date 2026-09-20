package cache

import (
	"context"
	"fmt"
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

// APIReplicaPresence is a best-effort census of live API replicas. Network
// access providers keep their overlay node key under one shared "api" state
// scope, so two API replicas would present the same overlay identity from two
// machines; the resident supervisor warns when it sees more than one. Nothing
// routes on this count and a Redis failure only silences the warning.
type APIReplicaPresence struct {
	client  *redis.Client
	id      string
	ttl     time.Duration
	refresh time.Duration
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

// Register writes this replica's marker and renews it until ctx ends.
func (p *APIReplicaPresence) Register(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if err := p.touch(ctx); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(p.refresh)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = p.client.Del(cleanup, p.key()).Err()
				cancel()
				return
			case <-ticker.C:
				_ = p.touch(ctx)
			}
		}
	}()
	return nil
}

func (p *APIReplicaPresence) key() string { return apiReplicaKeyPrefix + p.id }

func (p *APIReplicaPresence) touch(ctx context.Context) error {
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
