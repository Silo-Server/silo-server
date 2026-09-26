package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPostgresOperationPrivacy(t *testing.T) {
	for _, query := range []string{"/* token=private */ SELECT 1", "malicious-secret", "SELECT\nprivate", " SELECTED secret"} {
		if got := queryOperation(query); got != "other" {
			t.Fatalf("unbounded query label %q", got)
		}
	}
	if queryOperation("  SELECT private FROM passwords") != "select" {
		t.Fatal("select category missing")
	}
	tracer := postgresTracer{role: "checks"}
	ctx := tracer.TraceQueryStart(t.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT secret", Args: []any{"credential"}})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: context.Canceled})
}

func TestPostgresPoolSaturationMetrics(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL required for pool saturation")
	}
	pool, err := NewPoolForRole(t.Context(), config.DatabaseConfig{URL: dsn, MaxConnections: 1}, "checks")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ClosePool(pool) })
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if _, err = pool.Acquire(ctx); err == nil {
		t.Fatal("saturated pool acquire succeeded")
	}
	if pool.Stat().CanceledAcquireCount() != 1 {
		t.Fatal("cancellation was not observed")
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(postgresPools)
	if err := testutil.GatherAndCompare(registry, strings.NewReader(`
# HELP silo_postgres_pool_connections Connections in live PostgreSQL pools by role and state; maximum is configured capacity.
# TYPE silo_postgres_pool_connections gauge
silo_postgres_pool_connections{role="checks",state="acquired"} 1
silo_postgres_pool_connections{role="checks",state="constructing"} 0
silo_postgres_pool_connections{role="checks",state="idle"} 0
silo_postgres_pool_connections{role="checks",state="maximum"} 1
`), "silo_postgres_pool_connections"); err != nil {
		t.Fatal(err)
	}
	conn.Release()
	if err = pool.Ping(t.Context()); err != nil {
		t.Fatal("pool did not recover:", err)
	}
	ClosePool(pool)
	if n := testutil.CollectAndCount(postgresPools); n != 0 {
		t.Fatalf("closed pool retained %d gauges", n)
	}
}
