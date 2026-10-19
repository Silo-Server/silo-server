package ratelimit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

func accountRequest(account int, tokenType, ip string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v2/account/me", nil)
	ctx := clientip.SetContext(r.Context(), ip)
	return r.WithContext(apimw.SetClaims(ctx, &auth.Claims{UserID: account, TokenType: tokenType, APIKeyID: 7}))
}

func TestAccountDefaultRateAndRejectionOrder(t *testing.T) {
	for _, named := range []bool{false, true} {
		for _, denied := range []string{"", "ip:203.0.113.1", "key:7", "account:42", "global"} {
			t.Run(fmt.Sprintf("named=%v/denied=%s", named, denied), func(t *testing.T) {
				// One recorder proves order across both limiter instances. No high-volume traffic.
				limiter := &recordingLimiter{denied: map[string]bool{denied: true}}
				mw := NewMiddleware(limiter, limiter, nil, true)
				mw.cfg = DefaultConfig()
				wrap := mw.Handler
				want := []string{"ip:203.0.113.1", "key:7", "account:42", "global"}
				if named {
					wrap = mw.AuthEndpointHandler("device_lookup")
					want = []string{"ip:203.0.113.1", "authip:203.0.113.1:device_lookup", "account:42", "global"}
				}
				if index := slices.Index(want, denied); index >= 0 {
					want = want[:index+1]
				}
				rec := httptest.NewRecorder()
				wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(rec, accountRequest(42, auth.TokenTypeAPIKey, "203.0.113.1"))
				if !slices.Equal(limiter.keys, want) {
					t.Fatalf("calls = %v, want %v", limiter.keys, want)
				}
				if index := slices.Index(limiter.keys, "account:42"); index >= 0 {
					if got := limiter.rates[index]; got != (Rate{RequestsPerSecond: 120, RequestsPerMinute: 6000, Burst: 120}) {
						t.Fatalf("default account rate = %+v", got)
					}
				}
				wantCode := http.StatusNoContent
				if slices.Contains(want, denied) {
					wantCode = http.StatusTooManyRequests
				}
				if rec.Code != wantCode || (wantCode == 429 && rec.Header().Get("Retry-After") == "") {
					t.Fatalf("response = %d, headers = %v", rec.Code, rec.Header())
				}
			})
		}
	}
}

func TestAccountIdentityAndPreAuth(t *testing.T) {
	for _, named := range []bool{false, true} {
		for _, tokenType := range []string{auth.TokenTypeAccess, auth.TokenTypeAPIKey, auth.TokenTypeDirectDownloadLink, auth.TokenTypeApplePushDisplay, auth.TokenTypePluginAccess} {
			for _, account := range []int{-1, 0, 42} {
				t.Run(fmt.Sprintf("named=%v/type=%s/account=%d", named, tokenType, account), func(t *testing.T) {
					limiter := &recordingLimiter{}
					mw := NewMiddleware(limiter, limiter, nil, true)
					mw.cfg = DefaultConfig()
					wrap := mw.Handler
					if named {
						wrap = mw.AuthEndpointHandler("device_lookup")
					}
					r := accountRequest(account, tokenType, "") // identity is checked even without a resolved IP
					rec := httptest.NewRecorder()
					wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(rec, r)
					if got := slices.Contains(limiter.keys, "account:42"); got != (account == 42) {
						t.Fatalf("calls = %v", limiter.keys)
					}
					for _, key := range limiter.keys {
						if key == "account:0" || key == "account:-1" {
							t.Fatalf("invalid identity charged: %s", key)
						}
					}
				})
			}
		}
		limiter := &recordingLimiter{}
		mw := NewMiddleware(limiter, limiter, nil, true)
		mw.cfg = DefaultConfig()
		r := httptest.NewRequest(http.MethodGet, "/device/lookup", nil)
		r = r.WithContext(clientip.SetContext(r.Context(), "203.0.113.1"))
		mw.AuthEndpointHandler("device_lookup")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(httptest.NewRecorder(), r)
		if !slices.Equal(limiter.keys, []string{"ip:203.0.113.1", "authip:203.0.113.1:device_lookup", "global"}) {
			t.Fatalf("pre-auth calls = %v", limiter.keys)
		}
	}
}

func TestAccountAllowanceSharesCredentialsAndSeparatesHouseholds(t *testing.T) {
	perKey, global := NewMemoryLimiter(), NewMemoryLimiter()
	t.Cleanup(perKey.Close)
	t.Cleanup(global.Close)
	mw := NewMiddleware(perKey, global, nil, true)
	mw.cfg = DefaultConfig()
	mw.cfg.IPReqPerSecond, mw.cfg.IPReqPerMinute, mw.cfg.IPBurst = .001, 2, 2
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	for i, account := range []int{42, 42, 42, 42, 43, 43, 43} {
		r := accountRequest(account, auth.TokenTypeAccess, fmt.Sprintf("203.0.113.%d", i+1))
		claims := apimw.GetClaims(r.Context())
		claims.SessionID = fmt.Sprintf("session-%d", i)
		claims.ProfileID = fmt.Sprintf("profile-%d", i)
		r.Header.Set("X-Device-Id", fmt.Sprintf("device-%d", i))
		if i%2 != 0 {
			claims.TokenType, claims.APIKeyID = auth.TokenTypeAPIKey, int64(i+1)
		}
		wrap := mw.Handler
		if i == 2 {
			wrap = mw.AuthEndpointHandler("device_lookup")
		}
		rec := httptest.NewRecorder()
		wrap(next).ServeHTTP(rec, r)
		want := 204
		if i == 2 || i == 3 || i == 6 {
			want = 429
		}
		if rec.Code != want {
			t.Fatalf("request %d (account %d): status = %d, want %d", i, account, rec.Code, want)
		}
	}
}

func TestAccountAdapterDoesNotGrantNativeAuthority(t *testing.T) {
	ctx := WithAccount(t.Context(), 42)
	if accountFromContext(ctx) != 42 || apimw.GetClaims(ctx) != nil {
		t.Fatal("adapter identity or native authority changed")
	}
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 43})
	if accountFromContext(ctx) != 43 {
		t.Fatal("native identity must take precedence")
	}
}
