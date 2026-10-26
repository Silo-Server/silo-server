package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

func TestQueryAttributionPrivacy(t *testing.T) {
	enablePostgresAttribution(t)
	exporter := postgresSpanExporter(t)
	logs := capturePostgresLogs(t)
	registry := prometheus.NewRegistry()
	queryattr.RegisterMetrics(registry)
	before := postgresSiteMetrics(t, registry)
	tracer := postgresTracer{role: "checks", attribute: true}
	ctx, request := queryattr.StartRequest(t.Context())
	start := time.Now()

	query := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL:  `SELECT 'PRIVATE_LITERAL', $$PRIVATE_DOLLAR$$, $1 FROM "PRIVATE_TABLE" /* PRIVATE_COMMENT */`,
		Args: []any{"PRIVATE_ARGUMENT"},
	})
	tracer.TraceQueryEnd(query, nil, pgx.TraceQueryEndData{Err: errors.New("PRIVATE_ERROR")})
	args := make([]string, 1001)
	for i := range args {
		args[i] = "PRIVATE_ARRAY_ELEMENT"
	}
	large := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT $1", Args: []any{args}})
	pgErr := &pgconn.PgError{Code: "23505", Message: "PRIVATE_MESSAGE", Detail: "PRIVATE_DETAIL", Hint: "PRIVATE_HINT"}
	tracer.TraceQueryEnd(large, nil, pgx.TraceQueryEndData{Err: pgErr})
	batch := &pgx.Batch{}
	batch.Queue("SELECT 'PRIVATE_BATCH_SQL', $1", "PRIVATE_BATCH_ARGUMENT")
	b := tracer.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: batch})
	tracer.TraceBatchQuery(b, nil, pgx.TraceBatchQueryData{SQL: batch.QueuedQueries[0].SQL, Args: batch.QueuedQueries[0].Arguments, Err: pgErr})
	tracer.TraceBatchEnd(b, nil, pgx.TraceBatchEndData{})
	c := tracer.TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{
		TableName: pgx.Identifier{"PRIVATE_COPY_TABLE"}, ColumnNames: []string{"PRIVATE_COPY_COLUMN"},
	})
	tracer.TraceCopyFromEnd(c, nil, pgx.TraceCopyFromEndData{})
	elapsed := time.Since(start).Seconds()
	after := postgresSiteMetrics(t, registry)
	if len(after) != 2 {
		t.Errorf("attribution counter families = %d, want both queries and seconds", len(after))
	}
	if delta := after["silo_postgres_queries_total"] - before["silo_postgres_queries_total"]; delta != 4 {
		t.Errorf("unknown queries delta = %g, want 4 completed operations", delta)
	}
	if delta := after["silo_postgres_query_seconds_total"] - before["silo_postgres_query_seconds_total"]; delta <= 0 || delta > elapsed {
		t.Errorf("unknown seconds delta = %g, want > 0 and <= elapsed %g", delta, elapsed)
	}

	slog.InfoContext(ctx, "api request", request.LogAttrs()...)
	flushPostgresSummary()
	spans := exporter.GetSpans()
	if len(spans) != 4 {
		t.Errorf("ended spans = %d, want 4", len(spans))
	}
	for _, span := range spans {
		assertPostgresSpanAttribution(t, span, true)
	}
	messages := make(map[string]int)
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode log line: %v", err)
		}
		message, _ := record["msg"].(string)
		messages[message]++
	}
	if messages["api request"] != 1 || messages["slow or large database query"] < 1 || messages["database query summary"] != 1 {
		t.Errorf("log messages = %v, want one request, at least one event, one summary", messages)
	}
	if hasPostgresPrivateSentinel(t, exporter.GetSpans()) {
		t.Error("PRIVATE_ leak in encoded spans (names, attributes, events, or status)")
	}
	for name, gatherer := range map[string]prometheus.Gatherer{"private": registry, "default": prometheus.DefaultGatherer} {
		families, err := gatherer.Gather()
		if err != nil {
			t.Fatalf("gather %s metrics: %v", name, err)
		}
		if hasPostgresPrivateSentinel(t, families) {
			t.Errorf("PRIVATE_ leak in %s metrics", name)
		}
	}
	if hasPostgresPrivateSentinel(t, logs.String()) {
		t.Error("PRIVATE_ leak in emitted logs")
	}

	assertPostgresPrivacyControls(t, ctx, exporter, registry, logs)
}

// The same predicate must reject a real sentinel through each configured sink.
func assertPostgresPrivacyControls(t *testing.T, ctx context.Context, exporter *tracetest.InMemoryExporter, registry *prometheus.Registry, logs *bytes.Buffer) {
	t.Helper()
	exporter.Reset()
	_, control := otel.Tracer("privacy-control").Start(ctx, "PRIVATE_SPAN")
	control.SetAttributes(attribute.String("control", "PRIVATE_ATTRIBUTE"))
	control.End()
	if !hasPostgresPrivateSentinel(t, exporter.GetSpans()) {
		t.Error("privacy predicate accepted a planted span leak")
	}
	leak := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "privacy_control_total", Help: "Privacy detector control."}, []string{"control"})
	registry.MustRegister(leak)
	leak.WithLabelValues("PRIVATE_LABEL").Inc()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if !hasPostgresPrivateSentinel(t, families) {
		t.Error("privacy predicate accepted a planted counter-label leak")
	}
	logs.Reset()
	slog.InfoContext(ctx, "PRIVATE_LOG")
	if !hasPostgresPrivateSentinel(t, logs.String()) {
		t.Error("privacy predicate accepted a planted log leak")
	}
}

func enablePostgresAttribution(t *testing.T) {
	t.Helper()
	t.Setenv("SILO_QUERY_ATTRIBUTION_ENABLED", "true")
	if !queryattr.Initialize() {
		t.Fatal("attribution disabled, want enabled fixture")
	}
}

func postgresSpanExporter(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown tracer provider: %v", err)
		}
	})
	return exporter
}

func capturePostgresLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	logs := new(bytes.Buffer)
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

func flushPostgresSummary() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queryattr.RunSummary(ctx, "test-run", "test-build")
}

func postgresSummaryRowCalls(t *testing.T, data []byte, shape string) uint64 {
	t.Helper()
	var record struct {
		TopCount map[string]struct {
			Site  string `json:"site"`
			Shape string `json:"shape"`
			Calls uint64 `json:"calls"`
		} `json:"top_count"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	var calls uint64
	for _, row := range record.TopCount {
		if row.Site == "unknown" && (shape == "" || row.Shape == shape) {
			calls += row.Calls
		}
	}
	return calls
}

func postgresSiteMetrics(t *testing.T, gatherer prometheus.Gatherer) map[string]float64 {
	t.Helper()
	families, err := gatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]float64)
	for _, family := range families {
		name := family.GetName()
		if name != "silo_postgres_queries_total" && name != "silo_postgres_query_seconds_total" {
			continue
		}
		if family.GetType() != dto.MetricType_COUNTER {
			t.Errorf("%s type = %v, want COUNTER", name, family.GetType())
		}
		values[name] = 0
		for _, metric := range family.GetMetric() {
			labels := metric.GetLabel()
			if len(labels) != 1 || labels[0].GetName() != "site" {
				t.Errorf("%s labels = %v, want exactly site", name, labels)
				continue
			}
			if labels[0].GetValue() == "unknown" {
				values[name] = metric.GetCounter().GetValue()
			}
		}
	}
	// Empty CounterVec families are absent before the first operation.
	if len(families) > 0 {
		for _, name := range []string{"silo_postgres_queries_total", "silo_postgres_query_seconds_total"} {
			if _, present := values[name]; !present {
				t.Errorf("missing counter family %s", name)
			}
		}
	}
	return values
}

func hasPostgresPrivateSentinel(t *testing.T, value any) bool {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Contains(encoded, []byte("PRIVATE_"))
}
