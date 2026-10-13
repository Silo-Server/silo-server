package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type rateLimitSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *rateLimitSettings) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}

func (s *rateLimitSettings) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *rateLimitSettings) GetAll(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out, nil
}

// newTestRateLimit builds the native in-memory rate limiter with the given
// server_settings overrides on top of the seeded defaults.
func newTestRateLimit(t *testing.T, overrides map[string]string) *ratelimit.Middleware {
	t.Helper()
	perKey, global := ratelimit.NewMemoryLimiter(), ratelimit.NewMemoryLimiter()
	t.Cleanup(perKey.Close)
	t.Cleanup(global.Close)
	values := map[string]string{}
	for k, v := range overrides {
		values[k] = v
	}
	mw := ratelimit.NewMiddleware(perKey, global, &rateLimitSettings{values: values}, true)
	if err := mw.Init(t.Context()); err != nil {
		t.Fatalf("init rate limiter: %v", err)
	}
	return mw
}

func newRateLimitedRouter(t *testing.T, mw *ratelimit.Middleware, sessions *SessionStore) http.Handler {
	t.Helper()
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	return NewRouter(Dependencies{
		Config:           cfg,
		ClientIPResolver: clientip.NewResolver(nil),
		LoginResolver:    activityLoginResolver{},
		SessionStore:     sessions,
		RateLimit:        mw,
	})
}

func serveFrom(router http.Handler, ip, method, target, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = ip + ":40000"
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func signInFrom(router http.Handler, ip, password string) *httptest.ResponseRecorder {
	return serveFrom(router, ip, http.MethodPost, "/Users/AuthenticateByName",
		`{"Username":"alice","Pw":"`+password+`"}`, "")
}

// TestRouterRateLimitsSignInPerClientIP pins that Jellyfin password sign-in
// spends the native login budget per client IP, rejects with a Jellyfin-style
// 429 and Retry-After once it is spent, leaves other clients unaffected, and
// shares its counter with the native login route.
func TestRouterRateLimitsSignInPerClientIP(t *testing.T) {
	mw := newTestRateLimit(t, map[string]string{
		"ratelimit.auth.login.requests_per_minute": "2",
		"ratelimit.auth.login.burst":               "2",
	})
	router := newRateLimitedRouter(t, mw, nil)
	const attacker, other = "203.0.113.10", "198.51.100.20"

	for i := range 2 {
		if rec := signInFrom(router, attacker, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401 within the budget", i+1, rec.Code)
		}
	}
	rec := signInFrom(router, attacker, "right")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt over budget: status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 response has no Retry-After header")
	}
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 429 body %q: %v", rec.Body.String(), err)
	}
	if body.Error != "TooManyRequests" || body.Message == "" {
		t.Fatalf("429 body = %+v, want the compat error envelope", body)
	}

	if rec := signInFrom(router, other, "right"); rec.Code != http.StatusOK {
		t.Fatalf("other client sign-in: status = %d, want 200", rec.Code)
	}

	// The native login route draws on the same per-IP login counter.
	native := mw.AuthEndpointHandler("login")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req = req.WithContext(clientip.SetContext(req.Context(), attacker))
	nativeRec := httptest.NewRecorder()
	native.ServeHTTP(nativeRec, req)
	if nativeRec.Code != http.StatusTooManyRequests {
		t.Fatalf("native login after compat attempts: status = %d, want 429", nativeRec.Code)
	}
}

// TestRouterWithoutRateLimitLeavesSignInUnlimited pins that a nil limiter,
// which is what rate_limit.enabled=false wires, keeps today's behavior.
func TestRouterWithoutRateLimitLeavesSignInUnlimited(t *testing.T) {
	router := newRateLimitedRouter(t, nil, nil)
	for i := range 50 {
		if rec := signInFrom(router, "203.0.113.10", "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}
	if rec := signInFrom(router, "203.0.113.10", "right"); rec.Code != http.StatusOK {
		t.Fatalf("sign-in: status = %d, want 200", rec.Code)
	}
}

// TestRouterRateLimitCoversAuthenticatedAndMediaRoutesOnly pins the route
// split: anonymous artwork, avatars, web assets, and public server reads never
// spend the per-IP budget, while authenticated API and media routes do, as on
// the native API.
func TestRouterRateLimitCoversAuthenticatedAndMediaRoutesOnly(t *testing.T) {
	mw := newTestRateLimit(t, map[string]string{
		"ratelimit.ip.requests_per_second": "0.01",
		"ratelimit.ip.requests_per_minute": "1",
		"ratelimit.ip.burst":               "1",
	})
	sessions := NewSessionStore(time.Hour, nil)
	if err := sessions.Put(Session{Token: "compat-token", Username: "alice", StreamAppUserID: 42, ProfileID: "p1", PseudoUserID: PseudoUserID(42, "p1")}); err != nil {
		t.Fatalf("put session: %v", err)
	}
	router := newRateLimitedRouter(t, mw, sessions)
	const ip = "203.0.113.10"

	for _, target := range []string{
		"/System/Info/Public",
		"/System/Ping",
		"/QuickConnect/Enabled",
		"/Items/abc/Images/Primary",
		"/Items/abc/Images/Backdrop/0",
		"/Users/abc/Images/Primary",
		"/UserImage?userId=abc",
		"/api/v2/artwork/x",
		"/web/index.html",
	} {
		for range 3 {
			if rec := serveFrom(router, ip, http.MethodGet, target, "", ""); rec.Code == http.StatusTooManyRequests {
				t.Fatalf("GET %s was rate limited; anonymous routes stay exempt", target)
			}
		}
	}

	if rec := serveFrom(router, ip, http.MethodGet, "/SyncPlay/List", "", "compat-token"); rec.Code != http.StatusOK {
		t.Fatalf("first authenticated request: status = %d, want 200", rec.Code)
	}
	if rec := serveFrom(router, ip, http.MethodGet, "/SyncPlay/List", "", "compat-token"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("authenticated request over the per-IP budget: status = %d, want 429", rec.Code)
	}
	if rec := serveFrom(router, ip, http.MethodGet, "/Videos/abc/stream", "", "compat-token"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("media request over the per-IP budget: status = %d, want 429", rec.Code)
	}
	// An unauthenticated request is refused by auth before it reaches the
	// limiter, matching the native authenticated group.
	if rec := serveFrom(router, ip, http.MethodGet, "/SyncPlay/List", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request: status = %d, want 401", rec.Code)
	}
}
