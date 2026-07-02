package downloads

import (
	"context"
	"sync"
	"time"
)

// QuantityLimiter enforces concurrent and period-based download quotas.
type QuantityLimiter struct {
	mu             sync.RWMutex
	repo           *Repository
	maxConcurrent  int
	maxPerPeriod   int
	periodDuration time.Duration
}

// NewQuantityLimiter creates a new limiter.
// Zero values for maxConcurrent or maxPerPeriod mean unlimited.
func NewQuantityLimiter(repo *Repository, maxConcurrent, maxPerPeriod int, periodDuration time.Duration) *QuantityLimiter {
	return &QuantityLimiter{
		repo:           repo,
		maxConcurrent:  maxConcurrent,
		maxPerPeriod:   maxPerPeriod,
		periodDuration: periodDuration,
	}
}

// Reload updates the quantity limits.
func (l *QuantityLimiter) Reload(maxConcurrent, maxPerPeriod int, periodDuration time.Duration) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxConcurrent = maxConcurrent
	l.maxPerPeriod = maxPerPeriod
	l.periodDuration = periodDuration
}

// Check verifies the user has not exceeded their download limits.
// The batchSize parameter accounts for series batch downloads where
// multiple records will be created at once.
func (l *QuantityLimiter) Check(ctx context.Context, userID int, batchSize int) error {
	if l == nil {
		return nil
	}

	l.mu.RLock()
	maxConc := l.maxConcurrent
	maxPer := l.maxPerPeriod
	period := l.periodDuration
	l.mu.RUnlock()

	if maxConc > 0 {
		active, err := l.repo.CountActiveByUser(ctx, userID)
		if err != nil {
			return err
		}
		if active+batchSize > maxConc {
			return ErrConcurrentLimitReached
		}
	}

	if maxPer > 0 && period > 0 {
		since := time.Now().Add(-period)
		count, err := l.repo.CountByUserSince(ctx, userID, since)
		if err != nil {
			return err
		}
		if count+batchSize > maxPer {
			return ErrPeriodLimitReached
		}
	}

	return nil
}
