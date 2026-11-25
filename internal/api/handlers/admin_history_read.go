package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Insights and the Playback History page scan admin_playback_history and the
// dashboard metric tables, and an admin:monitor:read caller can start those
// scans at will: an all-time range, a filter combination nothing has cached,
// or a history search no index serves. They share one gate per process.
const (
	// adminHistoryMaxConcurrentReads bounds the gated reads one process runs
	// at once; a burst waits here instead of taking the database pool.
	adminHistoryMaxConcurrentReads = 3
	// adminHistoryReadTimeout bounds one gated read, including a shared
	// Insights cache fill that outlives the request that started it while
	// another request still waits on it.
	adminHistoryReadTimeout = 2 * time.Minute
	// adminHistoryRollbackTimeout bounds closing a read's transaction.
	adminHistoryRollbackTimeout = 5 * time.Second
)

var adminHistoryReadSlots = make(chan struct{}, adminHistoryMaxConcurrentReads)

// beginAdminHistoryRead waits for one of adminHistoryMaxConcurrentReads slots and
// opens the read-only snapshot a gated read runs in, with work_mem set to
// workMem when it is not empty (SET LOCAL ends with the transaction). The
// returned context carries adminHistoryReadTimeout for the whole read, the wait
// for a slot included, and each statement also gets it as statement_timeout
// so the server stops on its own. The caller runs its queries on that context
// and defers done, which rolls back an open transaction within
// adminHistoryRollbackTimeout and frees the slot.
func beginAdminHistoryRead(ctx context.Context, pool *pgxpool.Pool, workMem string) (readCtx context.Context, tx pgx.Tx, done func(), err error) {
	readCtx, cancel := context.WithTimeout(ctx, adminHistoryReadTimeout)
	select {
	case adminHistoryReadSlots <- struct{}{}:
	case <-readCtx.Done():
		cancel()
		return nil, nil, nil, readCtx.Err()
	}
	release := func() {
		<-adminHistoryReadSlots
		cancel()
	}
	tx, err = pool.BeginTx(readCtx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		release()
		return nil, nil, nil, err
	}
	done = func() {
		// Rollback runs even after the read's context ends, but within its
		// own deadline: a stalled connection must not keep the slot.
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(readCtx), adminHistoryRollbackTimeout)
		_ = tx.Rollback(rollbackCtx)
		cancelRollback()
		release()
	}
	settings := []string{fmt.Sprintf("SET LOCAL statement_timeout = '%dms'", adminHistoryReadTimeout.Milliseconds())}
	if workMem != "" {
		settings = append(settings, "SET LOCAL work_mem = '"+workMem+"'")
	}
	for _, stmt := range settings {
		if _, err := tx.Exec(readCtx, stmt); err != nil {
			done()
			return nil, nil, nil, fmt.Errorf("configuring admin history read: %w", err)
		}
	}
	return readCtx, tx, done, nil
}
