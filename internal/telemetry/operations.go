package telemetry

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var dependencyOperations = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "silo_dependency_operations_total", Help: "Dependency operations completed, including cancellation and timeout, by bounded category.",
}, []string{"dependency", "role", "operation", "outcome"})
var dependencyDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name: "silo_dependency_duration_seconds", Help: "Dependency operation duration including client waits and retries; streamed bodies have separate byte accounting.",
	Buckets: []float64{.001, .005, .01, .05, .1, .5, 1, 5, 15, 60},
}, []string{"dependency", "role", "operation"})

// Role bounds configured pool identities. Never pass endpoints or user identities.
func Role(role string) string {
	switch role {
	case "application", "bootstrap", "api", "events", "activity", "tasks", "worker", "checks", "metadata", "operational", "userstore":
		return role
	default:
		return "other"
	}
}

// Outcome deliberately never includes the error text: dependency errors routinely
// contain SQL, Redis keys, storage paths, credentials or signed URLs.
func Outcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "error"
	}
}

// StartDependency requires an operation selected from a fixed vocabulary by the
// owning client. It never inspects request values or serializes errors.
func StartDependency(ctx context.Context, dependency, role, operation string) (context.Context, func(error)) {
	role = Role(role)
	start := time.Now()
	ctx, span := otel.Tracer("silo/dependencies").Start(ctx, dependency+"."+operation,
		trace.WithSpanKind(trace.SpanKindClient))
	if span.IsRecording() {
		span.SetAttributes(attribute.String("dependency", dependency), attribute.String("role", role), attribute.String("operation", operation))
	}
	return ctx, func(err error) {
		outcome := Outcome(err)
		dependencyOperations.WithLabelValues(dependency, role, operation, outcome).Inc()
		dependencyDuration.WithLabelValues(dependency, role, operation).Observe(time.Since(start).Seconds())
		if err != nil {
			span.SetStatus(codes.Error, outcome)
		}
		span.End()
	}
}

// PublicContext removes caller-controlled trace and baggage state. Public API
// operations start new traces, so chosen trace IDs and sampling flags cannot
// force sampling. Cancellation, auth and request-local values are preserved.
func PublicContext(ctx context.Context) context.Context {
	ctx = baggage.ContextWithBaggage(ctx, baggage.Baggage{})
	return trace.ContextWithSpanContext(ctx, trace.SpanContext{})
}

// ExtractTrusted accepts trace context only after the caller has authenticated
// an internal peer. Baggage is never extracted. Do not use for public ingress.
func ExtractTrusted(ctx context.Context, header http.Header) context.Context {
	return propagation.TraceContext{}.Extract(baggage.ContextWithBaggage(ctx, baggage.Baggage{}), propagation.HeaderCarrier(header))
}

// InjectTrusted copies only trace context into an authenticated internal request.
// The caller must restrict the destination and avoid forwarding these headers
// through redirects to third parties.
func InjectTrusted(ctx context.Context, header http.Header) {
	header.Del("baggage")
	header.Del("traceparent")
	header.Del("tracestate")
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(header))
}
