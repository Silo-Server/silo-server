package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

type recordingLimiter struct {
	keys   []string
	denied map[string]bool
}

func (l *recordingLimiter) Allow(_ context.Context, key string, limit Rate) AllowResult {
	l.keys = append(l.keys, key)
	return AllowResult{
		Allowed:   !l.denied[key],
		Limit:     max(1, int(limit.RequestsPerSecond)),
		Remaining: 1,
		ResetAt:   time.Now().Add(time.Second),
	}
}

func (*recordingLimiter) Close() {}

func TestAuthEndpointHandlerUsesGlobalIPAndEndpointBudgets(t *testing.T) {
	perKey := &recordingLimiter{}
	global := &recordingLimiter{}
	mw := NewMiddleware(perKey, global, nil, true)
	mw.cfg = DefaultConfig()
	nextCalled := false
	handler := mw.AuthEndpointHandler("login")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req = req.WithContext(clientip.SetContext(req.Context(), "203.0.113.10"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || !nextCalled {
		t.Fatalf("response = %d, next called = %v", rec.Code, nextCalled)
	}
	if !slices.Equal(global.keys, []string{"global"}) {
		t.Fatalf("global limiter keys = %#v", global.keys)
	}
	if !slices.Equal(perKey.keys, []string{"ip:203.0.113.10", "authip:203.0.113.10:login"}) {
		t.Fatalf("per-key limiter keys = %#v", perKey.keys)
	}
}

func TestAuthEndpointHandlerDoesNotConsumeGlobalBudgetForRejectedClient(t *testing.T) {
	for _, deniedKey := range []string{"ip:203.0.113.10", "authip:203.0.113.10:login"} {
		t.Run(deniedKey, func(t *testing.T) {
			perKey := &recordingLimiter{denied: map[string]bool{deniedKey: true}}
			global := &recordingLimiter{}
			mw := NewMiddleware(perKey, global, nil, true)
			mw.cfg = DefaultConfig()
			handler := mw.AuthEndpointHandler("login")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("next handler called for rejected request")
			}))
			req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
			req = req.WithContext(clientip.SetContext(req.Context(), "203.0.113.10"))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("response = %d, want 429", rec.Code)
			}
			if len(global.keys) != 0 {
				t.Fatalf("global limiter keys = %#v, want no consumed budget", global.keys)
			}
		})
	}
}

func TestGlobalRateSupportsFractionalRequestsPerSecond(t *testing.T) {
	rate := globalRateFor(Config{GlobalReqPerSecond: 0.5})
	if rate.Burst != 1 {
		t.Fatalf("burst = %d, want 1", rate.Burst)
	}
}

func TestGlobalRateBoundsUnsafeRequestsPerSecond(t *testing.T) {
	rate := globalRateFor(Config{GlobalReqPerSecond: 1e308})
	if rate.RequestsPerSecond != MaxGlobalRequestsPerSecond {
		t.Fatalf("requests per second = %g, want %g", rate.RequestsPerSecond, MaxGlobalRequestsPerSecond)
	}
	if rate.RequestsPerMinute > MaxRequestsPerWindow {
		t.Fatalf("requests per minute = %g, must not exceed %g", rate.RequestsPerMinute, MaxRequestsPerWindow)
	}
	if rate.Burst <= 0 {
		t.Fatalf("burst = %d, want a positive bounded value", rate.Burst)
	}
}
