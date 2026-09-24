package jellycompat

import (
	"bytes"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/telemetry"
)

// Observability for the Jellyfin-compatible listener. Every request is counted
// once and opens one server span, whatever answered it: a route, the 404/405
// fallbacks, the CORS preflight, or a middleware that refused it. Labels stay
// bounded: the chi route template (never the raw path or an ID), the method
// folded into the standard set, the status class, and a fixed client family.
// The request log line in logging.go remains the per-request detail record.

// Metric label names.
const (
	compatLabelRoute       = "route"
	compatLabelMethod      = "method"
	compatLabelStatusClass = "status_class"
	compatLabelClient      = "client"
)

// Metric label values that are not a route template, method or client family.
const (
	// compatRouteUnmatched is the route label of a request no registered route
	// matched: a 404, a 405, or one a middleware answered before routing.
	compatRouteUnmatched = "unmatched"
	compatLabelOther     = "other"
	compatClientNone     = "none"
	compatStatusHijacked = "hijacked"
)

// compatStatusClasses labels a status by its first digit.
var compatStatusClasses = [...]string{"1xx", "2xx", "3xx", "4xx", "5xx"} //nolint:goconst // Label vocabulary.

var (
	compatRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "silo_jellycompat_requests_total",
		Help: "Jellyfin-compatible API requests by route template, method, status class and client family.",
	}, []string{compatLabelRoute, compatLabelMethod, compatLabelStatusClass, compatLabelClient})

	// The route histogram omits status class and client: each would multiply
	// every route's buckets, and an unauthenticated caller can choose both.
	compatRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "silo_jellycompat_request_duration_seconds",
		Help:    "Jellyfin-compatible API request duration in seconds by route template and method. Media body and socket routes are counted but not timed.",
		Buckets: prometheus.DefBuckets,
	}, []string{compatLabelRoute, compatLabelMethod})

	// The client histogram splits the same timed requests by client family
	// alone: one histogram per family, about 200 series, where a route by
	// client histogram would be about 26,000. Latency for one client on one
	// route is on the trace.
	compatClientRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "silo_jellycompat_client_request_duration_seconds",
		Help:    "Jellyfin-compatible API request duration in seconds by client family, over the same timed routes as silo_jellycompat_request_duration_seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{compatLabelClient})
)

// untimedCompatRoutes are the templates whose response lasts as long as the
// client keeps reading: the playback and transfer media routes (streams, HLS
// segments, subtitles, attachments, downloads, the bitrate test) and the
// session socket. An hour-long direct stream would land in +Inf and swamp the
// percentiles of its series, so these routes are counted but never timed. HLS
// manifests stay timed: they are short documents, and their latency is the
// server's share of playback start.
var untimedCompatRoutes = func() map[string]struct{} {
	routes := map[string]struct{}{"/socket": {}}
	for _, route := range jellycompatMediaRoutes {
		if route.Class != streamtelemetry.ClassManifest {
			routes[route.Pattern] = struct{}{}
		}
	}
	return routes
}()

// compatSpanOptions is built once: boxing the options per request allocates.
var compatSpanOptions = []trace.SpanStartOption{trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer)}

// observeCompatRequest records each request once and opens its server span, so
// the Postgres, Redis and S3 dependency spans the handler starts join one trace
// instead of each becoming a root. It runs ahead of routing and reads the route
// template after the handler returns.
func observeCompatRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// A public listener starts a fresh trace: a caller's traceparent or
		// baggage must not choose the trace ID or force sampling.
		ctx, span := otel.Tracer("silo/jellycompat").Start(telemetry.PublicContext(r.Context()), "jellycompat request", compatSpanOptions...)
		defer span.End()
		sw := &statusResponseWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(ctx))
		elapsed := time.Since(start)

		route := chi.RouteContext(ctx).RoutePattern()
		if route == "" {
			route = compatRouteUnmatched
		}
		method := compatMethodLabel(r.Method)
		client := compatClientFamily(r)
		compatRequestsTotal.WithLabelValues(route, method, compatStatusClass(sw.status, sw.hijacked), client).Inc()
		if _, untimed := untimedCompatRoutes[route]; !untimed {
			seconds := elapsed.Seconds()
			compatRequestDuration.WithLabelValues(route, method).Observe(seconds)
			compatClientRequestDuration.WithLabelValues(client).Observe(seconds)
		}

		if !span.IsRecording() {
			return
		}
		span.SetName("jellycompat " + method + " " + route)
		span.SetAttributes(
			attribute.String("http.request.method", method),
			attribute.String("http.route", route),
			attribute.String("jellycompat.client", client),
		)
		if sw.status == 0 && sw.hijacked {
			span.SetAttributes(attribute.String("http.response.outcome", compatStatusHijacked))
			return
		}
		status := statusOrDefault(sw.status)
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "server_error")
		}
	})
}

// compatMethodLabel folds the method into a bounded label. chi answers any
// syntactically valid method token with 405, so an invented method must not
// mint a series.
func compatMethodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	}
	return compatLabelOther
}

// compatStatusClass buckets the status the client received. A handler that
// wrote nothing still answers 200. A hijacked connection (the session socket)
// reports no HTTP status through the writer, so it gets its own class.
func compatStatusClass(status int, hijacked bool) string {
	if status == 0 && hijacked {
		return compatStatusHijacked
	}
	if class := statusOrDefault(status) / 100; class >= 1 && class <= len(compatStatusClasses) {
		return compatStatusClasses[class-1]
	}
	return compatLabelOther
}

// compatClientFamilies folds a Jellyfin client's identity into a fixed label.
// The first token found wins, so specific names precede the bare "jellyfin"
// the official apps share. A nameOnly token is trusted only in the MediaBrowser
// Client field: "android tv" also appears in the Dalvik User-Agent of every app
// on an NVIDIA Shield. Tokens are lower-case ASCII. The table is the label
// vocabulary; naming each family twice would bury what it says.
//
//nolint:goconst
var compatClientFamilies = []struct {
	token, family string
	nameOnly      bool
}{
	{token: "infuse", family: "infuse"},
	{token: "swiftfin", family: "swiftfin"},
	{token: "findroid", family: "findroid"},
	{token: "streamyfin", family: "streamyfin"},
	{token: "wholphin", family: "wholphin"},
	{token: "fladder", family: "fladder"},
	{token: "moonfin", family: "moonfin"},
	{token: "senplayer", family: "senplayer"},
	{token: "vidhub", family: "vidhub"},
	{token: "kodi", family: "kodi"},
	{token: "jellycon", family: "kodi"},
	{token: "jellyfin web", family: "jellyfin-web"},
	{token: "android tv", family: "jellyfin-androidtv", nameOnly: true},
	{token: "jellyfin", family: "jellyfin"},
}

// maxCompatClientScan bounds how much of the Client field and the User-Agent
// the family match reads. Real values are far shorter, and an oversized header
// then costs no more to classify than a normal one.
const maxCompatClientScan = 512

// compatClientFamily reads the MediaBrowser Client field first, with the same
// parser the playback path keys sessions on, then the User-Agent, which is all
// that identifies requests without an authorization header (images, media URLs
// with an api_key). A client outside the list is "other"; a request with no
// identity at all is "none". Free text never reaches the label. Matching folds
// ASCII case into a stack buffer, so it does not allocate.
func compatClientFamily(r *http.Request) string {
	var buf [maxCompatClientScan]byte
	name := appendLowerASCII(buf[:0], firstMediaBrowserAuthorizationValue(r, "Client"))
	for _, c := range compatClientFamilies {
		if containsToken(name, c.token) {
			return c.family
		}
	}
	hasName := len(name) > 0
	userAgent := appendLowerASCII(buf[:0], r.UserAgent())
	for _, c := range compatClientFamilies {
		if !c.nameOnly && containsToken(userAgent, c.token) {
			return c.family
		}
	}
	if !hasName && len(userAgent) == 0 {
		return compatClientNone
	}
	return compatLabelOther
}

// appendLowerASCII appends s to dst with ASCII letters lower-cased, stopping
// when dst is full.
func appendLowerASCII(dst []byte, s string) []byte {
	for i := 0; i < len(s) && len(dst) < cap(dst); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		dst = append(dst, c)
	}
	return dst
}

// containsToken reports whether b contains token. It searches for the token's
// first byte with bytes.IndexByte and compares the rest in place.
func containsToken(b []byte, token string) bool {
	for len(b) >= len(token) {
		i := bytes.IndexByte(b[:len(b)-len(token)+1], token[0])
		if i < 0 {
			return false
		}
		if string(b[i:i+len(token)]) == token {
			return true
		}
		b = b[i+1:]
	}
	return false
}
