package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

const streamAuthTestUserRole = "user"

// stubTokenValidator returns preconfigured claims/err for any token string.
type stubTokenValidator struct {
	claims *auth.Claims
	err    error
}

func (s stubTokenValidator) ValidateToken(string) (*auth.Claims, error) {
	return s.claims, s.err
}

// stubSessionValidator reports a fixed validity for any session.
type stubSessionValidator struct {
	valid bool
	err   error
}

func (s stubSessionValidator) IsValid(context.Context, string) (bool, error) {
	return s.valid, s.err
}

// probeHandler records that it ran and the identity it saw in context.
type probeHandler struct {
	ran       bool
	userID    int
	profileID string
}

func (p *probeHandler) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	p.ran = true
	p.userID = GetUserID(r.Context())
	p.profileID = GetProfileID(r.Context())
}

// allowStreamToken is a StreamTokenAuthenticator that authenticates every
// request as the given user/profile. Used to prove the ?st= fallback path.
func allowStreamToken(userID int, profileID string) StreamTokenAuthenticator {
	return func(*http.Request) (*auth.Claims, string, bool) {
		return &auth.Claims{UserID: userID, SessionID: "sess-1", TokenType: auth.TokenTypeAccess}, profileID, true
	}
}

// denyStreamToken is a StreamTokenAuthenticator that never authenticates,
// modeling an absent / invalid / expired / wrong-session ?st= token.
func denyStreamToken() StreamTokenAuthenticator {
	return func(*http.Request) (*auth.Claims, string, bool) {
		return nil, "", false
	}
}

func TestRequireStreamAuth_ValidStreamTokenAloneAuthenticates(t *testing.T) {
	// A bearer path that would REJECT everything, to prove the ?st= fallback
	// authenticated the request rather than any bearer credential.
	am := NewAuthMiddleware(stubTokenValidator{err: errors.New("no bearer")}, stubSessionValidator{}, nil, nil)
	probe := &probeHandler{}
	h := am.RequireStreamAuth(allowStreamToken(42, "profile-7"))(probe)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-1?st=tok", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !probe.ran {
		t.Fatalf("handler did not run; status=%d body=%s", rr.Code, rr.Body.String())
	}
	if probe.userID != 42 {
		t.Fatalf("context user id = %d, want 42", probe.userID)
	}
	if probe.profileID != "profile-7" {
		t.Fatalf("context profile id = %q, want profile-7", probe.profileID)
	}
}

func TestRequireStreamAuth_InvalidStreamTokenReturns401(t *testing.T) {
	// Models an expired ?st= or a ?st= whose session id != the path session:
	// the authenticator returns ok=false, and there is no bearer credential.
	am := NewAuthMiddleware(stubTokenValidator{err: errors.New("no bearer")}, stubSessionValidator{}, nil, nil)
	probe := &probeHandler{}
	h := am.RequireStreamAuth(denyStreamToken())(probe)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-1?st=expired", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if probe.ran {
		t.Fatal("handler ran; want 401 without invoking handler")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestRequireStreamAuth_NoCredentialsReturns401(t *testing.T) {
	am := NewAuthMiddleware(stubTokenValidator{err: errors.New("no bearer")}, stubSessionValidator{}, nil, nil)
	probe := &probeHandler{}
	h := am.RequireStreamAuth(denyStreamToken())(probe)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-1", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if probe.ran {
		t.Fatal("handler ran; want 401")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestRequireStreamAuth_ValidBearerTakesPrecedenceOverStreamToken(t *testing.T) {
	// Bearer resolves to user 100; the ?st= authenticator would resolve a
	// DIFFERENT user (7). The bearer identity must win.
	am := NewAuthMiddleware(
		stubTokenValidator{claims: &auth.Claims{UserID: 100, SessionID: "s", Role: streamAuthTestUserRole, TokenType: auth.TokenTypeAccess}},
		stubSessionValidator{valid: true},
		nil, nil,
	)
	probe := &probeHandler{}
	h := am.RequireStreamAuth(allowStreamToken(7, "profile-other"))(probe)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-1?st=tok", nil)
	req.Header.Set("Authorization", "Bearer valid.jwt.token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !probe.ran {
		t.Fatalf("handler did not run; status=%d body=%s", rr.Code, rr.Body.String())
	}
	if probe.userID != 100 {
		t.Fatalf("context user id = %d, want 100 (bearer precedence)", probe.userID)
	}
}

func TestRequireStreamAuth_InvalidBearerDoesNotFallThroughToStreamToken(t *testing.T) {
	// A present-but-invalid bearer must 401 outright: it must never fall back
	// to the ?st= path even when a valid ?st= token is supplied.
	am := NewAuthMiddleware(
		stubTokenValidator{err: errors.New("invalid or expired token")},
		stubSessionValidator{valid: true},
		nil, nil,
	)
	probe := &probeHandler{}
	h := am.RequireStreamAuth(allowStreamToken(7, "profile-7"))(probe)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-1?st=tok", nil)
	req.Header.Set("Authorization", "Bearer tampered.jwt.token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if probe.ran {
		t.Fatal("handler ran; an invalid bearer must not fall through to ?st=")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// TestRequireAuth_RejectsStreamTokenOnly proves the widening is scoped: the
// global RequireAuth (used by every non-stream route) never accepts a ?st=
// token as a credential.
func TestRequireAuth_RejectsStreamTokenOnly(t *testing.T) {
	am := NewAuthMiddleware(stubTokenValidator{claims: &auth.Claims{UserID: 1, TokenType: auth.TokenTypeAccess}}, stubSessionValidator{valid: true}, nil, nil)
	probe := &probeHandler{}
	h := am.RequireAuth(probe)

	// Only a ?st= query, no Authorization header and no ?token=.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/library?st=some.stream.token", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if probe.ran {
		t.Fatal("handler ran; RequireAuth must not accept a ?st= token")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// stubUserLoader returns a fixed user (or error) for any id.
type stubUserLoader struct {
	user *models.User
	err  error
}

func (s stubUserLoader) GetByID(context.Context, int) (*models.User, error) {
	return s.user, s.err
}

func TestRequireStreamAuth_DisabledUserStreamTokenReturns401(t *testing.T) {
	// A signed ?st= URL outlives login state (24h TTL); once the account is
	// disabled the token must stop streaming even though its signature and
	// session binding are still valid.
	am := NewAuthMiddleware(
		stubTokenValidator{err: errors.New("no bearer")},
		stubSessionValidator{},
		nil,
		stubUserLoader{user: &models.User{ID: 42, Enabled: false}},
	)
	probe := &probeHandler{}
	h := am.RequireStreamAuth(allowStreamToken(42, "profile-7"))(probe)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-1?st=tok", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if probe.ran {
		t.Fatal("handler ran; disabled user's ?st= must 401")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestRequireStreamAuth_EnabledUserStreamTokenStillAuthenticates(t *testing.T) {
	am := NewAuthMiddleware(
		stubTokenValidator{err: errors.New("no bearer")},
		stubSessionValidator{},
		nil,
		stubUserLoader{user: &models.User{ID: 42, Enabled: true}},
	)
	probe := &probeHandler{}
	h := am.RequireStreamAuth(allowStreamToken(42, "profile-7"))(probe)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/sess-1?st=tok", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !probe.ran || probe.userID != 42 {
		t.Fatalf("enabled user must pass; ran=%v status=%d", probe.ran, rr.Code)
	}
}
