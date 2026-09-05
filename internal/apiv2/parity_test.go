package apiv2

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

// The fakes below stand in for the stores the real gates read; the gates
// themselves are the production middleware from internal/api/middleware.

type fakeTokens struct{ claims map[string]*auth.Claims }

func (f fakeTokens) ValidateToken(tok string) (*auth.Claims, error) {
	if c, ok := f.claims[tok]; ok {
		return c, nil
	}
	return nil, errors.New("bad token")
}

type fakeSessions struct{ valid map[string]bool }

func (f fakeSessions) IsValid(_ context.Context, id string) (bool, error) { return f.valid[id], nil }

type fakeUsers struct{ users map[int]*models.User }

func (f fakeUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return nil, errors.New("no user")
}

type fakeResolver struct{}

// Resolve mimics the production resolver's outcomes: no declared profile
// resolves to the account scope; an unknown profile is not found; a locked
// profile without a token is unverified.
func (fakeResolver) Resolve(_ context.Context, in access.ResolveInput) (access.Scope, error) {
	switch in.ProfileID {
	case "":
		return access.Scope{UserID: in.UserID, ProfileVerified: true}, nil
	case "p-owner", "p-primary":
		return access.Scope{UserID: in.UserID, ProfileID: in.ProfileID, ProfileVerified: true}, nil
	case "p-locked", "p-primary-locked":
		// An API-key credential is exempt from the PIN at the gate, and the
		// scope records that the verification was skipped rather than proved.
		if in.SkipPINVerification {
			return access.Scope{UserID: in.UserID, ProfileID: in.ProfileID, ProfileVerified: true, PINVerificationSkipped: true}, nil
		}
		if in.ProfileToken == "" {
			return access.Scope{}, access.ErrProfileUnverified
		}
		return access.Scope{UserID: in.UserID, ProfileID: in.ProfileID, ProfileVerified: true}, nil
	}
	return access.Scope{}, access.ErrProfileNotFound
}

type fakeSettings struct{ demo bool }

func (f fakeSettings) Get(_ context.Context, key string) (string, error) {
	if key == "demo.enabled" && f.demo {
		return "true", nil
	}
	return "", nil
}
func (f fakeSettings) Set(context.Context, string, string) error { return nil }
func (f fakeSettings) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{"ratelimit.ip.requests_per_second": "1", "ratelimit.ip.requests_per_minute": "1", "ratelimit.ip.burst": "1"}, nil
}

const (
	memberToken = "tok-member"
	adminToken  = "tok-admin"
	// otherAdminToken is a second admin account, for cursors that must not
	// cross accounts.
	otherAdminToken = "tok-admin-other"
	expiredToken    = "tok-expired"
	// impersonatedToken is member 1's session opened by admin 2.
	impersonatedToken = "tok-impersonated"
	// apiKeyToken is an unscoped API key owned by the member account: no
	// login session, exempt from profile PIN verification at the gate.
	apiKeyToken = "sa_member"
)

type fakeAPIKeys struct{ keys map[string]*models.APIKey }

func (f fakeAPIKeys) GetByKey(_ context.Context, key string) (*models.APIKey, error) {
	if k, ok := f.keys[key]; ok {
		return k, nil
	}
	return nil, errors.New("no key")
}
func (f fakeAPIKeys) UpdateLastUsed(context.Context, int64) error { return nil }

func fakeAuth(users map[int]*models.User) *apimw.AuthMiddleware {
	claims := map[string]*auth.Claims{
		memberToken:       {UserID: 1, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess},
		adminToken:        {UserID: 2, Role: "admin", SessionID: "s2", TokenType: auth.TokenTypeAccess},
		otherAdminToken:   {UserID: 3, Role: "admin", SessionID: "s3", TokenType: auth.TokenTypeAccess},
		expiredToken:      {UserID: 1, Role: "user", SessionID: "s-gone", TokenType: auth.TokenTypeAccess},
		impersonatedToken: {UserID: 1, Role: "user", SessionID: "s4", TokenType: auth.TokenTypeAccess, ImpersonatorUserID: ptr(2)},
	}
	keys := fakeAPIKeys{map[string]*models.APIKey{apiKeyToken: {ID: 7, UserID: 1}}}
	return apimw.NewAuthMiddleware(fakeTokens{claims}, fakeSessions{map[string]bool{"s1": true, "s2": true, "s3": true, "s4": true}}, keys, fakeUsers{users})
}

func parityDeps(demo bool) Dependencies {
	users := map[int]*models.User{
		1: {ID: 1, Role: "user", Enabled: true, Permissions: []string{}},
		2: {ID: 2, Role: "admin", Enabled: true},
		3: {ID: 3, Role: "admin", Enabled: true},
	}
	primary := func(_ context.Context, userID int, profileID string) (bool, bool, error) {
		switch profileID {
		case "p-primary", "p-primary-locked":
			return true, userID == 2, nil
		case "p-owner":
			return false, true, nil
		}
		return false, false, nil
	}
	return Dependencies{
		Auth:         fakeAuth(users),
		ViewerAccess: apimw.NewViewerAccessMiddleware(fakeResolver{}),
		ActingAdmin:  apimw.RequireActingAdmin(primary),
		PermissionGates: map[string]func(http.Handler) http.Handler{
			"marker_edit": apimw.NewPermissionMiddleware(fakeUsers{users}, nil, primary).RequireMarkerEdit,
		},
		DemoSettings: fakeSettings{demo: demo},
	}
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func with(h map[string]string, k, v string) map[string]string {
	out := map[string]string{}
	for kk, vv := range h {
		out[kk] = vv
	}
	out[k] = v
	return out
}

// TestMiddlewareParity drives every operation class through the real router
// with the production gates and checks each denial's problem type and status.
func TestMiddlewareParity(t *testing.T) {
	h := newTestHandler(t, parityDeps(false))
	body := `{"name":"x","cleared":null}`
	cases := []struct {
		name    string
		class   Class
		headers map[string]string
		want    ProblemType
		ok      bool
	}{
		{"public: no credential", ClassPublic, nil, ProblemType{}, true},
		{"authenticated: no credential", ClassAuthenticated, nil, TypeAuthenticationRequired, false},
		{"authenticated: bad token", ClassAuthenticated, bearer("nope"), TypeInvalidToken, false},
		{"authenticated: expired session", ClassAuthenticated, bearer(expiredToken), TypeSessionExpired, false},
		{"authenticated: ok", ClassAuthenticated, bearer(memberToken), ProblemType{}, true},
		{"profile: missing header", ClassProfileScoped, bearer(memberToken), TypeValidationFailed, false},
		{"profile: wrong account", ClassProfileScoped, with(bearer(memberToken), "X-Profile-Id", "p-other"), TypeNotFound, false},
		{"profile: locked", ClassProfileScoped, with(bearer(memberToken), "X-Profile-Id", "p-locked"), TypeProfileVerificationRequired, false},
		{"profile: unlocked with token", ClassProfileScoped, with(with(bearer(memberToken), "X-Profile-Id", "p-locked"), "X-Profile-Token", "t"), ProblemType{}, true},
		{"profile: ok", ClassProfileScoped, with(bearer(memberToken), "X-Profile-Id", "p-owner"), ProblemType{}, true},
		// An API key is exempt from the profile PIN at the gate, in v1 and v2
		// alike; the household verifier, not the gate, refuses it (see
		// TestUpdateProfileDecisions).
		{"profile: locked with api key", ClassProfileScoped, with(bearer(apiKeyToken), "X-Profile-Id", "p-locked"), ProblemType{}, true},
		{"authenticated: api key", ClassAuthenticated, bearer(apiKeyToken), ProblemType{}, true},
		{"acting admin: member", ClassActingAdmin, bearer(memberToken), TypePermissionDenied, false},
		{"acting admin: non-primary profile", ClassActingAdmin, with(bearer(adminToken), "X-Profile-Id", "p-owner"), TypePermissionDenied, false},
		{"acting admin: primary", ClassActingAdmin, with(bearer(adminToken), "X-Profile-Id", "p-primary"), ProblemType{}, true},
		{"acting admin: unauthenticated", ClassActingAdmin, nil, TypeAuthenticationRequired, false},
		// Viewer access runs before the acting-admin gate, exactly as in the
		// v1 authenticated group: a locked profile is refused before the
		// admin check, and an unknown one is the viewer gate's 404.
		{"acting admin: locked primary profile", ClassActingAdmin, with(bearer(adminToken), "X-Profile-Id", "p-primary-locked"), TypeProfileVerificationRequired, false},
		{"acting admin: unlocked primary profile", ClassActingAdmin, with(with(bearer(adminToken), "X-Profile-Id", "p-primary-locked"), "X-Profile-Token", "t"), ProblemType{}, true},
		{"acting admin: unknown profile", ClassActingAdmin, with(bearer(adminToken), "X-Profile-Id", "p-other"), TypeNotFound, false},
		{"permission: member without it", ClassPermissionGated, bearer(memberToken), TypePermissionDenied, false},
		{"permission: admin", ClassPermissionGated, bearer(adminToken), ProblemType{}, true},
		{"permission: locked primary profile", ClassPermissionGated, with(bearer(adminToken), "X-Profile-Id", "p-primary-locked"), TypeProfileVerificationRequired, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/api/v2/probe/"+string(tc.class), body, tc.headers)
			if tc.ok {
				if rec.Code != 200 {
					t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
				return
			}
			p := requireProblem(t, rec, tc.want)
			if tc.want == TypeValidationFailed && (len(p.Errors) != 1 || p.Errors[0].Location != "header.x-profile-id") {
				t.Fatalf("profile header error: %+v", p.Errors)
			}
		})
	}
}

// TestHandlersReadIdentityFromContext: claims, profile and viewer scope reach
// the handler through the context the gates set, not through headers.
func TestHandlersReadIdentityFromContext(t *testing.T) {
	h := newTestHandler(t, parityDeps(false))
	rec := do(t, h, http.MethodPost, "/api/v2/probe/profile_scoped", `{"name":"x","cleared":null}`, with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	for _, want := range []string{`"user_id":1`, `"profile_id":"p-owner"`, `"has_scope":true`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("missing %s in %s", want, rec.Body.String())
		}
	}
	// A profile header on a public operation is not an identity.
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null}`, with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if !strings.Contains(rec.Body.String(), `"user_id":0`) || !strings.Contains(rec.Body.String(), `"profile_id":""`) || !strings.Contains(rec.Body.String(), `"has_scope":false`) {
		t.Fatalf("public op saw an identity: %s", rec.Body.String())
	}
}

func TestDemoModeDenial(t *testing.T) {
	h := newTestHandler(t, parityDeps(true))
	body := `{"name":"x","cleared":null}`
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/probe/authenticated", body, bearer(memberToken)), TypePermissionDenied)
	if rec := do(t, h, http.MethodPost, "/api/v2/probe/authenticated", body, bearer(adminToken)); rec.Code != 200 {
		t.Fatalf("admin blocked in demo mode: %d %s", rec.Code, rec.Body.String())
	}
	off := newTestHandler(t, parityDeps(false))
	if rec := do(t, off, http.MethodPost, "/api/v2/probe/authenticated", body, bearer(memberToken)); rec.Code != 200 {
		t.Fatalf("demo off: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRateLimitExceeded(t *testing.T) {
	deps := parityDeps(false)
	mw := ratelimit.NewMiddleware(ratelimit.NewMemoryLimiter(), ratelimit.NewMemoryLimiter(), fakeSettings{}, true)
	if err := mw.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The limiter keys on the resolved client IP the API listener's clientip
	// middleware stores; stand in for it here.
	deps.RateLimit = func(next http.Handler) http.Handler {
		return mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, r) }))
	}
	h := newTestHandler(t, deps)
	body := `{"name":"x","cleared":null}`
	var last ProblemType
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest(http.MethodPost, "/api/v2/probe/authenticated", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+memberToken)
		r = r.WithContext(clientip.SetContext(r.Context(), "203.0.113.9"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code == http.StatusTooManyRequests {
			requireProblem(t, rec, TypeRateLimited)
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("no Retry-After on 429")
			}
			if rec.Header().Get("X-RateLimit-Limit") != "" {
				t.Fatal("legacy X-RateLimit-* field on a v2 problem")
			}
			last = TypeRateLimited
			break
		}
	}
	if last != TypeRateLimited {
		t.Fatal("limiter never fired")
	}
}

// TestGateOrderMatchesV1 pins the order the v1 authenticated group runs its
// gates in: auth, demo, rate limit, viewer access, class gate. Demo mode
// denies before the limiter, so a refused request never spends budget — four
// refusals under a 1 rps / burst 1 limiter are all 403, never 429.
func TestGateOrderMatchesV1(t *testing.T) {
	deps := parityDeps(true)
	mw := ratelimit.NewMiddleware(ratelimit.NewMemoryLimiter(), ratelimit.NewMemoryLimiter(), fakeSettings{}, true)
	if err := mw.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	deps.RateLimit = mw.Handler
	h := newTestHandler(t, deps)
	for i := 0; i < 4; i++ {
		r := httptest.NewRequest(http.MethodPost, "/api/v2/probe/authenticated", strings.NewReader(`{"name":"x","cleared":null}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+memberToken)
		r = r.WithContext(clientip.SetContext(r.Context(), "203.0.113.10"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("request %d: status %d, want 403 (demo denies before the limiter): %s", i, rec.Code, rec.Body.String())
		}
		requireProblem(t, rec, TypePermissionDenied)
	}
	// The chain itself, so a reordering that the demo probe cannot see still fails.
	chain, missing := gateChain(deps, ClassProfileScoped, "", true, false)
	if missing != "" || len(chain) != 5 {
		t.Fatalf("profile-scoped chain = %d gates, missing %q; want auth, demo, rate limit, viewer access, profile", len(chain), missing)
	}
}

// TestGatesFailClosedWithoutViewerAccess: every class v1 runs viewer access
// for fails closed when it is unwired, rather than serving the operation.
func TestGatesFailClosedWithoutViewerAccess(t *testing.T) {
	deps := parityDeps(false)
	deps.ViewerAccess = nil
	h := newTestHandler(t, deps)
	for _, class := range []Class{ClassProfileScoped, ClassActingAdmin, ClassPermissionGated} {
		rec := do(t, h, http.MethodPost, "/api/v2/probe/"+string(class), `{"name":"x","cleared":null}`, bearer(adminToken))
		requireProblem(t, rec, TypeDependencyUnavailable)
	}
}
