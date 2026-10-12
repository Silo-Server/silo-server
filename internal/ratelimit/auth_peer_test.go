package ratelimit

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// authLimitBackends returns the limiters the auth budgets run on: in-memory
// always, Redis when SILO_TEST_REDIS_URL names one.
func authLimitBackends() map[string]func(t *testing.T) RateLimiter {
	return map[string]func(t *testing.T) RateLimiter{
		"memory": func(t *testing.T) RateLimiter {
			l := NewMemoryLimiter()
			t.Cleanup(l.Close)
			return l
		},
		"redis": func(t *testing.T) RateLimiter {
			client := testAttemptRedisClient(t)
			if err := client.Ping(t.Context()).Err(); err != nil {
				t.Fatalf("ping redis: %v", err)
			}
			return NewRedisLimiter(client)
		},
	}
}

// loginThroughResolver sends POST /auth/login through the real client IP
// middleware with the default trusted proxy list, from the given transport
// peer and X-Forwarded-For value, and returns the status.
func loginThroughResolver(t *testing.T, mw *Middleware) func(peer, forwardedFor string) int {
	t.Helper()
	trusted, err := clientip.ParseCIDRs(clientip.DefaultTrustedProxies)
	if err != nil {
		t.Fatal(err)
	}
	handler := clientip.Middleware(clientip.NewResolver(trusted))(
		mw.AuthEndpointHandler("login")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})))
	return func(peer, forwardedFor string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.RemoteAddr = peer + ":40000"
		if forwardedFor != "" {
			req.Header.Set("X-Forwarded-For", forwardedFor)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
}

// randomPrivatePeer returns a trusted proxy address. Random addresses keep
// Redis counters, which outlive a run by up to two minutes, apart between runs.
func randomPrivatePeer() string {
	return fmt.Sprintf("10.%d.%d.%d", rand.IntN(256), rand.IntN(256), 1+rand.IntN(254))
}

// randomClientNet returns the first two octets of a random public /16 to draw
// client addresses from.
func randomClientNet() string {
	return fmt.Sprintf("%d.%d", 100+rand.IntN(26), rand.IntN(256))
}

// A client whose connection comes from a private address is a trusted proxy by
// default, so it can name a different client in X-Forwarded-For on every
// request. Each name gets a fresh per-client login budget; the peer's shared
// budget must still stop the run.
func TestAuthEndpointHandlerCapsForwardedForRotationPerPeer(t *testing.T) {
	for name, newLimiter := range authLimitBackends() {
		t.Run(name, func(t *testing.T) {
			mw := NewMiddleware(newLimiter(t), NewMemoryLimiter(), nil, name == "memory")
			t.Cleanup(mw.global.Close)
			mw.cfg = DefaultConfig()
			login := loginThroughResolver(t, mw)
			peer := randomPrivatePeer()
			clients := randomClientNet()

			allowed := 0
			const attempts = 600
			for i := range attempts {
				if login(peer, fmt.Sprintf("%s.%d.%d", clients, i/250, 1+i%250)) != http.StatusTooManyRequests {
					allowed++
				}
			}

			t.Logf("rotating X-Forwarded-For from one peer: %d of %d login attempts allowed", allowed, attempts)
			ep := mw.cfg.AuthEndpoints["login"]
			// Redis counts in fixed minute windows, so a run that straddles a
			// boundary may see two windows of the peer's per-minute budget.
			if limit := 2 * authPeerBudgetMultiplier * ep.RequestsPerMinute; allowed < ep.Burst || float64(allowed) > limit {
				t.Fatalf("allowed = %d, want between %d and %g", allowed, ep.Burst, limit)
			}
		})
	}
}

// One reverse proxy carries every user's sign-in. Each user keeps a full
// per-client login budget while the proxy's total stays within its own.
func TestAuthEndpointHandlerKeepsPerClientBudgetsBehindOneProxy(t *testing.T) {
	for name, newLimiter := range authLimitBackends() {
		t.Run(name, func(t *testing.T) {
			mw := NewMiddleware(newLimiter(t), NewMemoryLimiter(), nil, name == "memory")
			t.Cleanup(mw.global.Close)
			mw.cfg = DefaultConfig()
			login := loginThroughResolver(t, mw)
			proxy := randomPrivatePeer()
			clientNet := randomClientNet()
			ep := mw.cfg.AuthEndpoints["login"]

			for c := range authPeerBudgetMultiplier - 1 {
				client := fmt.Sprintf("%s.0.%d", clientNet, 10+c)
				for i := range ep.Burst {
					if code := login(proxy, client); code == http.StatusTooManyRequests {
						t.Fatalf("client %s attempt %d behind the proxy got 429", client, i+1)
					}
				}
			}
		})
	}
}

func TestAuthEndpointHandlerSpendsPeerBudgetOnlyForForwardedClients(t *testing.T) {
	tests := []struct {
		name    string
		peer    string
		client  string
		overlay bool
		want    []string
	}{
		{
			name:   "direct connection",
			peer:   "203.0.113.10:40000",
			client: "203.0.113.10",
			want:   []string{"ip:203.0.113.10", "authip:203.0.113.10:login"},
		},
		{
			name:   "direct IPv6 connection",
			peer:   "[2001:db8::10]:40000",
			client: "2001:db8::10",
			want:   []string{"ip:2001:db8::10", "authip:2001:db8::10:login"},
		},
		{
			name:   "direct connection with an unparsable peer",
			peer:   "[fe80::1%en0]:40000",
			client: "0.0.0.0",
			want:   []string{"ip:0.0.0.0", "authip:0.0.0.0:login"},
		},
		{
			name:   "forwarded by a proxy",
			peer:   "172.17.0.1:40000",
			client: "203.0.113.10",
			want:   []string{"ip:203.0.113.10", "authip:203.0.113.10:login", "authpeer:172.17.0.1:login"},
		},
		{
			name:    "forwarded by network access ingress",
			peer:    "127.0.0.1:40000",
			client:  "100.64.0.7",
			overlay: true,
			want:    []string{"ip:100.64.0.7", "authip:100.64.0.7:login"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			perKey := &recordingLimiter{}
			global := &recordingLimiter{}
			mw := NewMiddleware(perKey, global, nil, true)
			mw.cfg = DefaultConfig()
			handler := mw.AuthEndpointHandler("login")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
			ctx := clientip.SetContext(req.Context(), tt.client)
			ctx = clientip.SetPeerContext(ctx, tt.peer)
			if tt.overlay {
				ctx = netaccess.WithPath(ctx, netaccess.Path{Provider: "tailscale", InstallationID: 1})
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req.WithContext(ctx))

			if rec.Code != http.StatusNoContent {
				t.Fatalf("response = %d, want 204", rec.Code)
			}
			if !slices.Equal(perKey.keys, tt.want) {
				t.Fatalf("per-key limiter keys = %#v, want %#v", perKey.keys, tt.want)
			}
		})
	}
}

// A client over its own budget must not spend the budget it shares with the
// other clients behind its proxy, and a request the peer budget rejects must
// not spend the global one.
func TestAuthEndpointHandlerRejectionsDoNotSpendWiderBudgets(t *testing.T) {
	tests := []struct {
		denied string
		want   []string
	}{
		{denied: "authip:203.0.113.10:login", want: []string{"ip:203.0.113.10", "authip:203.0.113.10:login"}},
		{denied: "authpeer:172.17.0.1:login", want: []string{"ip:203.0.113.10", "authip:203.0.113.10:login", "authpeer:172.17.0.1:login"}},
	}
	for _, tt := range tests {
		t.Run(tt.denied, func(t *testing.T) {
			perKey := &recordingLimiter{denied: map[string]bool{tt.denied: true}}
			global := &recordingLimiter{}
			mw := NewMiddleware(perKey, global, nil, true)
			mw.cfg = DefaultConfig()
			handler := mw.AuthEndpointHandler("login")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("next handler called for rejected request")
			}))
			req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
			ctx := clientip.SetContext(req.Context(), "203.0.113.10")
			ctx = clientip.SetPeerContext(ctx, "172.17.0.1:40000")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req.WithContext(ctx))

			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("response = %d, want 429", rec.Code)
			}
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
			if !slices.Equal(perKey.keys, tt.want) {
				t.Fatalf("per-key limiter keys = %#v, want %#v", perKey.keys, tt.want)
			}
			if len(global.keys) != 0 {
				t.Fatalf("global limiter keys = %#v, want no consumed budget", global.keys)
			}
		})
	}
}

func TestAuthPeerRateStaysWithinLimiterBounds(t *testing.T) {
	got := authPeerRate(Rate{RequestsPerSecond: 1.0 / 3, RequestsPerMinute: 20, Burst: 10})
	if got.RequestsPerMinute != 200 || got.Burst != 100 {
		t.Fatalf("login peer rate = %+v, want 200 per minute, burst 100", got)
	}
	got = authPeerRate(Rate{RequestsPerSecond: MaxRequestsPerWindow, RequestsPerMinute: MaxRequestsPerWindow, Burst: MaxBurst})
	if got.RequestsPerSecond > MaxRequestsPerWindow || got.RequestsPerMinute > MaxRequestsPerWindow || got.Burst <= 0 || got.Burst > MaxBurst {
		t.Fatalf("peer rate for maximal settings = %+v, want values within the limiter's bounds", got)
	}
}
