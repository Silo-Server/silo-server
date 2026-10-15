package database

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

// BatchSender is implemented by pgx pools, acquired connections and transactions.
type BatchSender interface {
	SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
}

// SendBatch forwards the batch without changing its results or lifecycle.
func SendBatch(ctx context.Context, sender BatchSender, batch *pgx.Batch) pgx.BatchResults {
	if batch.Len() == 0 && queryattr.Enabled() {
		(queryattr.Tracer{}).CountEmptyBatch()
	}
	return sender.SendBatch(ctx, batch)
}

// CopyFrom forwards a transaction copy without inspecting its source.
func CopyFrom(ctx context.Context, tx pgx.Tx, table pgx.Identifier, columns []string, source pgx.CopyFromSource) (int64, error) {
	conn := tx.Conn()
	tr, on := copyTracer(conn)
	if !on {
		return tx.CopyFrom(ctx, table, columns, source)
	}
	queryattr.ClearCopyTicket(conn)
	n, err := tx.CopyFrom(ctx, table, columns, source)
	if c := tr.attribution.ResolveCopySetup(conn, n, err); c != nil {
		tr.coarse.TraceCopyFromEnd(c, conn, pgx.TraceCopyFromEndData{Err: err})
	}
	return n, err
}

type copyTraceConfig struct {
	tracer poolTracer
	on     bool
}

// Conn.Config copies maps. Cache its tracer once per connection so repeated
// copies add only the lifecycle record allocation on the warmed path.
func copyTracer(conn *pgx.Conn) (poolTracer, bool) {
	const key = "silo.copy_tracer"
	data := conn.PgConn().CustomData()
	if cached, ok := data[key].(copyTraceConfig); ok {
		return cached.tracer, cached.on
	}
	tr, on := conn.Config().Tracer.(poolTracer)
	data[key] = copyTraceConfig{tr, on}
	return tr, on
}
