package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestAPITracePrivacyAndSingleObservation(t *testing.T) {
	captureLogs(t)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	h := newTestHandler(t, parityDeps(false))
	remote := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, Remote: true})
	r := httptest.NewRequest(http.MethodGet, "/api/v2/system/info?private=credential", nil)
	r = r.WithContext(trace.ContextWithRemoteSpanContext(r.Context(), remote))
	r.Header.Set("Traceparent", "00-01000000000000000000000000000000-0200000000000000-01")
	r.Header.Set("Baggage", "private=credential")
	r.Header.Set("Authorization", "Bearer credential")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("one request produced %d spans", len(spans))
	}
	if spans[0].Name != "api.v2.getSystemInfo" || spans[0].Parent.IsValid() || spans[0].SpanContext.TraceID() == remote.TraceID() {
		t.Fatalf("public trace used caller identity: %v", spans)
	}
	encoded, err := json.Marshal(spans)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"credential", "private=", "/api/v2/system/info?"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("trace contains %q", secret)
		}
	}
	if testutil.ToFloat64(requestInFlight.WithLabelValues("getSystemInfo")) != 0 {
		t.Fatal("in-flight operation leaked after response")
	}

	// A caller-supplied sampled parent must not override the server sampler.
	dropping := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())))
	otel.SetTracerProvider(dropping)
	t.Cleanup(func() { _ = dropping.Shutdown(context.Background()) })
	h.ServeHTTP(httptest.NewRecorder(), r)
	if len(exporter.GetSpans()) != 1 {
		t.Fatal("untrusted incoming flag forced sampling")
	}
}

func TestAPITraceOmitsUnobservedHTTPStatus(t *testing.T) {
	captureLogs(t)
	for _, outcome := range []string{"abandoned", "hijacked"} {
		t.Run(outcome, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			previous := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			defer func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) }()
			completed := make(chan struct{})
			handler := observe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if outcome == "hijacked" {
					conn, _, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				}
			}))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r); close(completed) }))
			defer server.Close()
			resp, _ := http.Get(server.URL)
			if resp != nil {
				_ = resp.Body.Close()
			}
			select {
			case <-completed:
			case <-time.After(time.Second):
				t.Fatal("observation did not complete")
			}
			spans := exporter.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("got %d spans", len(spans))
			}
			found := false
			for _, attr := range spans[0].Attributes {
				if attr.Key == "http.response.status_code" {
					t.Fatal("unobserved status became an HTTP code")
				}
				if attr.Key == "http.response.outcome" && attr.Value.AsString() == outcome {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s outcome", outcome)
			}
		})
	}
}
