package database

import (
	"context"
	"strings"
	"sync"
	"weak"

	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

const postgresOperationOther = "other"

type postgresTraceKey struct{ operation string }
type postgresTracer struct{ role string }

// The first token is deliberately allowlisted. Comments and CTEs are reported
// as other/with; no SQL text, statement name, arguments or table names escape.
func queryOperation(sql string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(sql), " ")
	if len(first) > 16 {
		return postgresOperationOther
	}
	switch strings.ToLower(first) {
	case "select", "insert", "update", "delete", "begin", "commit", "rollback", "with", "set", "show":
		return strings.ToLower(first)
	default:
		return postgresOperationOther
	}
}

func (t postgresTracer) start(ctx context.Context, slot, operation string) context.Context {
	ctx, finish := telemetry.StartDependency(ctx, "postgres", t.role, operation)
	return context.WithValue(ctx, postgresTraceKey{slot}, finish)
}
func endPostgres(ctx context.Context, slot string, err error) {
	if finish, ok := ctx.Value(postgresTraceKey{slot}).(func(error)); ok {
		finish(err)
	}
}
func (t postgresTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return t.start(ctx, "query", queryOperation(d.SQL))
}
func (t postgresTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	endPostgres(ctx, "query", d.Err)
}
func (t postgresTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return t.start(ctx, "batch", "batch")
}
func (t postgresTracer) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (t postgresTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceBatchEndData) {
	endPostgres(ctx, "batch", d.Err)
}
func (t postgresTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceCopyFromStartData) context.Context {
	return t.start(ctx, "copy", "copy")
}
func (t postgresTracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceCopyFromEndData) {
	endPostgres(ctx, "copy", d.Err)
}
func (t postgresTracer) TraceConnectStart(ctx context.Context, _ pgx.TraceConnectStartData) context.Context {
	return t.start(ctx, "connect", "connect")
}
func (t postgresTracer) TraceConnectEnd(ctx context.Context, d pgx.TraceConnectEndData) {
	endPostgres(ctx, "connect", d.Err)
}
func (t postgresTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	return t.start(ctx, "acquire", "acquire")
}
func (t postgresTracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, d pgxpool.TraceAcquireEndData) {
	endPostgres(ctx, "acquire", d.Err)
}

type postgresPoolCollector struct {
	mu          sync.Mutex
	pools       map[weak.Pointer[pgxpool.Pool]]string
	connections *prometheus.Desc
}

var postgresPools = &postgresPoolCollector{
	pools:       make(map[weak.Pointer[pgxpool.Pool]]string),
	connections: prometheus.NewDesc("silo_postgres_pool_connections", "Connections in live PostgreSQL pools by role and state; maximum is configured capacity.", []string{"role", "state"}, nil),
}

func init() { prometheus.MustRegister(postgresPools) }

func (c *postgresPoolCollector) add(pool *pgxpool.Pool, role string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p := range c.pools {
		if p.Value() == nil {
			delete(c.pools, p)
		}
	}
	c.pools[weak.Make(pool)] = role
}

// ClosePool removes the pool's capacity gauges when its owning component stops.
// Operation counters remain monotonic across pool replacement.
func ClosePool(pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	postgresPools.mu.Lock()
	delete(postgresPools.pools, weak.Make(pool))
	postgresPools.mu.Unlock()
	pool.Close()
}

// Collect reads only pgx's in-memory pool statistics. Weak references ensure
// instrumentation cannot keep retired pools/connections alive.
func (c *postgresPoolCollector) Collect(ch chan<- prometheus.Metric) {
	totals := map[string][4]float64{}
	c.mu.Lock()
	for ref, role := range c.pools {
		pool := ref.Value()
		if pool == nil {
			delete(c.pools, ref)
			continue
		}
		s := pool.Stat()
		v := totals[role]
		v[0] += float64(s.AcquiredConns())
		v[1] += float64(s.IdleConns())
		v[2] += float64(s.ConstructingConns())
		v[3] += float64(s.MaxConns())
		totals[role] = v
	}
	c.mu.Unlock()
	for role, values := range totals {
		for i, state := range []string{"acquired", "idle", "constructing", "maximum"} {
			ch <- prometheus.MustNewConstMetric(c.connections, prometheus.GaugeValue, values[i], role, state)
		}
	}
}
func (c *postgresPoolCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.connections }
