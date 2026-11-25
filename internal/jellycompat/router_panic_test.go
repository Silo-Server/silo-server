package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/config"
)

type compatPanicActivityWriter struct {
	mu      sync.Mutex
	entries []activitylog.LogEntry
}

func (w *compatPanicActivityWriter) Write(entry activitylog.LogEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, entry)
}

func (*compatPanicActivityWriter) Close() error { return nil }

func (w *compatPanicActivityWriter) recorded() []activitylog.LogEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]activitylog.LogEntry(nil), w.entries...)
}

// A Jellyfin handler that panics is answered with a 500, and the request
// metrics and the activity log record that 500, also when the request asks for
// a connection upgrade, for which chi's Recoverer writes no status (#5236).
func TestCompatPanickingRequestIsRecordedAsServerError(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	for _, tc := range []struct {
		name    string
		pattern string
		upgrade bool
	}{
		{name: "plain request", pattern: "/panic-test"},
		{name: "upgrade request", pattern: "/panic-upgrade-test", upgrade: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			activity := &compatPanicActivityWriter{}
			router := NewRouter(Dependencies{Config: cfg, ActivityLogWriter: activity})
			router.Get(tc.pattern, func(http.ResponseWriter, *http.Request) { panic("boom") })

			req := httptest.NewRequest(http.MethodGet, tc.pattern, nil)
			if tc.upgrade {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			counter := compatRequestsTotal.WithLabelValues(tc.pattern, compatMethodLabel(http.MethodGet), compatStatusClass(http.StatusInternalServerError, false), compatClientFamily(req))
			before := testutil.ToFloat64(counter)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			if got := testutil.ToFloat64(counter) - before; got != 1 {
				t.Fatalf("silo_jellycompat_requests_total{status_class=5xx} grew by %v, want 1", got)
			}
			entries := activity.recorded()
			if len(entries) != 1 || entries[0].StatusCode != http.StatusInternalServerError || entries[0].PathPattern != tc.pattern {
				t.Fatalf("activity entries = %+v, want one 500 row for %s", entries, tc.pattern)
			}
		})
	}
}
