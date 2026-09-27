package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestPublicTraceTrustBoundary(t *testing.T) {
	remote := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled, Remote: true})
	b, err := baggage.Parse("secret=private-token")
	if err != nil {
		t.Fatal(err)
	}
	ctx := trace.ContextWithRemoteSpanContext(baggage.ContextWithBaggage(t.Context(), b), remote)
	ctx = PublicContext(ctx)
	if trace.SpanContextFromContext(ctx).IsValid() || baggage.FromContext(ctx).Len() != 0 {
		t.Fatal("public context retained untrusted propagation")
	}

	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(newSampler(Config{Sampler: SamplerParentBasedTraceIDRatio, SamplerRatio: 0})))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	_, span := provider.Tracer("test").Start(ctx, "public")
	if span.IsRecording() || span.SpanContext().TraceID() == remote.TraceID() {
		t.Fatal("remote caller forced sampling or trace ID")
	}
	span.End()

	headers := make(http.Header)
	headers.Set("baggage", "secret=private-token")
	InjectTrusted(trace.ContextWithSpanContext(t.Context(), remote), headers)
	trusted := ExtractTrusted(t.Context(), headers)
	if trace.SpanContextFromContext(trusted).TraceID() != remote.TraceID() {
		t.Fatal("internal correlation lost")
	}
	if baggage.FromContext(trusted).Len() != 0 || headers.Get("baggage") != "" {
		t.Fatal("baggage propagated")
	}
}

func TestDependencySpanRedactsErrors(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	ctx, parent := provider.Tracer("test").Start(t.Context(), "request")
	_, finish := StartDependency(ctx, "postgres", "private-db-host", "select")
	finish(errors.New("SELECT secret FROM private WHERE token='credential'"))
	parent.End()
	spans := exporter.GetSpans()
	if len(spans) != 2 || spans[0].Parent.TraceID() != spans[1].SpanContext.TraceID() {
		t.Fatalf("missing dependency correlation: %v", spans)
	}
	encoded, err := json.Marshal(spans)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-db-host", "credential", "SELECT secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("span contains %q", secret)
		}
	}
}

type stalledExporter struct {
	entered chan struct{}
	release chan struct{}
}

func (e *stalledExporter) ExportSpans(ctx context.Context, _ []sdktrace.ReadOnlySpan) error {
	select {
	case e.entered <- struct{}{}:
	default:
	}
	select {
	case <-e.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*stalledExporter) Shutdown(context.Context) error { return nil }

func TestFullTraceQueueDoesNotBlockRequests(t *testing.T) {
	e := &stalledExporter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	p := sdktrace.NewTracerProvider(sdktrace.WithBatcher(e, sdktrace.WithMaxQueueSize(2), sdktrace.WithMaxExportBatchSize(1), sdktrace.WithBatchTimeout(time.Millisecond), sdktrace.WithExportTimeout(time.Second)))
	t.Cleanup(func() { close(e.release); _ = p.Shutdown(context.Background()) })
	_, first := p.Tracer("test").Start(t.Context(), "first")
	first.End()
	select {
	case <-e.entered:
	case <-time.After(time.Second):
		t.Fatal("exporter did not start")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			_, span := p.Tracer("test").Start(context.Background(), "request")
			span.End()
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("full exporter queue blocked request completion")
	}
}

func BenchmarkDependencyObservation(b *testing.B) {
	ctx := context.Background()
	for b.Loop() {
		_, finish := StartDependency(ctx, "postgres", "application", "select")
		finish(nil)
	}
}
