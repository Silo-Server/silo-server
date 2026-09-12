package apiv2

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Observability for the v2 listener. Every v2 request is counted, timed and
// logged once, here, with labels that are stable across releases and bounded
// in cardinality: the operation ID (never the raw path), the method folded
// into the fixed standard set, the status class, the problem type
// identifier, the credential class, and a fixed client family. The v1 request logger and metrics middleware skip the /api/v2/
// subtree so a v2 request is not recorded twice with two label vocabularies.
//
// Nothing here logs a URL, a query string, a header value other than the
// clamped client identity, or a body. The request ID is the correlation
// handle: it is the same value in the X-Request-ID header, the Problem
// Details `instance`, and the request log line.

// Metric label names; the log attributes use the same spelling.
const (
	labelAPIMajor    = "api_major"
	labelOperationID = "operation_id"
	labelMethod      = "method"
	labelStatusClass = "status_class"
	labelErrorCode   = "error_code"
	labelAuthClass   = "auth_class"
	labelClient      = "client"
)

// Metric label values that are not derived from the request.
const (
	// labelNone is the operation_id, error_code or client label when the
	// request produced none (an unmatched path, a success, a nameless client).
	labelNone = "none"
	// authClassPublic labels a public operation, which runs no credential gate.
	authClassPublic = "public"
	// authClassAnonymous labels a gated operation that established no identity
	// (no credential, or one the gate refused).
	authClassAnonymous = "anonymous"
	// labelOther replaces unknown client names and methods outside the standard set.
	labelOther          = "other"
	metricClientWeb     = "web"
	metricClientApple   = "apple"
	metricClientAndroid = "android"
	// maxClientNameLen and maxClientVersionLen clamp the X-Silo-Client and
	// X-Silo-Client-Version values before they reach a label or a log line.
	maxClientNameLen    = 64
	maxClientVersionLen = 32
)

var (
	requestInFlight = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "silo_apiv2_in_flight", Help: "Active native API v2 operations, including response streaming."}, []string{labelOperationID})
	requestsTotal   = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamapp_apiv2_requests_total",
		Help: "Native API v2 requests by operation, status class, problem type, credential class and client.",
	}, []string{labelAPIMajor, labelOperationID, labelMethod, labelStatusClass, labelErrorCode, labelAuthClass, labelClient})

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "streamapp_apiv2_request_duration_seconds",
		Help:    "Native API v2 request duration in seconds by operation.",
		Buckets: prometheus.DefBuckets,
	}, []string{labelAPIMajor, labelOperationID, labelMethod})

	validationFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamapp_apiv2_validation_failures_total",
		Help: "Native API v2 requests answered with the validation_failed problem, by operation.",
	}, []string{labelOperationID})

	// v1TombstoneRequests counts requests answered by the v1 retirement
	// tombstone. It is registered now so dashboards and release gates can
	// reference it before the tombstone exists; RecordV1Tombstone increments
	// it once the listener serves one.
	v1TombstoneRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamapp_apiv1_tombstone_requests_total",
		Help: "Requests answered by the API v1 retirement tombstone, by method.",
	}, []string{labelMethod})
)

// RecordV1Tombstone counts one request answered by the v1 tombstone. The
// method label is bounded the same way as the v2 request vectors, so an
// unauthenticated caller cannot mint a series per invented method token.
func RecordV1Tombstone(method string) { v1TombstoneRequests.WithLabelValues(methodLabel(method)).Inc() }

// observation is the per-request record the middleware chain fills in as the
// request moves through the router, and the observe middleware reports once
// the response is written.
type observation struct {
	operationID string
	pathPattern string
	errorCode   string
	authClass   string
	userID      *int
}

type observationKey struct{}

func observationFrom(ctx context.Context) *observation {
	o, _ := ctx.Value(observationKey{}).(*observation)
	return o
}

// observe is the outermost v2 chi middleware after requestID: it records the
// request once, whatever answered it (an operation, a gate, the 404/405
// fallbacks, or the panic recovery inside bufferResponse).
func observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx, span := otel.Tracer("silo/apiv2").Start(telemetry.PublicContext(r.Context()), "api.v2.unmatched", trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer))
		if span.IsRecording() {
			span.SetAttributes(attribute.String("http.request.method", methodLabel(r.Method)))
		}
		defer span.End()
		r = r.WithContext(ctx)
		o := &observation{operationID: labelNone, errorCode: labelNone, authClass: authClassAnonymous}
		r = r.WithContext(context.WithValue(r.Context(), observationKey{}, o))
		sw := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if span.IsRecording() {
			span.SetAttributes(attribute.String("operation", o.operationID), attribute.String("error.type", o.errorCode))
			if sw.status != 0 {
				span.SetAttributes(attribute.Int("http.response.status_code", sw.status))
			} else if sw.hijacked {
				span.SetAttributes(attribute.String("http.response.outcome", "hijacked"))
			} else {
				span.SetAttributes(attribute.String("http.response.outcome", "abandoned"))
			}
			if sw.status >= 500 {
				span.SetStatus(codes.Error, "server_error")
			}
		}
		report(r, o, sw.status, sw.hijacked, time.Since(start))
	})
}

// statusRecorder captures structured and raw response statuses. Unwrap lets
// http.ResponseController reach streaming capabilities on the original writer.
type statusRecorder struct {
	http.ResponseWriter
	status   int
	hijacked bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Hijack preserves the legacy http.Hijacker interface used by Gorilla's
// WebSocket upgrader. ResponseController also traverses Unwrap-only writers.
// A hijack alone does not prove which bytes the handler sent on the connection,
// so it must not fabricate an observed HTTP status.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(s.ResponseWriter).Hijack()
	if err == nil {
		s.hijacked = true
	}
	return conn, rw, err
}

func report(r *http.Request, o *observation, status int, hijacked bool, elapsed time.Duration) {
	name, version := clientIdentity(r)
	major := strconv.Itoa(APIMajor)
	method := methodLabel(r.Method)
	class := statusClass(status)
	if status == 0 && hijacked {
		class = "hijacked"
	}
	requestsTotal.WithLabelValues(major, o.operationID, method, class, o.errorCode, o.authClass, clientLabel(name)).Inc()
	requestDuration.WithLabelValues(major, o.operationID, method).Observe(elapsed.Seconds())
	if o.errorCode == TypeValidationFailed.ID {
		validationFailures.WithLabelValues(o.operationID).Inc()
	}

	attrs := []any{
		"component", "apiv2",
		"request_id", requestIDFrom(r.Context()),
		labelAPIMajor, APIMajor,
		labelOperationID, o.operationID,
		labelMethod, method,
		"path_pattern", o.pathPattern,
		"status", status,
		labelStatusClass, class,
		labelErrorCode, o.errorCode,
		labelAuthClass, o.authClass,
		"duration_ms", elapsed.Milliseconds(),
		"client_ip", clientip.FromContext(r.Context()),
	}
	if name != "" {
		attrs = append(attrs, "client_name", name)
	}
	if version != "" {
		attrs = append(attrs, "client_version", version)
	}
	if o.userID != nil {
		attrs = append(attrs, "user_id", *o.userID)
	}
	slog.InfoContext(r.Context(), "apiv2 request", attrs...)
}

// methodLabel folds the request method into a bounded label. The observe
// middleware runs ahead of routing, so it also records the 404 and 405
// fallbacks, and chi routes any syntactically valid method token to the
// latter: an unauthenticated caller could otherwise mint a fresh series per
// invented method. Only the standard methods keep their name; the rest are
// labelOther, in the metric and in the log line alike.
func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	}
	return labelOther
}

// statusClass buckets a status for the metric label. A response that was
// never written (the client left before the buffered body flushed) is
// "abandoned" rather than a fabricated code.
func statusClass(status int) string {
	if status == 0 {
		return "abandoned"
	}
	return strconv.Itoa(status/100) + "xx"
}

// clientIdentity reads the client's self-reported name and version, clamped
// to a printable bounded string. The values are opaque: trimmed, length
// limited, control characters dropped, never parsed or validated.
func clientIdentity(r *http.Request) (name, version string) {
	return clampLabel(r.Header.Get("X-Silo-Client"), maxClientNameLen),
		clampLabel(r.Header.Get("X-Silo-Client-Version"), maxClientVersionLen)
}

func clampLabel(v string, limit int) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(v))
	if len(v) > limit {
		v = v[:limit]
	}
	return v
}

// clientLabel maps only recognized first-party product names into fixed families.
// Arbitrary self-reported names cannot create metric series or store private text
// in Prometheus. Logs retain the existing clamped client identity for diagnosis.
func clientLabel(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "":
		return labelNone
	case "silo web":
		return metricClientWeb
	case "silo apple", "silo apple tv", "silo ios", "silo tvos", "silo macos", "silo ipados":
		return metricClientApple
	case "silo android", "silo android tv":
		return metricClientAndroid
	default:
		return labelOther
	}
}

// observeOperation is the first Huma middleware: the request matched an
// operation, so the record names it. Nothing after this point can change
// which operation answered.
func observeOperation(ctx huma.Context, next func(huma.Context)) {
	opID := ctx.Operation().OperationID
	trace.SpanFromContext(ctx.Context()).SetName("api.v2." + opID)
	active := requestInFlight.WithLabelValues(opID)
	active.Inc()
	defer active.Dec()
	if o := observationFrom(ctx.Context()); o != nil {
		op := ctx.Operation()
		o.operationID = op.OperationID
		o.pathPattern = op.Path
		if class, _ := op.Metadata[metaClass].(Class); class == ClassPublic {
			o.authClass = authClassPublic
		}
	}
	next(ctx)
}

// observeIdentity runs after classGate admitted the request: the credential
// class and the account come from the claims the auth gate stored.
func observeIdentity(ctx huma.Context, next func(huma.Context)) {
	if o := observationFrom(ctx.Context()); o != nil {
		if claims := apimw.GetClaims(ctx.Context()); claims != nil {
			o.authClass = authClassFor(claims)
			id := claims.UserID
			o.userID = &id
		}
	}
	next(ctx)
}

// authClassFor names the credential kind without echoing anything from it.
func authClassFor(claims *auth.Claims) string {
	switch claims.TokenType {
	case auth.TokenTypeAPIKey:
		return "api_key"
	case auth.TokenTypePluginAccess:
		return "plugin"
	case auth.TokenTypeAccess:
		return "session"
	default:
		return labelOther
	}
}

// noteProblem records the problem type identifier for the metric and log
// labels. The identifier is the catalog's, never client input.
func noteProblem(ctx context.Context, p *Problem) {
	if o := observationFrom(ctx); o != nil && p != nil {
		o.errorCode = strings.TrimPrefix(p.Type, ProblemTypeOrigin)
	}
}
