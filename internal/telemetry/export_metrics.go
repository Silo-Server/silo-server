package telemetry

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var exportRecords = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "silo_otel_export_records_total", Help: "Records handed to the OTLP exporter by signal and final export outcome. Failed batches are lost after exporter retries.",
}, []string{"signal", "outcome"})
var finishedSpans = promauto.NewCounter(prometheus.CounterOpts{
	Name: "silo_otel_finished_spans_total", Help: "Sampled spans ended in the SDK. Difference from exported trace records includes queued, exporting and dropped spans; inspect after a drain to assess loss.",
})

// The SDK's queue is bounded and nonblocking but exposes no Prometheus drop
// counter. Counting sampled completions and batch outcomes makes losses visible
// without adding a second metrics SDK or an unbounded queue.
type spanCompletionCounter struct{}

func (spanCompletionCounter) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (spanCompletionCounter) OnEnd(span sdktrace.ReadOnlySpan) {
	if span.SpanContext().IsSampled() {
		finishedSpans.Inc()
	}
}
func (spanCompletionCounter) Shutdown(context.Context) error   { return nil }
func (spanCompletionCounter) ForceFlush(context.Context) error { return nil }

type measuredTraceExporter struct{ sdktrace.SpanExporter }

func (e measuredTraceExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.SpanExporter.ExportSpans(ctx, spans)
	exportRecords.WithLabelValues("traces", Outcome(err)).Add(float64(len(spans)))
	return err
}

type measuredLogExporter struct{ sdklog.Exporter }

func (e measuredLogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	err := e.Exporter.Export(ctx, records)
	exportRecords.WithLabelValues("logs", Outcome(err)).Add(float64(len(records)))
	return err
}
