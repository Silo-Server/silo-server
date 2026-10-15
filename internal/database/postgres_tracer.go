package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

// poolTracer preserves the coarse tracer's callback behavior, including duplicate
// batch ends. Attribution ends first while the coarse dependency span is open.
type poolTracer struct {
	coarse      postgresTracer
	attribution queryattr.Tracer
}

func (t poolTracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return t.attribution.TraceQueryStart(t.coarse.TraceQueryStart(ctx, c, d), c, d)
}
func (t poolTracer) TraceQueryEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryEndData) {
	t.attribution.TraceQueryEnd(ctx, c, d)
	t.coarse.TraceQueryEnd(ctx, c, d)
}
func (t poolTracer) TraceBatchStart(ctx context.Context, c *pgx.Conn, d pgx.TraceBatchStartData) context.Context {
	return t.attribution.TraceBatchStart(t.coarse.TraceBatchStart(ctx, c, d), c, d)
}
func (t poolTracer) TraceBatchQuery(ctx context.Context, c *pgx.Conn, d pgx.TraceBatchQueryData) {
	t.attribution.TraceBatchQuery(ctx, c, d)
	t.coarse.TraceBatchQuery(ctx, c, d)
}
func (t poolTracer) TraceBatchEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceBatchEndData) {
	t.attribution.TraceBatchEnd(ctx, c, d)
	t.coarse.TraceBatchEnd(ctx, c, d)
}
func (t poolTracer) TraceCopyFromStart(ctx context.Context, c *pgx.Conn, d pgx.TraceCopyFromStartData) context.Context {
	return t.attribution.TraceCopyFromStart(t.coarse.TraceCopyFromStart(ctx, c, d), c, d)
}
func (t poolTracer) TraceCopyFromEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceCopyFromEndData) {
	t.attribution.TraceCopyFromEnd(ctx, c, d)
	t.coarse.TraceCopyFromEnd(ctx, c, d)
}
func (t poolTracer) TraceConnectStart(ctx context.Context, d pgx.TraceConnectStartData) context.Context {
	return t.coarse.TraceConnectStart(ctx, d)
}
func (t poolTracer) TraceConnectEnd(ctx context.Context, d pgx.TraceConnectEndData) {
	t.coarse.TraceConnectEnd(ctx, d)
}
func (t poolTracer) TraceAcquireStart(ctx context.Context, p *pgxpool.Pool, d pgxpool.TraceAcquireStartData) context.Context {
	return t.attribution.TraceAcquireStart(t.coarse.TraceAcquireStart(ctx, p, d), p, d)
}
func (t poolTracer) TraceAcquireEnd(ctx context.Context, p *pgxpool.Pool, d pgxpool.TraceAcquireEndData) {
	t.attribution.TraceAcquireEnd(ctx, p, d)
	t.coarse.TraceAcquireEnd(ctx, p, d)
}

func configurePostgresTracing(cfg *pgxpool.Config, role string, enabled bool) {
	cfg.AfterConnect = disableUnconfiguredJIT
	cfg.ConnConfig.Tracer = postgresTracer{role: role}
	if enabled {
		tracer := queryattr.Tracer{}
		cfg.ConnConfig.Tracer = poolTracer{coarse: postgresTracer{role: role}, attribution: tracer}
		cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
			return disableUnconfiguredJIT(queryattr.Internal(ctx, queryattr.ConnectionSetup), c)
		}
		cfg.BeforeClose = tracer.BeforeClose
	}
}
