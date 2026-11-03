package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// Waiter paces calls: Wait returns once the next call may go, or with ctx's
// error once ctx ends first. *rate.Limiter is one.
type Waiter interface {
	Wait(ctx context.Context) error
}

// NewOutboundLimiter returns the limiter for calls Silo makes to an outside
// service, such as TMDB, that bounds requests per second for the whole
// deployment. With a Redis client every node spends one budget of perSecond
// calls a second under key; without one the budget is this process's, as a
// token bucket with a burst of perSecond.
//
// client should honor context deadlines, as one from
// cache.NewDeadlineRedisClientForRole does (see NewRedisLimiter). While Redis
// is down, each node counts its own calls until it answers again.
func NewOutboundLimiter(client *redis.Client, key string, perSecond int) Waiter {
	if client == nil {
		return rate.NewLimiter(rate.Limit(perSecond), perSecond)
	}
	return newSharedWaiter(NewRedisLimiter(client), key, perSecond)
}

// sharedWaiter spends a budget counted in Redis's wall-clock second windows.
type sharedWaiter struct {
	limiter *RedisLimiter
	key     string
	rate    Rate
	now     func() time.Time
	after   func(time.Duration) <-chan time.Time
	// turn admits one waiter at a time to Redis. The rest wait here
	// without calling it, so a backlog of waiters costs Redis one refused
	// check per node a second rather than one per waiter.
	turn chan struct{}
}

func newSharedWaiter(limiter *RedisLimiter, key string, perSecond int) *sharedWaiter {
	return &sharedWaiter{
		limiter: limiter,
		key:     key,
		rate: Rate{
			RequestsPerSecond: float64(perSecond),
			RequestsPerMinute: float64(perSecond) * 60,
			Burst:             perSecond,
		},
		now:   time.Now,
		after: time.After,
		turn:  make(chan struct{}, 1),
	}
}

func (w *sharedWaiter) Wait(ctx context.Context) error {
	select {
	case w.turn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-w.turn }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := w.now()
		if w.limiter.allowAt(ctx, w.key, w.rate, now).Allowed {
			// A check whose caller gave up mid-call is counted on this node
			// and may be allowed; the caller still gets its context error.
			return ctx.Err()
		}
		// The minute window holds sixty seconds' worth, so only the second
		// window refuses, and it opens again at the next wall-clock second.
		// The result's RetryAfter is Redis's whole-second TTL, too coarse to
		// wait on.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.after(now.Truncate(time.Second).Add(time.Second).Sub(now)):
		}
	}
}
