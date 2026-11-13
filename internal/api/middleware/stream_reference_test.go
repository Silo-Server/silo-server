package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

const (
	streamReferenceSecret = "test-secret"
	referenceSession      = "11111111-1111-4111-8111-111111111111"
	stoppedSession        = "44444444-4444-4444-8444-444444444444"
	unknownSession        = "55555555-5555-4555-8555-555555555555"
	accountScopeSession   = "33333333-3333-4333-8333-333333333333"
	// unboundSession was started with an API key; signedOutSession by a
	// login session that has since been revoked.
	unboundSession   = "66666666-6666-4666-8666-666666666666"
	signedOutSession = "77777777-7777-4777-8777-777777777777"
)

// streamReferenceFixture runs RequireStreamReferenceAuth and viewer access in
// front of a handler that records the identity it was given.
type streamReferenceFixture struct {
	jwt        *auth.JWTService
	sessions   *fakeSessionValidator
	attempts   map[string]StreamReferenceAttempt
	attemptErr error
	access     string
}

func newStreamReferenceFixture(t *testing.T) *streamReferenceFixture {
	t.Helper()
	f := &streamReferenceFixture{
		jwt:      auth.NewJWTService(streamReferenceSecret, 15*time.Minute, 7*24*time.Hour),
		sessions: &fakeSessionValidator{roles: map[string]string{"sess-live": models.RoleUser}},
		attempts: map[string]StreamReferenceAttempt{
			referenceSession:    {UserID: 7, ProfileID: "p-kid", LoginSessionID: "sess-live", Live: true},
			stoppedSession:      {UserID: 7, ProfileID: "p-kid", LoginSessionID: "sess-live"},
			accountScopeSession: {UserID: 7, LoginSessionID: "sess-live", Live: true},
			unboundSession:      {UserID: 7, ProfileID: "p-kid", Live: true},
			signedOutSession:    {UserID: 7, ProfileID: "p-kid", LoginSessionID: "sess-revoked", Live: true},
		},
	}
	access, err := f.jwt.GenerateAccessToken(7, models.RoleUser, "sess-live")
	if err != nil {
		t.Fatal(err)
	}
	f.access = access
	return f
}

func (f *streamReferenceFixture) sign(t *testing.T, c streamtoken.Claims, ttl time.Duration) string {
	t.Helper()
	token, err := streamtoken.Sign(c, streamReferenceSecret, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (f *streamReferenceFixture) reference(t *testing.T, session string, user int, profile string) string {
	t.Helper()
	return f.sign(t, streamtoken.Claims{SessionID: session, PlayMethod: "transcode", UserID: user, ProfileID: profile}, time.Hour)
}

func (f *streamReferenceFixture) serve(t *testing.T, path string, header http.Header) (*httptest.ResponseRecorder, *auth.Claims, string) {
	t.Helper()
	am := NewAuthMiddleware(f.jwt, f.sessions, nil, nil)
	am.SetStreamReferenceVerifier(func(reference string) (*streamtoken.Claims, error) {
		return streamtoken.Verify(reference, streamReferenceSecret)
	}, func(_ context.Context, id string) (StreamReferenceAttempt, error) {
		return f.attempts[id], f.attemptErr
	})
	viewer := NewViewerAccessMiddleware(&fakeViewerResolver{
		profiles: map[string]int{"p-kid": 7, "p-other": 7},
		pinned:   map[string]bool{"p-kid": true},
	})
	var claims *auth.Claims
	var profile string
	router := chi.NewRouter()
	router.With(am.RequireStreamReferenceAuth, viewer.RequireViewerAccess).Get("/media/{session_id}", func(w http.ResponseWriter, r *http.Request) {
		claims, profile = GetClaims(r.Context()), GetProfileID(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for name, values := range header {
		req.Header[name] = values
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec, claims, profile
}

// A Cast receiver holds only the plan URL, so the plan's stream reference has
// to stand in for the account credential, for its own session and profile
// only.
func TestRequireStreamReferenceAuthAccepts(t *testing.T) {
	f := newStreamReferenceFixture(t)
	valid := f.reference(t, referenceSession, 7, "p-kid")

	t.Run("reference alone plays its session as its profile", func(t *testing.T) {
		// A caller-chosen profile header is replaced, and the PIN-locked
		// profile the plan was started under still resolves.
		rec, claims, profile := f.serve(t, "/media/"+referenceSession+"?st="+valid, http.Header{"X-Profile-Id": {"p-other"}, "X-Profile-Token": {"forged"}})
		if rec.Code != http.StatusOK || claims == nil {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		if claims.UserID != 7 || claims.Role != models.RoleUser || claims.TokenType != auth.TokenTypeStreamReference || claims.SessionID != "" || profile != "p-kid" {
			t.Fatalf("claims %+v profile %q", claims, profile)
		}
	})

	t.Run("account credential keeps the standard path", func(t *testing.T) {
		rec, claims, _ := f.serve(t, "/media/"+referenceSession+"?st=not-a-token", http.Header{"Authorization": {"Bearer " + f.access}})
		if rec.Code != http.StatusOK || claims == nil || claims.TokenType != auth.TokenTypeAccess {
			t.Fatalf("status %d claims %+v", rec.Code, claims)
		}
		// A bad account credential is refused, never retried as the reference.
		rec, claims, _ = f.serve(t, "/media/"+referenceSession+"?st="+valid, http.Header{"Authorization": {"Bearer not-a-token"}})
		if rec.Code != http.StatusUnauthorized || claims != nil {
			t.Fatalf("bad bearer with a valid reference: status %d claims %+v", rec.Code, claims)
		}
	})

	t.Run("account-scope reference drops the caller's profile header", func(t *testing.T) {
		reference := f.reference(t, accountScopeSession, 7, "")
		rec, claims, profile := f.serve(t, "/media/"+accountScopeSession+"?st="+reference, http.Header{"X-Profile-Id": {"p-other"}})
		if rec.Code != http.StatusOK || claims == nil || profile != "" {
			t.Fatalf("status %d claims %+v profile %q", rec.Code, claims, profile)
		}
	})
}

func TestRequireStreamReferenceAuthRefuses(t *testing.T) {
	f := newStreamReferenceFixture(t)
	valid := f.reference(t, referenceSession, 7, "p-kid")
	otherKey, err := streamtoken.Sign(streamtoken.Claims{SessionID: referenceSession, PlayMethod: "transcode", UserID: 7, ProfileID: "p-kid"}, "other-secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	media := "/media/" + referenceSession + "?st="
	refused := []struct {
		name   string
		path   string
		status int
	}{
		{"no credential", "/media/" + referenceSession, http.StatusUnauthorized},
		{"another session", "/media/22222222-2222-4222-8222-222222222222?st=" + valid, http.StatusForbidden},
		{"expired reference", media + f.sign(t, streamtoken.Claims{SessionID: referenceSession, PlayMethod: "transcode", UserID: 7, ProfileID: "p-kid"}, -time.Minute), http.StatusUnauthorized},
		{"download token", media + f.sign(t, streamtoken.Claims{SessionID: referenceSession, PlayMethod: streamtoken.PlayMethodDownload, UserID: 7}, time.Hour), http.StatusUnauthorized},
		{"theme token", media + f.sign(t, streamtoken.Claims{SessionID: referenceSession, PlayMethod: streamtoken.PlayMethodThemeDirect, UserID: 7}, time.Hour), http.StatusUnauthorized},
		{"account token as reference", media + f.access, http.StatusUnauthorized},
		{"other signing key", media + otherKey, http.StatusUnauthorized},
		// A reference is bound to the login session that started playback:
		// without one it needs the account credential, and signing out ends it.
		{"attempt started with an API key", "/media/" + unboundSession + "?st=" + f.reference(t, unboundSession, 7, "p-kid"), http.StatusUnauthorized},
		{"login session signed out", "/media/" + signedOutSession + "?st=" + f.reference(t, signedOutSession, 7, "p-kid"), http.StatusUnauthorized},
		// The attempt check ends a reference whose session was stopped, with or
		// without a session-deny marker store.
		{"stopped session", "/media/" + stoppedSession + "?st=" + f.reference(t, stoppedSession, 7, "p-kid"), http.StatusGone},
		{"session owned by another profile", media + f.reference(t, referenceSession, 7, "p-other"), http.StatusForbidden},
		{"unknown session", "/media/" + unknownSession + "?st=" + f.reference(t, unknownSession, 7, "p-kid"), http.StatusGone},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			rec, claims, _ := f.serve(t, tc.path, nil)
			if rec.Code != tc.status || claims != nil {
				t.Fatalf("status %d, want %d; claims %+v", rec.Code, tc.status, claims)
			}
		})
	}

	t.Run("attempt store down", func(t *testing.T) {
		f.attemptErr = errors.New("dial tcp: connection refused")
		defer func() { f.attemptErr = nil }()
		rec, claims, _ := f.serve(t, media+valid, nil)
		if rec.Code != http.StatusServiceUnavailable || claims != nil {
			t.Fatalf("status %d claims %+v", rec.Code, claims)
		}
	})

	t.Run("login session store down", func(t *testing.T) {
		f.sessions.err = errors.New("dial tcp: connection refused")
		defer func() { f.sessions.err = nil }()
		rec, claims, _ := f.serve(t, media+valid, nil)
		if rec.Code != http.StatusServiceUnavailable || claims != nil {
			t.Fatalf("status %d claims %+v", rec.Code, claims)
		}
	})
}
