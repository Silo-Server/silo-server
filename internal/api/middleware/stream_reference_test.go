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
	// disabledOwnedSession and deletedOwnedSession are live sessions of
	// accounts 8 (disabled) and 9 (gone).
	disabledOwnedSession = "66666666-6666-4666-8666-666666666666"
	deletedOwnedSession  = "77777777-7777-4777-8777-777777777777"
	accountScopeSession  = "33333333-3333-4333-8333-333333333333"
)

type streamReferenceUsers struct {
	users map[int]*models.User
	err   error
}

func (f streamReferenceUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	if user, ok := f.users[id]; ok {
		return user, nil
	}
	return nil, auth.ErrNotFound
}

// referenceAttempt is a playback attempt's owner and whether it is live.
type referenceAttempt struct {
	user    int
	profile string
	live    bool
}

// streamReferenceFixture runs RequireStreamReferenceAuth and viewer access in
// front of a handler that records the identity it was given.
type streamReferenceFixture struct {
	jwt        *auth.JWTService
	users      streamReferenceUsers
	attempts   map[string]referenceAttempt
	attemptErr error
	access     string
}

func newStreamReferenceFixture(t *testing.T) *streamReferenceFixture {
	t.Helper()
	f := &streamReferenceFixture{
		jwt: auth.NewJWTService(streamReferenceSecret, 15*time.Minute, 7*24*time.Hour),
		users: streamReferenceUsers{users: map[int]*models.User{
			7: {ID: 7, Role: models.RoleUser, Enabled: true},
			8: {ID: 8, Role: models.RoleUser, Enabled: false},
		}},
		attempts: map[string]referenceAttempt{
			referenceSession:     {7, "p-kid", true},
			stoppedSession:       {7, "p-kid", false},
			disabledOwnedSession: {8, "", true},
			deletedOwnedSession:  {9, "", true},
			accountScopeSession:  {7, "", true},
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

func (f *streamReferenceFixture) serve(t *testing.T, users APIKeyUserLoader, path string, header http.Header) (*httptest.ResponseRecorder, *auth.Claims, string) {
	t.Helper()
	am := NewAuthMiddleware(f.jwt, &fakeSessionValidator{roles: map[string]string{"sess-live": models.RoleUser}}, nil, users)
	am.SetStreamReferenceVerifier(func(reference string) (*streamtoken.Claims, error) {
		return streamtoken.Verify(reference, streamReferenceSecret)
	}, func(_ context.Context, id string) (int, string, bool, error) {
		a, ok := f.attempts[id]
		return a.user, a.profile, ok && a.live, f.attemptErr
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
		rec, claims, profile := f.serve(t, f.users, "/media/"+referenceSession+"?st="+valid, http.Header{"X-Profile-Id": {"p-other"}, "X-Profile-Token": {"forged"}})
		if rec.Code != http.StatusOK || claims == nil {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		if claims.UserID != 7 || claims.Role != models.RoleUser || claims.TokenType != auth.TokenTypeStreamReference || claims.SessionID != "" || profile != "p-kid" {
			t.Fatalf("claims %+v profile %q", claims, profile)
		}
	})

	t.Run("account credential keeps the standard path", func(t *testing.T) {
		rec, claims, _ := f.serve(t, f.users, "/media/"+referenceSession+"?st=not-a-token", http.Header{"Authorization": {"Bearer " + f.access}})
		if rec.Code != http.StatusOK || claims == nil || claims.TokenType != auth.TokenTypeAccess {
			t.Fatalf("status %d claims %+v", rec.Code, claims)
		}
		// A bad account credential is refused, never retried as the reference.
		rec, claims, _ = f.serve(t, f.users, "/media/"+referenceSession+"?st="+valid, http.Header{"Authorization": {"Bearer not-a-token"}})
		if rec.Code != http.StatusUnauthorized || claims != nil {
			t.Fatalf("bad bearer with a valid reference: status %d claims %+v", rec.Code, claims)
		}
	})

	t.Run("account-scope reference drops the caller's profile header", func(t *testing.T) {
		reference := f.reference(t, accountScopeSession, 7, "")
		rec, claims, profile := f.serve(t, f.users, "/media/"+accountScopeSession+"?st="+reference, http.Header{"X-Profile-Id": {"p-other"}})
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
		users  APIKeyUserLoader
		path   string
		status int
	}{
		{"no credential", f.users, "/media/" + referenceSession, http.StatusUnauthorized},
		{"another session", f.users, "/media/22222222-2222-4222-8222-222222222222?st=" + valid, http.StatusForbidden},
		{"expired reference", f.users, media + f.sign(t, streamtoken.Claims{SessionID: referenceSession, PlayMethod: "transcode", UserID: 7, ProfileID: "p-kid"}, -time.Minute), http.StatusUnauthorized},
		{"download token", f.users, media + f.sign(t, streamtoken.Claims{SessionID: referenceSession, PlayMethod: streamtoken.PlayMethodDownload, UserID: 7}, time.Hour), http.StatusUnauthorized},
		{"theme token", f.users, media + f.sign(t, streamtoken.Claims{SessionID: referenceSession, PlayMethod: streamtoken.PlayMethodThemeDirect, UserID: 7}, time.Hour), http.StatusUnauthorized},
		{"account token as reference", f.users, media + f.access, http.StatusUnauthorized},
		{"other signing key", f.users, media + otherKey, http.StatusUnauthorized},
		{"disabled account", f.users, "/media/" + disabledOwnedSession + "?st=" + f.reference(t, disabledOwnedSession, 8, ""), http.StatusUnauthorized},
		{"deleted account", f.users, "/media/" + deletedOwnedSession + "?st=" + f.reference(t, deletedOwnedSession, 9, ""), http.StatusUnauthorized},
		{"account store down", streamReferenceUsers{err: errors.New("dial tcp: connection refused")}, media + valid, http.StatusServiceUnavailable},
		// The attempt check ends a reference whose session was stopped, with or
		// without a session-deny marker store.
		{"stopped session", f.users, "/media/" + stoppedSession + "?st=" + f.reference(t, stoppedSession, 7, "p-kid"), http.StatusGone},
		{"session owned by another profile", f.users, media + f.reference(t, referenceSession, 7, "p-other"), http.StatusForbidden},
		{"unknown session", f.users, "/media/" + unknownSession + "?st=" + f.reference(t, unknownSession, 7, "p-kid"), http.StatusGone},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			rec, claims, _ := f.serve(t, tc.users, tc.path, nil)
			if rec.Code != tc.status || claims != nil {
				t.Fatalf("status %d, want %d; claims %+v", rec.Code, tc.status, claims)
			}
		})
	}

	t.Run("attempt store down", func(t *testing.T) {
		f.attemptErr = errors.New("dial tcp: connection refused")
		defer func() { f.attemptErr = nil }()
		rec, claims, _ := f.serve(t, f.users, media+valid, nil)
		if rec.Code != http.StatusServiceUnavailable || claims != nil {
			t.Fatalf("status %d claims %+v", rec.Code, claims)
		}
	})
}
