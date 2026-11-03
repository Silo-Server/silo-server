package jellycompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

func TestCompatAccountSharesNativeAllowance(t *testing.T) {
	mw := newTestRateLimit(t, map[string]string{
		"ratelimit.ip.requests_per_second": ".001", "ratelimit.ip.requests_per_minute": "2", "ratelimit.ip.burst": "2",
	})
	sessions := NewSessionStore(time.Hour, nil)
	for _, session := range []Session{
		{Token: "one", StreamAppUserID: 42, ProfileID: "parent"},
		{Token: "two", StreamAppUserID: 42, ProfileID: "child"},
		{Token: "other", StreamAppUserID: 43, ProfileID: "parent"},
	} {
		if err := sessions.Put(session); err != nil {
			t.Fatal(err)
		}
	}
	router := newRateLimitedRouter(t, mw, sessions)
	// Fresh addresses keep the per-address budget out of the way: the compat
	// session spends the account until it is refused.
	admitted := 0
	for i := range 64 {
		rec := serveFrom(router, fmt.Sprintf("203.0.113.%d", 100+i), http.MethodGet, "/SyncPlay/List", "", "one")
		if rec.Code == 429 {
			break
		}
		if rec.Code != 200 {
			t.Fatal(rec.Code)
		}
		admitted++
	}
	if admitted == 0 || admitted == 64 {
		t.Fatalf("compat session admitted %d requests before the account refused", admitted)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v2/account/me", nil)
	r = r.WithContext(apimw.SetClaims(clientip.SetContext(r.Context(), "203.0.113.2"), &auth.Claims{UserID: 42, TokenType: auth.TokenTypeAPIKey, APIKeyID: 7}))
	rec := httptest.NewRecorder()
	mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(rec, r)
	if rec.Code != 429 {
		t.Fatalf("native key after compat spent the account = %d, want 429", rec.Code)
	}
	if rec := serveFrom(router, "203.0.113.3", http.MethodGet, "/SyncPlay/List", "", "two"); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("shared budget response = %d, %s", rec.Code, rec.Body.String())
	}
	if rec := serveFrom(router, "203.0.113.4", http.MethodGet, "/SyncPlay/List", "", "other"); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}

type accountRecorder struct{ keys []string }

func (l *accountRecorder) Allow(_ context.Context, key string, _ ratelimit.Rate) ratelimit.AllowResult {
	l.keys = append(l.keys, key)
	return ratelimit.AllowResult{Allowed: true}
}
func (*accountRecorder) Close() {}

func TestCompatAPIKeyAccountRequiresCurrentEnabledOwner(t *testing.T) {
	for _, sessionPath := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("session=%v/enabled=%v", sessionPath, enabled), func(t *testing.T) {
				keys := &fakeAPIKeyValidator{key: &models.APIKey{ID: 7, UserID: 2, Key: "sa_test"}}
				users := &fakeAPIKeyUserLoader{user: &models.User{ID: 2, Role: "admin", Enabled: enabled}}
				_, provider, _ := adminKeyAuthWithPrimary(t, fixedNow)
				keyAuth := NewAdminAPIKeyAuthenticator(keys, users, provider, fixedNow)
				limiter := &accountRecorder{}
				mw := ratelimit.NewMiddleware(limiter, limiter, &rateLimitSettings{values: map[string]string{}}, false)
				if err := mw.Init(t.Context()); err != nil {
					t.Fatal(err)
				}
				wrap := keyAuth.RequireAdminAPIKey
				if sessionPath {
					wrap = RequireSessionOrAPIKeySession(NewAuthenticator(NewSessionStore(time.Hour, nil), nil), keyAuth)
				}
				probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if apimw.GetClaims(r.Context()) != nil {
						t.Fatal("compat must not grant native claims")
					}
					w.WriteHeader(204)
				})
				rec := httptest.NewRecorder()
				wrap(compatRateLimit(mw)(probe)).ServeHTTP(rec, apiKeyRequest())
				if enabled {
					if rec.Code != 204 || !slices.Equal(limiter.keys, []string{"account:2", "global"}) {
						t.Fatalf("status=%d, calls=%v", rec.Code, limiter.keys)
					}
				} else if rec.Code != 401 || len(limiter.keys) != 0 {
					t.Fatalf("disabled owner: status=%d, calls=%v", rec.Code, limiter.keys)
				}
			})
		}
	}
}

func TestPlaybackAccountExplicitGrantOnly(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		sessions, playback := seedDirectPlaySession(t, fixedNow)
		p, _ := playback.Get("ps1")
		p.ClientIP = "192.0.2.1"
		p.ClientPeer = "192.0.2.1"
		playback.PutNegotiated(*p)
		limiter := &accountRecorder{}
		mw := ratelimit.NewMiddleware(limiter, limiter, &rateLimitSettings{values: map[string]string{}}, false)
		if err := mw.Init(t.Context()); err != nil {
			t.Fatal(err)
		}
		router := chi.NewRouter()
		router.Use(clientip.Middleware(clientip.NewResolver(nil)))
		router.With(PlaybackSessionAuth(sessions, playback, nil), compatRateLimit(mw)).Get("/Videos/{id}/stream", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
		target := "/Videos/item123/stream?static=true&mediaSourceId=src9"
		if explicit {
			target += "&PlaySessionId=ps1"
		}
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.RemoteAddr = "192.0.2.1:4000"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		if rec.Code != 204 {
			t.Fatalf("explicit=%v, status=%d: %s", explicit, rec.Code, rec.Body.String())
		}
		if slices.Contains(limiter.keys, "account:7") != explicit {
			t.Fatalf("explicit=%v, calls=%v", explicit, limiter.keys)
		}
	}
}
