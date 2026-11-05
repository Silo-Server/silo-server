// Package readtx runs one read statement in a read-only transaction whose
// SET LOCAL settings travel in the same pipelined batch, so the settings cost
// no extra round trip.
package readtx

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// NoJIT disables JIT compilation for the statement.
	NoJIT = "SET LOCAL jit = off"
	// GenericPlan makes the connection's cached statement keep one generic
	// plan instead of planning again for each call's values. Use it only for
	// a statement whose generic plan suits every value it is called with.
	GenericPlan = "SET LOCAL plan_cache_mode = force_generic_plan"
)

// Query runs query after the SET LOCAL settings and hands its rows to read.
// The transaction goes out as one pipelined batch: one round trip rather than
// one per statement.
func Query(ctx context.Context, pool *pgxpool.Pool, query string, args []any, read func(pgx.Rows) error, settings ...string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	batch := &pgx.Batch{}
	batch.Queue("BEGIN READ ONLY")
	for _, setting := range settings {
		batch.Queue(setting)
	}
	batch.Queue(query, args...).Query(read)
	batch.Queue("COMMIT")
	err = conn.SendBatch(ctx, batch).Close()
	// A failed statement skips the COMMIT and leaves the transaction aborted.
	// Roll it back so the pool keeps the connection instead of discarding it.
	if pgConn := conn.Conn().PgConn(); !pgConn.IsClosed() && pgConn.TxStatus() != 'I' {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, _ = conn.Exec(rollbackCtx, "ROLLBACK")
	}
	return err
}
