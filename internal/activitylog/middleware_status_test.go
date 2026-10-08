package activitylog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A request its client abandoned is recorded as 499 when it got no response
// or a server error: the client never saw an answer, so it is not a server
// failure. Anything else keeps the status that was written.
func TestMiddlewareRecordsAbandonedRequests(t *testing.T) {
	canceled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	expired := func() context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		t.Cleanup(cancel)
		return ctx
	}
	for _, tc := range []struct {
		name   string
		ctx    func() context.Context
		status int // 0 writes nothing
		want   int
	}{
		{"client gone, nothing written", canceled, 0, StatusClientClosedRequest},
		{"client gone, server error", canceled, http.StatusInternalServerError, StatusClientClosedRequest},
		{"client gone, not found", canceled, http.StatusNotFound, http.StatusNotFound},
		{"client gone, success started", canceled, http.StatusOK, http.StatusOK},
		{"client waiting, server error", context.Background, http.StatusInternalServerError, http.StatusInternalServerError},
		{"client waiting, nothing written", context.Background, 0, http.StatusOK},
		{"deadline, server error", expired, http.StatusServiceUnavailable, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &captureWriter{}
			h := NewMiddleware(capture, "node")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
			}))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v2/libraries", nil).WithContext(tc.ctx()))
			if len(capture.entries) != 1 || capture.entries[0].StatusCode != tc.want {
				t.Fatalf("entries = %+v, want status %d", capture.entries, tc.want)
			}
		})
	}
}
