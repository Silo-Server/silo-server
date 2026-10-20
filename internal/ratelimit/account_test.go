package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
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
					if got := limiter.rates[index]; got != (Rate{RequestsPerSecond: 480, RequestsPerMinute: 24000, Burst: 480}) {
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
	mw.cfg.IPReqPerSecond, mw.cfg.IPReqPerMinute, mw.cfg.IPBurst = .001, 1, 1
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	// Each request uses a fresh address, so only the account budget, sized at
	// accountBudgetMultiplier one-request addresses, can refuse it.
	accounts := append(slices.Repeat([]int{42}, accountBudgetMultiplier+2), slices.Repeat([]int{43}, accountBudgetMultiplier+1)...)
	spent := map[int]int{}
	for i, account := range accounts {
		r := accountRequest(account, auth.TokenTypeAccess, fmt.Sprintf("203.0.113.%d", i+1))
		claims := apimw.GetClaims(r.Context())
		claims.SessionID = fmt.Sprintf("session-%d", i)
		claims.ProfileID = fmt.Sprintf("profile-%d", i)
		if i == 0 {
			claims.SessionPurpose = models.SessionPurposeRemotePlayback
		}
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
		spent[account]++
		want := 204
		if spent[account] > accountBudgetMultiplier {
			want = 429
		}
		if rec.Code != want {
			t.Fatalf("request %d (account %d): status = %d, want %d", i, account, rec.Code, want)
		}
	}
}

func TestAccountRateStaysWithinLimiterBounds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPReqPerSecond, cfg.IPReqPerMinute, cfg.IPBurst = MaxRequestsPerWindow, MaxRequestsPerWindow, MaxBurst
	if got := accountRate(cfg); got.RequestsPerSecond > MaxRequestsPerWindow || got.RequestsPerMinute > MaxRequestsPerWindow || got.Burst > MaxBurst {
		t.Fatalf("account rate %+v exceeds limiter bounds", got)
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

type accountKeyStore struct{ owner int }

func (s *accountKeyStore) GetByKey(_ context.Context, token string) (*models.APIKey, error) {
	if token != "sa_test" {
		return nil, auth.ErrAPIKeyNotFound
	}
	return &models.APIKey{ID: 7, UserID: s.owner}, nil
}
func (*accountKeyStore) UpdateLastUsed(context.Context, int64) error { return nil }

type accountOwnerStore struct{ enabled bool }

func (s *accountOwnerStore) GetByID(_ context.Context, id int) (*models.User, error) {
	return &models.User{ID: id, Role: models.RoleUser, Enabled: s.enabled}, nil
}

func TestAccountLimiterUsesCurrentNativeAPIKeyOwner(t *testing.T) {
	keys, owners := &accountKeyStore{owner: 42}, &accountOwnerStore{enabled: true}
	authn := apimw.NewAuthMiddleware(nil, nil, keys, owners)
	limiter := &recordingLimiter{}
	mw := NewMiddleware(limiter, limiter, nil, true)
	mw.cfg = DefaultConfig()
	h := authn.RequireAuth(mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })))
	send := func(token string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/v2/account/me", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	for _, owner := range []int{42, 43} {
		keys.owner = owner
		limiter.keys = nil
		if code := send("sa_test"); code != 204 {
			t.Fatal(code)
		}
		if want := []string{"key:7", fmt.Sprintf("account:%d", owner), "global"}; !slices.Equal(limiter.keys, want) {
			t.Fatalf("calls = %v, want %v", limiter.keys, want)
		}
	}
	owners.enabled = false
	for _, token := range []string{"sa_test", "sa_unknown"} {
		limiter.keys = nil
		if code := send(token); code != 401 || len(limiter.keys) != 0 {
			t.Fatalf("invalid/disabled key: status=%d, calls=%v", code, limiter.keys)
		}
	}
}
