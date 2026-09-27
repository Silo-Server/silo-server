package activitylog

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureWriter records every entry the middleware emits.
type captureWriter struct {
	entries []LogEntry
}

func (w *captureWriter) Write(entry LogEntry) { w.entries = append(w.entries, entry) }
func (w *captureWriter) Close() error         { return nil }

// TestMiddlewarePathFiltering pins which request paths reach the activity log.
// The v1 and v2 majors serve the same health, admin-log and streaming shapes,
// so every rule has to hold on both.
func TestMiddlewarePathFiltering(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		logged bool
	}{
		// v1 exclusions.
		{name: "v1 health", path: "/api/v1/health", logged: false},
		{name: "v1 ready", path: "/api/v1/ready", logged: false},
		{name: "v1 admin logs", path: "/api/v1/admin/logs", logged: false},
		{name: "v1 admin logs app", path: "/api/v1/admin/logs/app", logged: false},

		// v2 exclusions: every operation under the admin log prefix.
		{name: "v2 admin logs app", path: "/api/v2/admin/logs/app", logged: false},
		{name: "v2 admin logs audit", path: "/api/v2/admin/logs/audit", logged: false},
		{name: "v2 admin logs ws", path: "/api/v2/admin/logs/ws", logged: false},
		{name: "v2 admin logs ws ticket", path: "/api/v2/admin/logs/ws-ticket", logged: false},
		{name: "v2 admin logs ws capabilities", path: "/api/v2/admin/logs/ws/capabilities", logged: false},

		// Session starts stay logged on both majors.
		{name: "v1 stream session start", path: "/api/v1/stream/sess-1", logged: true},
		{name: "v2 stream session start", path: "/api/v2/stream/sess-1", logged: true},

		// Per-chunk stream fetches are suppressed on both majors.
		{name: "v1 stream subtitles", path: "/api/v1/stream/sess-1/subtitles/2", logged: false},
		{name: "v2 stream subtitles", path: "/api/v2/stream/sess-1/subtitles/2", logged: false},
		{name: "v2 stream subtitle fonts", path: "/api/v2/stream/sess-1/subtitles/2/fonts", logged: false},
		{name: "v1 transcode master playlist", path: "/api/v1/playback/transcode/sess-1/master.m3u8", logged: false},
		{name: "v2 transcode master playlist", path: "/api/v2/playback/transcode/sess-1/master.m3u8", logged: false},
		{name: "v1 transcode segment", path: "/api/v1/playback/transcode/sess-1/segment/0.ts", logged: false},
		{name: "v2 transcode segment", path: "/api/v2/playback/transcode/sess-1/segment/0.ts", logged: false},

		// Neighboring paths must not be swept up by prefix matching.
		{name: "v2 admin stream telemetry parity", path: "/api/v2/admin/stream-telemetry/parity", logged: true},
		{name: "v2 catalog item", path: "/api/v2/catalog/items/abc", logged: true},
		{name: "v1 catalog item", path: "/api/v1/catalog/items/abc", logged: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &captureWriter{}
			var handlerCalls int
			h := NewMiddleware(w, "node-1")(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
				handlerCalls++
				rw.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			h.ServeHTTP(httptest.NewRecorder(), req)

			if handlerCalls != 1 {
				t.Fatalf("handler called %d times, want 1", handlerCalls)
			}
			if got := len(w.entries) == 1; got != tc.logged {
				t.Fatalf("path %q: logged=%v (entries=%d), want logged=%v", tc.path, got, len(w.entries), tc.logged)
			}
			if tc.logged && w.entries[0].Path != tc.path {
				t.Fatalf("logged path = %q, want %q", w.entries[0].Path, tc.path)
			}
		})
	}
}

// TestIsStreamChunk pins chunk detection on the concrete v1 and v2 route
// shapes, independent of the prefix list.
func TestIsStreamChunk(t *testing.T) {
	tests := []struct {
		path  string
		chunk bool
	}{
		{path: "/api/v1/stream/sess-1", chunk: false},
		{path: "/api/v2/stream/sess-1", chunk: false},
		{path: "/api/v2/stream/sess-1/subtitles/eng", chunk: true},
		{path: "/api/v2/stream/sess-1/subtitles/eng/fonts", chunk: true},
		{path: "/api/v2/playback/transcode/sess-1/master.m3u8", chunk: true},
		{path: "/api/v2/playback/transcode/sess-1/segment/3.ts", chunk: true},
		{path: "/api/v1/playback/transcode/sess-1/master.m3u8", chunk: true},
		{path: "/api/v1/playback/transcode/sess-1/segment/3.ts", chunk: true},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := isStreamChunk(tc.path); got != tc.chunk {
				t.Fatalf("isStreamChunk(%q) = %v, want %v", tc.path, got, tc.chunk)
			}
		})
	}
}
