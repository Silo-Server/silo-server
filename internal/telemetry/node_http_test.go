package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestTrustedNodeTraceAndRedirectBoundary(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(old); _ = provider.Shutdown(context.Background()) })
	ctx, root := provider.Tracer("test").Start(t.Context(), "request")
	var called atomic.Int32
	server := httptest.NewServer(TrustedHTTPHandler("worker", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		if trace.SpanContextFromContext(r.Context()).TraceID() != root.SpanContext().TraceID() {
			t.Error("worker lost trace parent")
		}
		if r.Header.Get("baggage") != "" {
			t.Error("baggage crossed boundary")
		}
		_, _ = io.WriteString(w, "fixture")
	})))
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/secret-session?token=private", nil)
	req.Header.Set("baggage", "secret=private")
	resp, err := DoTrustedNode(nil, req, "capabilities")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "fixture" || called.Load() != 1 {
		t.Fatal("request behavior changed")
	}
	root.End()
	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want API root, client and server", len(spans))
	}
	for _, span := range spans {
		if span.SpanContext.TraceID() != root.SpanContext().TraceID() {
			t.Fatal("trace split at node boundary")
		}
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, server.URL, http.StatusFound) }))
	defer redirect.Close()
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, redirect.URL, nil)
	req.Header.Set("Authorization", "Bearer private")
	resp, err = DoTrustedNode(nil, req, "capabilities")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || called.Load() != 1 {
		t.Fatal("redirect forwarded private context or credentials")
	}
}

func TestNoSpanRemovesStalePropagation(t *testing.T) {
	headers := http.Header{"Traceparent": {"untrusted"}, "Tracestate": {"untrusted"}, "Baggage": {"secret=private"}}
	InjectTrusted(context.Background(), headers)
	if len(headers) != 0 {
		t.Fatalf("retained stale context: %v", headers)
	}
}
