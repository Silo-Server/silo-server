package database

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

func BenchmarkPostgresTracerQuery(b *testing.B) {
	b.Setenv("SILO_QUERY_ATTRIBUTION_ENABLED", "true")
	if !queryattr.Initialize() {
		b.Fatal("attribution disabled, want enabled benchmark")
	}
	for _, scenario := range []struct {
		name      string
		attribute bool
		sampler   sdktrace.Sampler
	}{
		{"coarse_only", false, sdktrace.NeverSample()},
		{"unsampled", true, sdktrace.NeverSample()},
		{"sampled", true, sdktrace.AlwaysSample()},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(scenario.sampler), sdktrace.WithSyncer(discardPostgresSpanExporter{}))
			previous := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			b.Cleanup(func() {
				otel.SetTracerProvider(previous)
				if err := provider.Shutdown(context.Background()); err != nil {
					b.Errorf("shutdown tracer provider: %v", err)
				}
			})
			tracer := postgresTracer{role: "checks", attribute: scenario.attribute}
			ctx := context.Background()
			data := pgx.TraceQueryStartData{SQL: "SELECT $1", Args: []any{1}}
			b.ReportAllocs()
			for b.Loop() {
				query := tracer.TraceQueryStart(ctx, nil, data)
				tracer.TraceQueryEnd(query, nil, pgx.TraceQueryEndData{})
			}
		})
	}
}

type discardPostgresSpanExporter struct{}

func (discardPostgresSpanExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return nil
}
func (discardPostgresSpanExporter) Shutdown(context.Context) error { return nil }
