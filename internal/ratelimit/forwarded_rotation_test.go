package ratelimit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

// Synthetic RemoteAddr fixtures exercise the production resolver and limiter.
// They do not claim that any deployed reverse proxy passes these headers through.
func TestHandlerBoundsForwardedRotation(t *testing.T) {
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP", "repeated X-Forwarded-For"} {
		t.Run(header, func(t *testing.T) {
			perKey, global := NewMemoryLimiter(), NewMemoryLimiter()
			t.Cleanup(perKey.Close)
			t.Cleanup(global.Close)
			mw := NewMiddleware(perKey, global, nil, true)
			mw.cfg = DefaultConfig()
			mw.cfg.IPReqPerSecond = 0.001
			mw.cfg.IPReqPerMinute = 1
			mw.cfg.IPBurst = 1
			trusted, err := clientip.ParseCIDRs(clientip.DefaultTrustedProxies)
			if err != nil {
				t.Fatal(err)
			}
			handler := clientip.Middleware(clientip.NewResolver(trusted))(mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})))
			const requests = 24
			allowed := 0
			for i := range requests {
				req := httptest.NewRequest(http.MethodGet, "/api/v2/account/me", nil)
				req.RemoteAddr = "192.168.1.20:40000"
				req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 42, TokenType: auth.TokenTypeAccess, SessionID: fmt.Sprintf("session-%d", i)}))
				req.Header.Set("X-Profile-Id", fmt.Sprintf("profile-%d", i))
				req.Header.Set("X-Device-Id", fmt.Sprintf("device-%d", i))
				ip := fmt.Sprintf("203.0.113.%d", i+1)
				if header == "repeated X-Forwarded-For" {
					req.Header.Add("X-Forwarded-For", "198.51.100.1")
					req.Header.Add("X-Forwarded-For", ip)
				} else {
					req.Header.Set(header, ip)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code == http.StatusNoContent {
					allowed++
				} else if rec.Code != http.StatusTooManyRequests {
					t.Fatalf("response = %d, want 204 or 429", rec.Code)
				}
			}
			t.Logf("synthetic trusted private peer, %s rotation: %d/%d allowed; source burst 1, global burst 1000", header, allowed, requests)
			if allowed != accountBudgetMultiplier {
				t.Fatalf("%d requests admitted with fresh forwarded identities, want exactly one shared account allowance of %d", allowed, accountBudgetMultiplier)
			}
		})
	}
}
