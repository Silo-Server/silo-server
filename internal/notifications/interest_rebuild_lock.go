package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
)

// interestRebuildLock keeps one full interest rebuild running across the
// cluster: every API server runs the task manager, and every rebuild resumes
// from and advances the same notification_backfill_state checkpoint. It
// spells "SILONINT".
const interestRebuildLock int64 = 0x53494C4F4E494E54

// interestRebuildLockCheckTimeout bounds each probe of the lock's session, so
// a connection that stopped answering fails the run instead of stalling it.
const interestRebuildLockCheckTimeout = 10 * time.Second

// ErrInterestRebuildRunning reports that another server holds the interest
// rebuild lock, so the call did no work.
var ErrInterestRebuildRunning = errors.New("another server is rebuilding notification interest")

// interestRebuildClaim is this server's hold on interestRebuildLock.
type interestRebuildClaim struct {
	lock   *pglock.Lock
	logger *slog.Logger
}

// claimInterestRebuild takes interestRebuildLock without waiting and returns
// ErrInterestRebuildRunning when another server holds it. Skipping is enough:
// the running rebuild works through the remaining profiles from the shared
// checkpoint, and waiting for it would only repeat the whole pass once it
// completes.
func (s *System) claimInterestRebuild(ctx context.Context) (*interestRebuildClaim, error) {
	if s.pool == nil {
		return nil, errors.New("interest rebuild requires a database pool")
	}
	lock, acquired, err := pglock.TryAcquire(ctx, s.pool, interestRebuildLock)
	if err != nil {
		return nil, fmt.Errorf("acquire interest rebuild lock: %w", err)
	}
	if !acquired {
		return nil, ErrInterestRebuildRunning
	}
	return &interestRebuildClaim{lock: lock, logger: s.logger}, nil
}

// check returns an error once the lock's session has closed, which released
// the lock. Another server may then start a rebuild, so this one stops before
// it writes the checkpoint again.
func (c *interestRebuildClaim) check(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, interestRebuildLockCheckTimeout)
	defer cancel()
	if err := c.lock.Check(checkCtx); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("interest rebuild lost its cluster lock: %w", err)
	}
	return nil
}

// release unlocks interestRebuildLock. pglock gives the unlock its own
// context, so a canceled run still releases cleanly.
func (c *interestRebuildClaim) release(ctx context.Context) {
	if err := c.lock.Release(ctx); err != nil {
		c.logger.WarnContext(ctx, "interest rebuild: releasing cluster lock failed", "error", err)
	}
}
