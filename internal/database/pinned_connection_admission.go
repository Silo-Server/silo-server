package database

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

type pinnedConnectionAdmission struct {
	slots chan struct{}
}

func (a *pinnedConnectionAdmission) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("waiting for pinned PostgreSQL connection admission: %w", err)
	}
	select {
	case a.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-a.slots }) }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for pinned PostgreSQL connection admission: %w", ctx.Err())
	}
}

type pinnedConnectionAdmissionRegistry struct {
	mu     sync.Mutex
	byPool map[*pgxpool.Pool]*pinnedConnectionAdmission
}

func (r *pinnedConnectionAdmissionRegistry) forPool(pool *pgxpool.Pool, capacity int) *pinnedConnectionAdmission {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byPool == nil {
		r.byPool = make(map[*pgxpool.Pool]*pinnedConnectionAdmission)
	}
	if admission := r.byPool[pool]; admission != nil {
		return admission
	}
	admission := &pinnedConnectionAdmission{slots: make(chan struct{}, capacity)}
	r.byPool[pool] = admission
	return admission
}

var pinnedConnectionAdmissions pinnedConnectionAdmissionRegistry

func pinnedConnectionCapacity(maxConns int32) int {
	if maxConns < 2 {
		return 0
	}
	return int(maxConns / 2)
}

// PinnedConnectionCapacity is the shared budget for operations that pin one
// pooled connection while issuing protected work through the same pool. At
// least half of the pool remains available to that inner work.
func PinnedConnectionCapacity(pool *pgxpool.Pool) int {
	if pool == nil {
		return 0
	}
	return pinnedConnectionCapacity(pool.Config().MaxConns)
}

// AcquirePinnedConnectionSlot admits a long-lived pooled-connection holder.
// Operations using the same pool pointer share one budget.
func AcquirePinnedConnectionSlot(ctx context.Context, pool *pgxpool.Pool) (func(), error) {
	if pool == nil {
		return nil, errors.New("acquiring pinned PostgreSQL connection admission: pool is not configured")
	}
	capacity := PinnedConnectionCapacity(pool)
	if capacity == 0 {
		return nil, fmt.Errorf(
			"pinned PostgreSQL connection admission requires pool MaxConns >= 2 (configured %d)",
			pool.Config().MaxConns,
		)
	}
	return pinnedConnectionAdmissions.forPool(pool, capacity).acquire(ctx)
}
