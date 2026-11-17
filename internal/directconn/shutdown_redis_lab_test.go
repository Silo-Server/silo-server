//go:build redislab

package directconn

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestShutdownOrderRedis runs through scripts/direct-redis-lab.sh against a
// disposable Redis: with the replica census and the engine lease on, Shutdown
// leaves no lease key behind, so another replica can take over at once.
func TestShutdownOrderRedis(t *testing.T) {
	client := labClient(t)
	rig := newShutdownRig(t, client)
	close(rig.drainGate)
	if err := client.Get(t.Context(), engineLeaseKey).Err(); err != nil {
		t.Fatalf("lease key while the engine runs: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := rig.svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := client.Get(t.Context(), engineLeaseKey).Err(); !errors.Is(err, redis.Nil) {
		t.Fatalf("lease key after Shutdown: %v, want redis.Nil", err)
	}
}

// TestShutdownReleasesLeaseWithOpenStream: a direct request that stays open
// through the drain is cut at the caller's deadline minus drainReserve, and the
// lease is released before that deadline.
func TestShutdownReleasesLeaseWithOpenStream(t *testing.T) {
	client := labClient(t)
	rig := newShutdownRig(t, client) // The drain gate stays shut: the stream never ends by itself.
	ctx, cancel := context.WithTimeout(context.Background(), drainReserve+3*time.Second)
	defer cancel()
	if err := rig.svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("the caller's deadline passed before Shutdown returned")
	}
	if err := client.Get(t.Context(), engineLeaseKey).Err(); !errors.Is(err, redis.Nil) {
		t.Fatalf("lease key after Shutdown: %v, want redis.Nil", err)
	}
}
