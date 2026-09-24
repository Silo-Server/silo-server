package jellycompat

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/telemetry"
)

const (
	compatRequestsMetric = "silo_jellycompat_requests_total"
	compatDurationMetric = "silo_jellycompat_request_duration_seconds"
)

// The label vocabulary is a dashboard contract: every value a series can carry
// is listed here, so a new family or class is a deliberate test change.
var (
	compatMetricMethods  = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "other"}
	compatMetricClasses  = []string{"1xx", "2xx", "3xx", "4xx", "5xx", "hijacked", "other"}
	compatMetricFamilies = []string{
		"infuse", "swiftfin", "findroid", "streamyfin", "wholphin", "fladder", "senplayer", "vidhub", "kodi",
		"jellyfin-web", "jellyfin-androidtv", "jellyfin", "other", "none",
	}
)

func newObservedTestRouter(t testing.TB, artwork http.Handler, sessions *SessionStore) chi.Router {
	t.Helper()
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	if sessions == nil {
		sessions = NewSessionStore(time.Hour, time.Now)
	}
	return NewRouter(Dependencies{
		Config:         cfg,
		SessionStore:   sessions,
		ContentService: &genresContentService{},
		ArtworkHandler: artwork,
	})
}

// compatSample is one jellycompat request series: a counter's value or a
// histogram's sample count.
type compatSample struct {
	metric string
	labels map[string]string
	value  float64
}

// gatherCompatSeries reads every jellycompat request series from the default
// registry. Keys are the metric name and its label pairs, which the gatherer
// sorts by name.
func gatherCompatSeries(t *testing.T) map[string]compatSample {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	series := map[string]compatSample{}
	for _, family := range families {
		if family.GetName() != compatRequestsMetric && family.GetName() != compatDurationMetric {
			continue
		}
		for _, metric := range family.GetMetric() {
			sample := compatSample{metric: family.GetName(), labels: map[string]string{}}
			pairs := make([]string, 0, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				sample.labels[label.GetName()] = label.GetValue()
				pairs = append(pairs, label.GetName()+"="+label.GetValue())
			}
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				sample.value = metric.GetCounter().GetValue()
			case dto.MetricType_HISTOGRAM:
				sample.value = float64(metric.GetHistogram().GetSampleCount())
			}
			series[family.GetName()+"{"+strings.Join(pairs, ",")+"}"] = sample
		}
	}
	return series
}

// compatSeriesDelta runs fn and returns only the series it changed, with the
// change as the value. The registry is process-wide, so comparing snapshots
// keeps the assertions independent of what other tests recorded.
func compatSeriesDelta(t *testing.T, fn func()) map[string]compatSample {
	t.Helper()
	before := gatherCompatSeries(t)
	fn()
	changed := map[string]compatSample{}
	for key, sample := range gatherCompatSeries(t) {
		if delta := sample.value - before[key].value; delta != 0 {
			sample.value = delta
			changed[key] = sample
		}
	}
	return changed
}

func assertCompatSeries(t *testing.T, got map[string]compatSample, want map[string]float64) {
	t.Helper()
	for key, value := range want {
		if got[key].value != value {
			t.Errorf("%s delta = %v, want %v", key, got[key].value, value)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected series %s", key)
		}
	}
}

func compatAuthHeader(client string) string {
	return `MediaBrowser Client="` + client + `", Device="Test", DeviceId="device-1", Version="1.0"`
}

// TestCompatRequestMetrics drives compat routes through the real router and
// asserts the exact series each request adds. The route label is the chi
// template, never the raw path or an ID; media body routes are counted but not
// timed, while an HLS manifest is timed; requests the router never matched fold
// into "unmatched".
func TestCompatRequestMetrics(t *testing.T) {
	router := newObservedTestRouter(t, nil, nil)
	requests := []struct {
		method, path, authorization, userAgent string
		wantStatus                             int
	}{
		{http.MethodGet, "/System/Info/Public", compatAuthHeader("Infuse-Direct"), "", http.StatusOK},
		// Path normalization runs before routing, so a prefixed lowercase path
		// lands on the same template.
		{http.MethodGet, "/emby/system/info/public", compatAuthHeader("Jellyfin Web"), "Mozilla/5.0", http.StatusOK},
		{http.MethodGet, "/Items/0123abcdef", compatAuthHeader("Swiftfin tvOS"), "", http.StatusUnauthorized},
		{http.MethodGet, "/Videos/0123abcdef/stream", "", "Infuse/8.0 (iPhone)", http.StatusUnauthorized},
		{http.MethodGet, "/Videos/0123abcdef/master.m3u8", compatAuthHeader("Findroid"), "", http.StatusUnauthorized},
		{http.MethodGet, "/no/such/route", "", "", http.StatusNotFound},
		{"PURGE", "/System/Info/Public", "", "", http.StatusMethodNotAllowed},
	}

	got := compatSeriesDelta(t, func() {
		for _, tc := range requests {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.authorization != "" {
				req.Header.Set("X-Emby-Authorization", tc.authorization)
			}
			if tc.userAgent != "" {
				req.Header.Set("User-Agent", tc.userAgent)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.wantStatus)
			}
		}
	})
	// The histogram carries route and method only, so both clients' requests
	// to /System/Info/Public land in one duration series.
	assertCompatSeries(t, got, map[string]float64{
		compatRequestsMetric + "{client=infuse,method=GET,route=/System/Info/Public,status_class=2xx}":       1,
		compatRequestsMetric + "{client=jellyfin-web,method=GET,route=/System/Info/Public,status_class=2xx}": 1,
		compatDurationMetric + "{method=GET,route=/System/Info/Public}":                                      2,
		compatRequestsMetric + "{client=swiftfin,method=GET,route=/Items/{id},status_class=4xx}":             1,
		compatDurationMetric + "{method=GET,route=/Items/{id}}":                                              1,
		// A media body route is counted but has no duration series.
		compatRequestsMetric + "{client=infuse,method=GET,route=/Videos/{id}/stream,status_class=4xx}": 1,
		// A manifest is a short document, so it is timed.
		compatRequestsMetric + "{client=findroid,method=GET,route=/Videos/{id}/master.m3u8,status_class=4xx}": 1,
		compatDurationMetric + "{method=GET,route=/Videos/{id}/master.m3u8}":                                  1,
		compatRequestsMetric + "{client=none,method=GET,route=unmatched,status_class=4xx}":                    1,
		compatDurationMetric + "{method=GET,route=unmatched}":                                                 1,
		compatRequestsMetric + "{client=none,method=other,route=unmatched,status_class=4xx}":                  1,
		compatDurationMetric + "{method=other,route=unmatched}":                                               1,
	})
}

// TestCompatRequestMetricLabelsStayBounded is the cardinality guard: hostile
// input (an ID-bearing path, an invented method, a free-text client name) must
// fold into a registered route template and the fixed method, class and client
// sets, and no untimed route may gain a duration series.
func TestCompatRequestMetricLabelsStayBounded(t *testing.T) {
	router := newObservedTestRouter(t, nil, nil)
	routes := map[string]bool{compatRouteUnmatched: true}
	if err := chi.Walk(router, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[route] = true
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, family := range compatClientFamilies {
		if !slices.Contains(compatMetricFamilies, family.family) {
			t.Errorf("client family %q is missing from the documented vocabulary", family.family)
		}
	}

	series := compatSeriesDelta(t, func() {
		for _, tc := range []struct{ method, path, client string }{
			{http.MethodGet, "/Items/9f1c0e7a2b", "My Private Player 3.1"},
			{"BREW", "/Items/9f1c0e7a2b/Similar", "Findroid"},
			{http.MethodGet, "/Videos/9f1c0e7a2b/hls/p1/7.ts", "Kodi JellyCon"},
			{http.MethodGet, "/Videos/9f1c0e7a2b/9f1c/Subtitles/3/stream.srt", "Wholphin"},
			{http.MethodGet, "/Users/9f1c0e7a2b/Items", "Fladder"},
		} {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("X-Emby-Authorization", compatAuthHeader(tc.client))
			router.ServeHTTP(httptest.NewRecorder(), req)
		}
	})
	if len(series) == 0 {
		t.Fatal("no jellycompat request series were exported")
	}
	for key, sample := range series {
		for name, value := range sample.labels {
			var ok bool
			switch name {
			case "route":
				ok = routes[value]
			case "method":
				ok = slices.Contains(compatMetricMethods, value)
			case "status_class":
				ok = slices.Contains(compatMetricClasses, value)
			case "client":
				ok = slices.Contains(compatMetricFamilies, value)
			}
			if !ok {
				t.Errorf("series %s: label %s=%q is outside the bounded vocabulary", key, name, value)
			}
		}
		if strings.Contains(key, "9f1c") || strings.Contains(key, "Private") {
			t.Errorf("series %s leaks request text", key)
		}
		if _, untimed := untimedCompatRoutes[sample.labels["route"]]; untimed && sample.metric == compatDurationMetric {
			t.Errorf("untimed route %s has a duration series", sample.labels["route"])
		}
	}
}

// TestCompatSocketUpgradeIsCountedAsHijacked upgrades the session socket
// through the whole compat middleware chain on a real listener. The upgrade
// must reach the connection through every status-recording writer, and the
// request must be counted once as hijacked with no duration series.
func TestCompatSocketUpgradeIsCountedAsHijacked(t *testing.T) {
	sessions := NewSessionStore(time.Hour, time.Now)
	if err := sessions.Put(Session{Token: "socket-metrics-token", StreamAppUserID: 1, ProfileID: "profile-1"}); err != nil {
		t.Fatalf("put session: %v", err)
	}
	server := httptest.NewServer(newObservedTestRouter(t, nil, sessions))
	t.Cleanup(server.Close)

	const key = compatRequestsMetric + "{client=streamyfin,method=GET,route=/socket,status_class=hijacked}"
	got := compatSeriesDelta(t, func() {
		recorded := gatherCompatSeries(t)[key].value
		header := http.Header{"X-Emby-Authorization": {compatAuthHeader("Streamyfin") + `, Token="socket-metrics-token"`}}
		conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/socket", header)
		if response != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			t.Fatalf("dial socket: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil || msg.MessageType != "ForceKeepAlive" {
			t.Fatalf("first socket message = %+v, %v", msg, err)
		}
		_ = conn.Close()
		// The handler returns, and the request is recorded, once the server
		// notices the close.
		deadline := time.Now().Add(5 * time.Second)
		for gatherCompatSeries(t)[key].value == recorded && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})
	assertCompatSeries(t, got, map[string]float64{key: 1})
}

func TestCompatClientFamily(t *testing.T) {
	const shieldDalvik = "Dalvik/2.1.0 (Linux; U; Android 11; SHIELD Android TV Build/RQ1A.210105.003)"
	for _, tc := range []struct {
		header, value, userAgent, want string
	}{
		{"X-Emby-Authorization", compatAuthHeader("Infuse-Direct"), "", "infuse"},
		{"X-Emby-Authorization", compatAuthHeader("Swiftfin tvOS"), "", "swiftfin"},
		{"X-Emby-Authorization", compatAuthHeader("Kodi JellyCon"), "", "kodi"},
		{"X-Emby-Authorization", compatAuthHeader("Fladder"), "", "fladder"},
		{"X-Emby-Authorization", compatAuthHeader("Jellyfin Web"), "Mozilla/5.0 (Macintosh)", "jellyfin-web"},
		{"X-Emby-Authorization", compatAuthHeader("Android TV"), "", "jellyfin-androidtv"},
		{"X-Emby-Authorization", compatAuthHeader("Jellyfin Media Player"), "", "jellyfin"},
		{"Authorization", compatAuthHeader("Streamyfin"), "", "streamyfin"},
		// The Client field wins over a User-Agent naming another product.
		{"X-Emby-Authorization", compatAuthHeader("Findroid"), shieldDalvik, "findroid"},
		// Without an authorization header the User-Agent decides, but only
		// through product names: every app on a Shield says "Android TV".
		{"", "", "Infuse/8.0 (AppleTV)", "infuse"},
		{"", "", shieldDalvik, "other"},
		{"X-Emby-Authorization", compatAuthHeader("My Private Player"), "", "other"},
		{"", "", "", "none"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/Items", nil)
		if tc.header != "" {
			req.Header.Set(tc.header, tc.value)
		}
		if tc.userAgent != "" {
			req.Header.Set("User-Agent", tc.userAgent)
		}
		if got := compatClientFamily(req); got != tc.want {
			t.Errorf("client family(%s=%q, User-Agent=%q) = %q, want %q", tc.header, tc.value, tc.userAgent, got, tc.want)
		}
	}
}

func TestCompatStatusClass(t *testing.T) {
	for _, tc := range []struct {
		status   int
		hijacked bool
		want     string
	}{
		{0, false, "2xx"},
		{0, true, "hijacked"},
		{http.StatusSwitchingProtocols, true, "1xx"},
		{http.StatusNoContent, false, "2xx"},
		{http.StatusFound, false, "3xx"},
		{http.StatusUnauthorized, false, "4xx"},
		{http.StatusBadGateway, false, "5xx"},
		{799, false, "other"},
	} {
		if got := compatStatusClass(tc.status, tc.hijacked); got != tc.want {
			t.Errorf("status class(%d, hijacked=%v) = %q, want %q", tc.status, tc.hijacked, got, tc.want)
		}
	}
}

// TestCompatRequestSpanParentsDependencySpans proves a compat request opens
// one server span and that a dependency span started by the handler joins its
// trace instead of becoming an orphan root. A caller's trace context is
// ignored: a public listener must not let clients pick trace IDs or sampling.
func TestCompatRequestSpanParentsDependencySpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })

	artwork := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, done := telemetry.StartDependency(r.Context(), "postgres", "api", "query")
		done(nil)
		w.WriteHeader(http.StatusNoContent)
	})
	router := newObservedTestRouter(t, artwork, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v2/artwork/poster/abc123?token=secret", nil)
	req.Header.Set("Traceparent", "00-01000000000000000000000000000000-0200000000000000-01")
	req.Header.Set("X-Emby-Authorization", compatAuthHeader("Findroid"))
	remote := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, Remote: true})
	req = req.WithContext(trace.ContextWithRemoteSpanContext(req.Context(), remote))
	router.ServeHTTP(httptest.NewRecorder(), req)

	spans := exporter.GetSpans()
	var server, dependency *tracetest.SpanStub
	for i := range spans {
		switch spans[i].SpanKind {
		case trace.SpanKindServer:
			server = &spans[i]
		case trace.SpanKindClient:
			dependency = &spans[i]
		}
	}
	if server == nil || dependency == nil || len(spans) != 2 {
		t.Fatalf("spans = %d (server=%v dependency=%v), want one server and one dependency span", len(spans), server != nil, dependency != nil)
	}
	if server.Name != "jellycompat GET /api/v2/artwork/*" {
		t.Errorf("server span name = %q", server.Name)
	}
	if server.Parent.IsValid() || server.SpanContext.TraceID() == remote.TraceID() {
		t.Errorf("server span adopted the caller's trace context: parent=%v trace=%v", server.Parent, server.SpanContext.TraceID())
	}
	if dependency.Parent.SpanID() != server.SpanContext.SpanID() || dependency.SpanContext.TraceID() != server.SpanContext.TraceID() {
		t.Errorf("dependency span is not a child of the server span: parent=%v server=%v", dependency.Parent, server.SpanContext)
	}
	attrs := map[string]string{}
	for _, kv := range server.Attributes {
		attrs[string(kv.Key)] = kv.Value.String()
	}
	for key, want := range map[string]string{
		"http.request.method":       "GET",
		"http.route":                "/api/v2/artwork/*",
		"http.response.status_code": "204",
		"jellycompat.client":        "findroid",
	} {
		if attrs[key] != want {
			t.Errorf("server span %s = %q, want %q", key, attrs[key], want)
		}
	}
	for _, kv := range server.Attributes {
		if strings.Contains(kv.Value.String(), "secret") || strings.Contains(kv.Value.String(), "abc123") {
			t.Errorf("server span attribute %s leaks request text: %q", kv.Key, kv.Value.String())
		}
	}
}

// BenchmarkCompatRouterPing measures one request through the full compat
// middleware chain to a trivial handler, so the number isolates what the
// chain costs per request. The request log is discarded.
func BenchmarkCompatRouterPing(b *testing.B) {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	b.Cleanup(func() { slog.SetDefault(previous) })
	router := newObservedTestRouter(b, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/System/Ping", nil)
	req.Header.Set("X-Emby-Authorization", compatAuthHeader("Infuse-Direct"))
	req.Header.Set("User-Agent", "Infuse/8.0 (AppleTV)")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		router.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// BenchmarkCompatRequestObserver isolates the middleware: the same chi route
// with and without it. "observed" runs with the default no-op tracer, as a
// server without OTel does; "observed-sampled" records every span, the cost
// ceiling with tracing on.
func BenchmarkCompatRequestObserver(b *testing.B) {
	handler := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	newRouter := func(observed bool) chi.Router {
		r := chi.NewRouter()
		if observed {
			r.Use(observeCompatRequest)
		}
		r.Get("/Items/{id}", handler)
		return r
	}
	req := httptest.NewRequest(http.MethodGet, "/Items/0123abcdef", nil)
	req.Header.Set("X-Emby-Authorization", compatAuthHeader("Infuse-Direct"))
	req.Header.Set("User-Agent", "Infuse/8.0 (AppleTV)")
	run := func(b *testing.B, router http.Handler) {
		w := httptest.NewRecorder()
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			router.ServeHTTP(w, req)
		}
	}
	b.Run("bare", func(b *testing.B) { run(b, newRouter(false)) })
	b.Run("observed", func(b *testing.B) { run(b, newRouter(true)) })
	b.Run("observed-sampled", func(b *testing.B) {
		provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
		previous := otel.GetTracerProvider()
		otel.SetTracerProvider(provider)
		b.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
		run(b, newRouter(true))
	})
}
