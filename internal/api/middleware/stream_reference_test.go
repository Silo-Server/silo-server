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

// A Cast receiver holds only the plan URL, so the plan's stream reference has
// to stand in for the account credential, for its own session and profile
// only.
func TestRequireStreamReferenceAuth(t *testing.T) {
	const secret = "test-secret"
	const (
		session = "11111111-1111-4111-8111-111111111111"
		stopped = "44444444-4444-4444-8444-444444444444"
		unknown = "55555555-5555-4555-8555-555555555555"
		// disabledOwned and deletedOwned are live sessions of accounts 8
		// (disabled) and 9 (gone).
		disabledOwned = "66666666-6666-4666-8666-666666666666"
		deletedOwned  = "77777777-7777-4777-8777-777777777777"
	)
	jwt := auth.NewJWTService(secret, 15*time.Minute, 7*24*time.Hour)
	users := streamReferenceUsers{users: map[int]*models.User{
		7: {ID: 7, Role: models.RoleUser, Enabled: true},
		8: {ID: 8, Role: models.RoleUser, Enabled: false},
	}}
	sign := func(t *testing.T, c streamtoken.Claims, ttl time.Duration) string {
		t.Helper()
		token, err := streamtoken.Sign(c, secret, ttl)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	playback := streamtoken.Claims{SessionID: session, PlayMethod: "transcode", UserID: 7, ProfileID: "p-kid"}
	valid := sign(t, playback, time.Hour)
	access, err := jwt.GenerateAccessToken(7, models.RoleUser, "sess-live")
	if err != nil {
		t.Fatal(err)
	}

	// attempts maps each playback session to its owner; stopped sessions are
	// listed with live=false.
	type attempt struct {
		user    int
		profile string
		live    bool
	}
	attempts := map[string]attempt{session: {7, "p-kid", true}, stopped: {7, "p-kid", false}, disabledOwned: {8, "", true}, deletedOwned: {9, "", true}}
	var attemptErr error
	serve := func(t *testing.T, users APIKeyUserLoader, path string, header http.Header) (*httptest.ResponseRecorder, *auth.Claims, string) {
		t.Helper()
		am := NewAuthMiddleware(jwt, &fakeSessionValidator{roles: map[string]string{"sess-live": models.RoleUser}}, nil, users)
		am.SetStreamReferenceVerifier(func(reference string) (*streamtoken.Claims, error) {
			return streamtoken.Verify(reference, secret)
		}, func(_ context.Context, id string) (int, string, bool, error) {
			a, ok := attempts[id]
			return a.user, a.profile, ok && a.live, attemptErr
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

	t.Run("reference alone plays its session as its profile", func(t *testing.T) {
		// A caller-chosen profile header is replaced, and the PIN-locked
		// profile the plan was started under still resolves.
		rec, claims, profile := serve(t, users, "/media/"+session+"?st="+valid, http.Header{"X-Profile-Id": {"p-other"}, "X-Profile-Token": {"forged"}})
		if rec.Code != http.StatusOK || claims == nil {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		if claims.UserID != 7 || claims.Role != models.RoleUser || claims.TokenType != auth.TokenTypeStreamReference || claims.SessionID != "" || profile != "p-kid" {
			t.Fatalf("claims %+v profile %q", claims, profile)
		}
	})

	t.Run("account credential keeps the standard path", func(t *testing.T) {
		rec, claims, _ := serve(t, users, "/media/"+session+"?st=not-a-token", http.Header{"Authorization": {"Bearer " + access}})
		if rec.Code != http.StatusOK || claims == nil || claims.TokenType != auth.TokenTypeAccess {
			t.Fatalf("status %d claims %+v", rec.Code, claims)
		}
		// A bad account credential is refused, never retried as the reference.
		rec, claims, _ = serve(t, users, "/media/"+session+"?st="+valid, http.Header{"Authorization": {"Bearer not-a-token"}})
		if rec.Code != http.StatusUnauthorized || claims != nil {
			t.Fatalf("bad bearer with a valid reference: status %d claims %+v", rec.Code, claims)
		}
	})

	t.Run("account-scope reference drops the caller's profile header", func(t *testing.T) {
		const accountSession = "33333333-3333-4333-8333-333333333333"
		attempts[accountSession] = attempt{7, "", true}
		reference := sign(t, streamtoken.Claims{SessionID: accountSession, PlayMethod: "transcode", UserID: 7}, time.Hour)
		rec, claims, profile := serve(t, users, "/media/"+accountSession+"?st="+reference, http.Header{"X-Profile-Id": {"p-other"}})
		if rec.Code != http.StatusOK || claims == nil || profile != "" {
			t.Fatalf("status %d claims %+v profile %q", rec.Code, claims, profile)
		}
	})

	refused := []struct {
		name   string
		users  APIKeyUserLoader
		path   string
		status int
	}{
		{"no credential", users, "/media/" + session, http.StatusUnauthorized},
		{"another session", users, "/media/22222222-2222-4222-8222-222222222222?st=" + valid, http.StatusForbidden},
		{"expired reference", users, "/media/" + session + "?st=" + sign(t, playback, -time.Minute), http.StatusUnauthorized},
		{"download token", users, "/media/" + session + "?st=" + sign(t, streamtoken.Claims{SessionID: session, PlayMethod: streamtoken.PlayMethodDownload, UserID: 7}, time.Hour), http.StatusUnauthorized},
		{"theme token", users, "/media/" + session + "?st=" + sign(t, streamtoken.Claims{SessionID: session, PlayMethod: streamtoken.PlayMethodThemeDirect, UserID: 7}, time.Hour), http.StatusUnauthorized},
		{"account token as reference", users, "/media/" + session + "?st=" + access, http.StatusUnauthorized},
		{"other signing key", users, "/media/" + session + "?st=" + func() string {
			token, _ := streamtoken.Sign(playback, "other-secret", time.Hour)
			return token
		}(), http.StatusUnauthorized},
		{"disabled account", users, "/media/" + disabledOwned + "?st=" + sign(t, streamtoken.Claims{SessionID: disabledOwned, PlayMethod: "transcode", UserID: 8}, time.Hour), http.StatusUnauthorized},
		{"deleted account", users, "/media/" + deletedOwned + "?st=" + sign(t, streamtoken.Claims{SessionID: deletedOwned, PlayMethod: "transcode", UserID: 9}, time.Hour), http.StatusUnauthorized},
		{"account store down", streamReferenceUsers{err: errors.New("dial tcp: connection refused")}, "/media/" + session + "?st=" + valid, http.StatusServiceUnavailable},
		// The attempt check ends a reference whose session was stopped, with or
		// without a session-deny marker store.
		{"stopped session", users, "/media/" + stopped + "?st=" + sign(t, streamtoken.Claims{SessionID: stopped, PlayMethod: "transcode", UserID: 7, ProfileID: "p-kid"}, time.Hour), http.StatusGone},
		{"session owned by another profile", users, "/media/" + session + "?st=" + sign(t, streamtoken.Claims{SessionID: session, PlayMethod: "transcode", UserID: 7, ProfileID: "p-other"}, time.Hour), http.StatusForbidden},
		{"unknown session", users, "/media/" + unknown + "?st=" + sign(t, streamtoken.Claims{SessionID: unknown, PlayMethod: "transcode", UserID: 7, ProfileID: "p-kid"}, time.Hour), http.StatusGone},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			rec, claims, _ := serve(t, tc.users, tc.path, nil)
			if rec.Code != tc.status || claims != nil {
				t.Fatalf("status %d, want %d; claims %+v", rec.Code, tc.status, claims)
			}
		})
	}

	t.Run("attempt store down", func(t *testing.T) {
		attemptErr = errors.New("dial tcp: connection refused")
		defer func() { attemptErr = nil }()
		rec, claims, _ := serve(t, users, "/media/"+session+"?st="+valid, nil)
		if rec.Code != http.StatusServiceUnavailable || claims != nil {
			t.Fatalf("status %d claims %+v", rec.Code, claims)
		}
	})
}
