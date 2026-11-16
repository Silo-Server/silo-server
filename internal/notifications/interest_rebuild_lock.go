package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// interestRebuildLock keeps one full interest rebuild running across the
// cluster: every API server runs the task manager, and every rebuild resumes
// from and advances the same notification_backfill_state checkpoint. It
// spells "SILONINT".
const interestRebuildLock int64 = 0x53494C4F4E494E54

// ErrInterestRebuildRunning reports that another server holds the interest
// rebuild lock, so the call did no work.
var ErrInterestRebuildRunning = errors.New("another server is rebuilding notification interest")

// interestRebuildClaim is this server's hold on interestRebuildLock. The lock
// lives on a session of its own, opened with the pool's configuration but
// outside it, as the audiobook enrichment sweep and storage maintenance locks
// do: the rebuild's own reads take pool connections, and with the lock's
// session in the pool a pool of one connection would wait on itself. The
// rebuild reads and writes its checkpoint on this session, so a rebuild whose
// session ended cannot write next to another server's. Closing the session
// releases the lock, including when this process dies mid-rebuild.
type interestRebuildClaim struct {
	conn *pgx.Conn
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
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, fmt.Errorf("open interest rebuild lock session: %w", err)
	}
	claim := &interestRebuildClaim{conn: conn}
	// A failed query may still have been granted the lock; closing the
	// session releases it either way.
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, interestRebuildLock).Scan(&acquired); err != nil {
		claim.release()
		return nil, fmt.Errorf("acquire interest rebuild lock: %w", err)
	}
	if !acquired {
		claim.release()
		return nil, ErrInterestRebuildRunning
	}
	return claim, nil
}

// release closes the lock's session, which releases the lock.
func (c *interestRebuildClaim) release() {
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.conn.Close(closeCtx)
}
