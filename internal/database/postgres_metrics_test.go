package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database/queryattr"
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

func TestPostgresTracerAttributesOperations(t *testing.T) {
	enablePostgresAttribution(t)
	exporter := postgresSpanExporter(t)
	tracer := postgresTracer{role: "checks", attribute: true}
	ctx, request := queryattr.StartRequest(t.Context())
	query := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT $1", Args: []any{1}})
	tracer.TraceQueryEnd(query, nil, pgx.TraceQueryEndData{})
	batch := &pgx.Batch{}
	batch.Queue("SELECT 1")
	b := tracer.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: batch})
	tracer.TraceBatchEnd(b, nil, pgx.TraceBatchEndData{})
	c := tracer.TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{})
	tracer.TraceCopyFromEnd(c, nil, pgx.TraceCopyFromEndData{})
	a := tracer.TraceAcquireStart(ctx, nil, pgxpool.TraceAcquireStartData{})
	tracer.TraceAcquireEnd(a, nil, pgxpool.TraceAcquireEndData{Err: context.Canceled})
	connect := tracer.TraceConnectStart(ctx, pgx.TraceConnectStartData{})
	tracer.TraceConnectEnd(connect, pgx.TraceConnectEndData{})
	attrs := postgresRequestAttrs(request)
	if got := attrs["db_queries"]; got != uint64(3) {
		t.Errorf("db_queries = %v, want 3", got)
	}
	if got, _ := attrs["db_acquire_wait_ns"].(uint64); got == 0 {
		t.Error("db_acquire_wait_ns = 0, want positive failed-acquire wait")
	}
	spans := exporter.GetSpans()
	if len(spans) != 5 {
		t.Fatalf("ended spans = %d, want 5", len(spans))
	}
	for _, span := range spans {
		attributed := span.Name != "postgres.acquire" && span.Name != "postgres.connect"
		assertPostgresSpanAttribution(t, span, attributed)
	}
}

func TestPostgresTracerDuplicateBatchEndCountsOnce(t *testing.T) {
	enablePostgresAttribution(t)
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("attribute=%t", enabled), func(t *testing.T) {
			exporter := postgresSpanExporter(t)
			logs := capturePostgresLogs(t)
			flushPostgresSummary()
			logs.Reset()
			counter := postgresDependencyCounter(t, "batch", "error")
			before := testutil.ToFloat64(counter)
			ctx, request := queryattr.StartRequest(t.Context())
			tracer := postgresTracer{role: "checks", attribute: enabled}
			batch := &pgx.Batch{}
			batch.Queue("SELECT 1")
			b := tracer.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: batch})
			end := pgx.TraceBatchEndData{Err: errors.New("batch failed")}
			tracer.TraceBatchEnd(b, nil, end)
			tracer.TraceBatchEnd(b, nil, end)
			if delta := testutil.ToFloat64(counter) - before; delta != 1 {
				t.Errorf("coarse batch ends = %g, want 1", delta)
			}
			spans := exporter.GetSpans()
			if len(spans) != 1 {
				t.Errorf("ended spans = %d, want 1", len(spans))
			} else {
				assertPostgresSpanAttribution(t, spans[0], enabled)
			}
			want := uint64(0)
			if enabled {
				want = 1
			}
			if got, present := postgresRequestAttrs(request)["db_queries"]; enabled {
				if got != want {
					t.Errorf("db_queries = %v, want %d", got, want)
				}
			} else if present {
				t.Errorf("db_queries = %v, want field absent", got)
			}
			flushPostgresSummary()
			if calls := postgresSummaryRowCalls(t, logs.Bytes(), "batch"); calls != want {
				t.Errorf("unknown/batch row calls = %d, want %d", calls, want)
			}
		})
	}
}

func TestPostgresTracerSurvivesPanickingLogSink(t *testing.T) {
	enablePostgresAttribution(t)
	exporter := postgresSpanExporter(t)
	logs := capturePostgresLogs(t)
	flushPostgresSummary()
	logs.Reset()
	counter := postgresDependencyCounter(t, "select", "success")
	before := testutil.ToFloat64(counter)
	tracer := postgresTracer{role: "checks", attribute: true}
	ctx, request := queryattr.StartRequest(t.Context())
	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT $1", Args: make([]any, 1001)})
	logger := slog.Default()
	handler := &postgresPanicHandler{}
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(logger)
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	slog.SetDefault(logger)
	if handler.calls != 1 {
		t.Errorf("panicking sink calls = %d, want 1", handler.calls)
	}
	if delta := testutil.ToFloat64(counter) - before; delta != 1 {
		t.Errorf("coarse query ends = %g, want 1", delta)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	assertPostgresSpanAttribution(t, spans[0], true)
	if got := postgresRequestAttrs(request)["db_queries"]; got != uint64(1) {
		t.Errorf("db_queries = %v, want 1", got)
	}
	flushPostgresSummary()
	if calls := postgresSummaryRowCalls(t, logs.Bytes(), ""); calls != 1 {
		t.Errorf("unknown query row calls = %d, want 1", calls)
	}
}

func postgresDependencyCounter(t *testing.T, operation, outcome string) prometheus.Counter {
	t.Helper()
	probe := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "silo_dependency_operations_total",
		Help: "Dependency operations completed, including cancellation and timeout, by bounded category.",
	}, []string{"dependency", "role", "operation", "outcome"})
	var duplicate prometheus.AlreadyRegisteredError
	if err := prometheus.DefaultRegisterer.Register(probe); !errors.As(err, &duplicate) {
		if err == nil {
			prometheus.DefaultRegisterer.Unregister(probe)
		}
		t.Fatalf("existing dependency collector lookup = %v, want AlreadyRegisteredError", err)
	}
	counter, ok := duplicate.ExistingCollector.(*prometheus.CounterVec)
	if !ok {
		t.Fatalf("dependency collector = %T, want *prometheus.CounterVec", duplicate.ExistingCollector)
	}
	return counter.WithLabelValues("postgres", "checks", operation, outcome)
}

func postgresRequestAttrs(request *queryattr.Request) map[string]any {
	attrs := make(map[string]any)
	for _, value := range request.LogAttrs() {
		attr := value.(slog.Attr)
		attrs[attr.Key] = attr.Value.Any()
	}
	return attrs
}

func assertPostgresSpanAttribution(t *testing.T, span tracetest.SpanStub, want bool) {
	t.Helper()
	attrs := make(map[string]string)
	for _, attr := range span.Attributes {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}
	for _, key := range []string{"silo.db.site", "silo.db.shape"} {
		value, present := attrs[key]
		if present != want || (want && value == "") {
			t.Errorf("span %s %s = %q (present=%t), want attributed=%t", span.Name, key, value, present, want)
		}
	}
	if want && attrs["silo.db.site"] != "unknown" {
		t.Errorf("span %s site = %q, want unknown for database frames", span.Name, attrs["silo.db.site"])
	}
	if want && (span.Name == "postgres.batch" || span.Name == "postgres.copy") {
		if shape := strings.TrimPrefix(span.Name, "postgres."); attrs["silo.db.shape"] != shape {
			t.Errorf("span %s shape = %q, want %q", span.Name, attrs["silo.db.shape"], shape)
		}
	}
}

type postgresPanicHandler struct{ calls int }

func (*postgresPanicHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *postgresPanicHandler) Handle(context.Context, slog.Record) error {
	h.calls++
	panic("log sink failed")
}
func (h *postgresPanicHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *postgresPanicHandler) WithGroup(string) slog.Handler      { return h }
