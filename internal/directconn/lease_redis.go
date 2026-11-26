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

// acquireEngineLease writes this process's value when the key is free and
// refreshes the TTL when the key already holds it, so a retry after a lost reply
// to an applied write succeeds instead of reading its own value as contention.
var acquireEngineLease = redis.NewScript(`
local owner = redis.call('GET', KEYS[1])
if owner == ARGV[1] then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return 1
end
if owner == false then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
  return 1
end
return 0
`)

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

// redisEngineLease is one process's claim on engineLeaseKey. A grant is one
// stretch of ownership: a confirmed acquisition opens it, and a renewal answered
// 0, a release, or a renewal that completes after the grant's own deadline ends
// it, so the next success is a new grant even for the same process. An attempt
// that fails, even if its write applied, opens nothing: the retry is idempotent
// and opens the grant when Redis confirms it.
type redisEngineLease struct {
	client *redis.Client
	id     string
	// lastRenew is when the last acquisition or renewal that succeeded was sent;
	// zero when no grant is active.
	lastRenew time.Time
	// grant counts grants opened; grantStart is when the open grant's acquisition
	// was sent, which Redis confirmed after any write by the former holder's key
	// loss. grantStart is zero when no grant is open.
	grant      uint64
	grantStart time.Time
}

// acquire opens a grant when Redis confirms the acquisition. The grant's hold
// counts from now, the moment this call was sent: an earlier attempt that failed
// says nothing about when the former holder lost the key.
func (l *redisEngineLease) acquire(ctx context.Context, now time.Time) (bool, error) {
	result, err := acquireEngineLease.Run(ctx, l.client, []string{engineLeaseKey}, l.id, engineLeaseTTL.Milliseconds()).Int()
	if err != nil {
		return false, fmt.Errorf("acquire direct engine lease: %w", err)
	}
	if result != 1 {
		return false, nil
	}
	l.open(now)
	return true, nil
}

// open starts a grant whose acquisition or renewal was sent at now.
func (l *redisEngineLease) open(now time.Time) {
	l.grant++
	l.grantStart = now
	l.lastRenew = now
}

func (l *redisEngineLease) renew(ctx context.Context, now time.Time) (bool, error) {
	result, err := renewEngineLease.Run(ctx, l.client, []string{engineLeaseKey}, l.id, engineLeaseTTL.Milliseconds()).Int()
	if err != nil {
		return false, fmt.Errorf("renew direct engine lease: %w", err)
	}
	if result == 1 {
		l.lastRenew = now
		return true, nil
	}
	l.reset()
	return false, nil
}

func (l *redisEngineLease) release(ctx context.Context) error {
	l.reset()
	if err := releaseEngineLease.Run(ctx, l.client, []string{engineLeaseKey}, l.id).Err(); err != nil {
		return fmt.Errorf("release direct engine lease: %w", err)
	}
	return nil
}

// reset ends the open grant.
func (l *redisEngineLease) reset() {
	l.lastRenew = time.Time{}
	l.grantStart = time.Time{}
}

func (l *redisEngineLease) held(now time.Time) bool {
	return !l.lastRenew.IsZero() && now.Sub(l.lastRenew) < engineLeaseSelfStop
}
