package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/config"
)

type capturingActivityWriter struct {
	mu      sync.Mutex
	entries []activitylog.LogEntry
}

func (w *capturingActivityWriter) Write(entry activitylog.LogEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, entry)
}

func (*capturingActivityWriter) Close() error { return nil }

func (w *capturingActivityWriter) recorded() []activitylog.LogEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]activitylog.LogEntry(nil), w.entries...)
}

// nativeRequestCount reads streamapp_http_requests_total for one label set
// from the default registry, where the native metrics middleware records.
func nativeRequestCount(t *testing.T, method, path, status string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "streamapp_http_requests_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["method"] == method && labels["path"] == path && labels["status"] == status {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// A native handler that panics is answered with a 500 by the recovery
// middleware. The request metrics and the activity log record it as that 500,
// like any other server error (#3735), also when the request asks for a
// connection upgrade, for which chi's Recoverer writes no status.
func TestNativePanickingRequestIsRecordedAsServerError(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	for _, tc := range []struct {
		name    string
		pattern string
		upgrade bool
	}{
		{name: "plain request", pattern: "/api/v1/panic-test"},
		{name: "upgrade request", pattern: "/api/v1/panic-upgrade-test", upgrade: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			activity := &capturingActivityWriter{}
			root := chi.NewRouter()
			useBaseMiddleware(root, Dependencies{Config: cfg, ActivityLogWriter: activity})
			root.Get(tc.pattern, func(http.ResponseWriter, *http.Request) { panic("boom") })

			before := nativeRequestCount(t, http.MethodGet, tc.pattern, "500")
			req := httptest.NewRequest(http.MethodGet, tc.pattern, nil)
			if tc.upgrade {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			rec := httptest.NewRecorder()
			root.ServeHTTP(rec, req)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			if got := nativeRequestCount(t, http.MethodGet, tc.pattern, "500") - before; got != 1 {
				t.Fatalf("streamapp_http_requests_total{status=500} grew by %v, want 1", got)
			}
			entries := activity.recorded()
			if len(entries) != 1 || entries[0].StatusCode != http.StatusInternalServerError || entries[0].PathPattern != tc.pattern {
				t.Fatalf("activity entries = %+v, want one 500 row for %s", entries, tc.pattern)
			}
		})
	}
}
