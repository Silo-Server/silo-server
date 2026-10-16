package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
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

// A client over its own per-IP limit must not drain the shared global budget:
// with a two-request global burst and a one-request per-IP burst, client A's
// rejected retries used to spend the global tokens and get client B rejected.
// The custom writer covers the Jellyfin listener, which shares this limiter.
func TestHandlerRejectedClientDoesNotDrainGlobalBudget(t *testing.T) {
	writers := map[string]func(*Middleware) func(http.Handler) http.Handler{
		"native": func(mw *Middleware) func(http.Handler) http.Handler { return mw.Handler },
		"custom reject body": func(mw *Middleware) func(http.Handler) http.Handler {
			return mw.HandlerWithRejectBody(func(w http.ResponseWriter, _ int) {
				w.WriteHeader(http.StatusTooManyRequests)
			})
		},
	}
	for name, wrap := range writers {
		t.Run(name, func(t *testing.T) {
			perKey := NewMemoryLimiter()
			t.Cleanup(perKey.Close)
			global := NewMemoryLimiter()
			t.Cleanup(global.Close)
			mw := NewMiddleware(perKey, global, nil, true)
			cfg := DefaultConfig()
			cfg.GlobalReqPerSecond = 2 // burst of 2, refills one token every 500ms
			cfg.IPReqPerSecond = 0.001
			cfg.IPReqPerMinute = 0.06
			cfg.IPBurst = 1
			mw.cfg = cfg

			served := map[string]int{}
			handler := wrap(mw)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				served[clientip.FromContext(r.Context())]++
				w.WriteHeader(http.StatusNoContent)
			}))
			send := func(ip string) int {
				req := httptest.NewRequest(http.MethodGet, "/api/v1/items", nil)
				req = req.WithContext(clientip.SetContext(req.Context(), ip))
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec.Code
			}

			const clientA, clientB = "203.0.113.10", "203.0.113.20"
			if code := send(clientA); code != http.StatusNoContent {
				t.Fatalf("client A first request = %d, want 204", code)
			}
			for i := range 5 {
				if code := send(clientA); code != http.StatusTooManyRequests {
					t.Fatalf("client A retry %d = %d, want 429", i, code)
				}
			}
			if code := send(clientB); code != http.StatusNoContent {
				t.Fatalf("client B = %d, want 204: client A's rejected requests drained the global budget", code)
			}
			if served[clientA] != 1 || served[clientB] != 1 {
				t.Fatalf("served = %#v, want one request each", served)
			}
		})
	}
}

func TestHandlerDoesNotConsumeGlobalBudgetForRejectedClient(t *testing.T) {
	for _, deniedKey := range []string{"ip:203.0.113.10", "key:7"} {
		t.Run(deniedKey, func(t *testing.T) {
			perKey := &recordingLimiter{denied: map[string]bool{deniedKey: true}}
			global := &recordingLimiter{}
			mw := NewMiddleware(perKey, global, nil, true)
			mw.cfg = DefaultConfig()
			handler := mw.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("next handler called for rejected request")
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/items", nil)
			ctx := clientip.SetContext(req.Context(), "203.0.113.10")
			ctx = apimw.SetClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAPIKey, APIKeyID: 7})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req.WithContext(ctx))

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
