package directconn

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const engineLeaseKey = "silo:direct:engine-lease"

const (
	engineLeaseTTL      = 90 * time.Second
	engineLeaseSelfStop = 60 * time.Second
)

var renewEngineLease = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

var releaseEngineLease = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

type redisEngineLease struct {
	client    *redis.Client
	id        string
	lastRenew time.Time
}

func (l *redisEngineLease) acquire(ctx context.Context, now time.Time) (bool, error) {
	// SetNX uses EX for whole seconds; the lease contract uses PX explicitly.
	cmd := redis.NewBoolCmd(ctx, "set", engineLeaseKey, l.id, "nx", "px", engineLeaseTTL.Milliseconds())
	if err := l.client.Process(ctx, cmd); err != nil {
		return false, fmt.Errorf("acquire direct engine lease: %w", err)
	}
	held := cmd.Val()
	if held {
		l.lastRenew = now
	}
	return held, nil
}

func (l *redisEngineLease) renew(ctx context.Context, now time.Time) (bool, error) {
	result, err := renewEngineLease.Run(ctx, l.client, []string{engineLeaseKey}, l.id, engineLeaseTTL.Milliseconds()).Int()
	if err != nil {
		return false, fmt.Errorf("renew direct engine lease: %w", err)
	}
	if result == 1 {
		l.lastRenew = now
	} else {
		l.lastRenew = time.Time{}
	}
	return result == 1, nil
}

func (l *redisEngineLease) release(ctx context.Context) error {
	l.lastRenew = time.Time{}
	if err := releaseEngineLease.Run(ctx, l.client, []string{engineLeaseKey}, l.id).Err(); err != nil {
		return fmt.Errorf("release direct engine lease: %w", err)
	}
	return nil
}

func (l *redisEngineLease) held(now time.Time) bool {
	return !l.lastRenew.IsZero() && now.Sub(l.lastRenew) < engineLeaseSelfStop
}
