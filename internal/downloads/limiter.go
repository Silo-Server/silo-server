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
	return l.CheckCounts(ctx, userID, batchSize, batchSize)
}

// CheckCounts is Check with separate counts: activating is how many downloads
// become active, created how many new downloads are created. Replacing an
// entry in place can make it active without creating one.
func (l *QuantityLimiter) CheckCounts(ctx context.Context, userID int, activating, created int) error {
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
		if active+activating > maxConc {
			return ErrConcurrentLimitReached
		}
	}

	if maxPer > 0 && period > 0 {
		since := time.Now().Add(-period)
		count, err := l.repo.CountByUserSince(ctx, userID, since)
		if err != nil {
			return err
		}
		if count+created > maxPer {
			return ErrPeriodLimitReached
		}
	}

	return nil
}

// FreeConcurrentSlots reports how many more downloads userID may have active
// under the concurrent cap, or -1 when there is no cap. Monitors use it to
// pace prepared episodes instead of being refused outright.
func (l *QuantityLimiter) FreeConcurrentSlots(ctx context.Context, userID int) (int, error) {
	if l == nil {
		return -1, nil
	}
	l.mu.RLock()
	maxConc := l.maxConcurrent
	l.mu.RUnlock()
	if maxConc <= 0 {
		return -1, nil
	}
	active, err := l.repo.CountActiveByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	return max(0, maxConc-active), nil
}
