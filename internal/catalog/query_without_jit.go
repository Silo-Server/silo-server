package catalog

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// queryWithoutJIT runs one read statement with JIT disabled and scans each
// row. Pool connections already start with JIT off unless the operator
// configured it, so SET LOCAL only matters where JIT was turned on. The
// transaction goes out as one pipelined batch: one round trip rather than one
// per statement.
func queryWithoutJIT(ctx context.Context, pool *pgxpool.Pool, query string, args []any, scan func(pgx.Rows) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	batch := &pgx.Batch{}
	batch.Queue("BEGIN READ ONLY")
	batch.Queue("SET LOCAL jit = off")
	batch.Queue(query, args...).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	})
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
