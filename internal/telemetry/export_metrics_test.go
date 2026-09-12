package telemetry

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type failedTraceExporter struct{}

func (failedTraceExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("private collector address")
}
func (failedTraceExporter) Shutdown(context.Context) error { return nil }

func TestExportFailureIsCountedWithoutErrorText(t *testing.T) {
	beforeFinished := testutil.ToFloat64(finishedSpans)
	beforeFailed := testutil.ToFloat64(exportRecords.WithLabelValues("traces", "error"))
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanCompletionCounter{}), sdktrace.WithSyncer(measuredTraceExporter{failedTraceExporter{}}))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	_, span := provider.Tracer("test").Start(t.Context(), "test")
	span.End()
	if testutil.ToFloat64(finishedSpans) != beforeFinished+1 || testutil.ToFloat64(exportRecords.WithLabelValues("traces", "error")) != beforeFailed+1 {
		t.Fatal("export loss was not visible")
	}
}
