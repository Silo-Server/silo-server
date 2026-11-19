package database

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

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
