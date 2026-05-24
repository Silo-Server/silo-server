package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestRequireSession_SkipsRefreshWhenNoAuthService(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	store := NewSessionStore(30*24*time.Hour, clock)

	// Session with token expiring in 3 minutes (within 5min buffer).
	// With no authService configured, refresh is skipped and the session
	// is still returned (best-effort enhancement, not hard requirement).
	_ = store.Put(Session{
		Token:                 "valid-tok",
		StreamAppUserID:       1,
		StreamAppAccessToken:  "old-access",
		StreamAppRefreshToken: "refresh-tok",
		StreamAppTokenExpiry:  now.Add(3 * time.Minute),
	})

	authn := &Authenticator{sessions: store, authService: nil}
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Emby-Token", "valid-tok")
	rec := httptest.NewRecorder()

	handler := authn.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session := SessionFromContext(r.Context())
		if session == nil {
			t.Fatal("expected session in context")
		}
		if session.StreamAppAccessToken != "old-access" {
			t.Errorf("expected access token unchanged, got %s", session.StreamAppAccessToken)
		}
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestRequireSession_PassesThroughNonExpiringToken(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	store := NewSessionStore(30*24*time.Hour, clock)

	// Token not expiring for 30 minutes — no refresh needed.
	_ = store.Put(Session{
		Token:                "fresh-tok",
		StreamAppUserID:      1,
		StreamAppAccessToken: "good-access",
		StreamAppTokenExpiry: now.Add(30 * time.Minute),
	})

	authn := &Authenticator{sessions: store}
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Emby-Token", "fresh-tok")
	rec := httptest.NewRecorder()

	handler := authn.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session := SessionFromContext(r.Context())
		if session == nil {
			t.Fatal("expected session in context")
		}
		if session.StreamAppAccessToken != "good-access" {
			t.Errorf("expected access token unchanged, got %s", session.StreamAppAccessToken)
		}
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestRequireSession_NoAuthService_PassesThroughExpiredStreamAppToken(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	store := NewSessionStore(30*24*time.Hour, clock)

	// Session with already-expired StreamApp token and no authService to refresh.
	_ = store.Put(Session{
		Token:                "expired-tok",
		StreamAppUserID:      1,
		StreamAppAccessToken: "dead-access",
		StreamAppTokenExpiry: now.Add(-1 * time.Hour), // already expired
	})

	authn := &Authenticator{sessions: store, authService: nil}
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Emby-Token", "expired-tok")
	rec := httptest.NewRecorder()

	handler := authn.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// When authService is nil and token is expired, session should still
		// be returned (the compat session itself is valid, only the StreamApp
		// token is stale). The refresh logic is a best-effort enhancement.
		session := SessionFromContext(r.Context())
		if session == nil {
			t.Fatal("expected session in context")
		}
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (no authService = skip refresh), got %d", rec.Code)
	}
}

func TestRequireAdminAPIKey_AcceptsAdminKey(t *testing.T) {
	authn := NewAdminAPIKeyAuthenticator(
		&fakeAPIKeyValidator{key: &models.APIKey{ID: 1, UserID: 2, Key: "sa_test"}},
		&fakeAPIKeyUserLoader{user: &models.User{ID: 2, Role: "admin", Enabled: true}},
	)
	req := httptest.NewRequest("GET", "/Library/VirtualFolders", nil)
	req.Header.Set("X-Emby-Token", "sa_test")
	rec := httptest.NewRecorder()

	authn.RequireAdminAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !AdminAPIKeyFromContext(r.Context()) {
			t.Fatal("expected admin API key marker in context")
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequireAdminAPIKey_RejectsNonAdminKey(t *testing.T) {
	authn := NewAdminAPIKeyAuthenticator(
		&fakeAPIKeyValidator{key: &models.APIKey{ID: 1, UserID: 2, Key: "sa_test"}},
		&fakeAPIKeyUserLoader{user: &models.User{ID: 2, Role: "user", Enabled: true}},
	)
	req := httptest.NewRequest("POST", "/Library/Media/Updated", nil)
	req.Header.Set("X-Emby-Token", "sa_test")
	rec := httptest.NewRecorder()

	authn.RequireAdminAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

type fakeAPIKeyValidator struct {
	key *models.APIKey
}

func (f *fakeAPIKeyValidator) GetByKey(_ context.Context, key string) (*models.APIKey, error) {
	if f.key != nil && f.key.Key == key {
		return f.key, nil
	}
	return nil, auth.ErrAPIKeyNotFound
}

func (f *fakeAPIKeyValidator) UpdateLastUsed(context.Context, int64) error {
	return nil
}

type fakeAPIKeyUserLoader struct {
	user *models.User
}

func (f *fakeAPIKeyUserLoader) GetByID(_ context.Context, id int) (*models.User, error) {
	if f.user != nil && f.user.ID == id {
		return f.user, nil
	}
	return nil, auth.ErrNotFound
}
