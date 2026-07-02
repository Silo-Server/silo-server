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

func TestPlaybackSessionAuth_CaseInsensitivePlaySessionId(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	sessions := NewSessionStore(30*24*time.Hour, clock)
	_ = sessions.Put(Session{Token: "compat-tok", StreamAppUserID: 1})
	playbackStore := NewPlaybackSessionStore(time.Hour, clock)
	playbackStore.Put(PlaybackSession{ID: "ps-abc", CompatToken: "compat-tok"})

	mw := PlaybackSessionAuth(sessions, playbackStore, nil)

	cases := []struct {
		name     string
		rawQuery string
		wantCode int
	}{
		// Wholphin's jellyfin-sdk-kotlin direct-play URL: lowercase playSessionId,
		// no api_key and no auth header. Previously 401'd (case-sensitive lookup).
		{"lowercase playSessionId (Wholphin)", "static=true&mediaSourceId=x&playSessionId=ps-abc", http.StatusOK},
		{"canonical PlaySessionId", "PlaySessionId=ps-abc", http.StatusOK},
		{"legacy PlaySessionID", "PlaySessionID=ps-abc", http.StatusOK},
		{"unknown play session", "playSessionId=does-not-exist", http.StatusUnauthorized},
		{"no auth at all", "static=true", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/Videos/itm/stream?"+tc.rawQuery, nil)
			rec := httptest.NewRecorder()
			gotSession := false
			mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if SessionFromContext(r.Context()) != nil {
					gotSession = true
				}
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusOK && !gotSession {
				t.Fatal("expected authenticated session in context")
			}
		})
	}
}

func TestExtractToken_CaseInsensitiveAPIKey(t *testing.T) {
	// "ApiKey" is Jellyfin's current query spelling (PascalCase); "api_key" is
	// the legacy spelling. Native clients (incl. Jellyfin Android TV) build
	// direct-play /Videos/{id}/stream URLs with "ApiKey", so rejecting it 401s
	// the stream. Match both, plus the casing variants clients send.
	for _, key := range []string{"ApiKey", "apikey", "APIKEY", "api_key", "Api_Key", "API_KEY"} {
		req := httptest.NewRequest("GET", "/Videos/itm/stream?"+key+"=tok123", nil)
		if got, ok := ExtractToken(req); !ok || got != "tok123" {
			t.Fatalf("%s: ExtractToken = (%q, %v), want (tok123, true)", key, got, ok)
		}
	}
}

func TestRequireAdminAPIKey_AcceptsAdminKey(t *testing.T) {
	authn := newAdminAPIKeyAuthForTest(
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
	authn := newAdminAPIKeyAuthForTest(
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

func TestRequireAdminAPIKey_RejectsNilAPIKey(t *testing.T) {
	authn := newAdminAPIKeyAuthForTest(
		&fakeAPIKeyValidator{returnNilWithoutError: true},
		&fakeAPIKeyUserLoader{user: &models.User{ID: 2, Role: "admin", Enabled: true}},
	)
	req := httptest.NewRequest("GET", "/Library/VirtualFolders", nil)
	req.Header.Set("X-Emby-Token", "sa_test")
	rec := httptest.NewRecorder()

	authn.RequireAdminAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequireAdminAPIKey_LastUsedUpdateHasDeadline(t *testing.T) {
	called := make(chan bool, 1)
	authn := newAdminAPIKeyAuthForTest(
		&fakeAPIKeyValidator{
			key: &models.APIKey{ID: 1, UserID: 2, Key: "sa_test"},
			update: func(ctx context.Context, _ int64) error {
				_, ok := ctx.Deadline()
				called <- ok
				return nil
			},
		},
		&fakeAPIKeyUserLoader{user: &models.User{ID: 2, Role: "admin", Enabled: true}},
	)
	req := httptest.NewRequest("GET", "/Library/VirtualFolders", nil)
	req.Header.Set("X-Emby-Token", "sa_test")
	rec := httptest.NewRecorder()

	authn.RequireAdminAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case ok := <-called:
		if !ok {
			t.Fatal("expected last-used update context to have a deadline")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for last-used update")
	}
}

// newAdminAPIKeyAuthForTest builds an authenticator without a UserStoreProvider,
// exercising the admin-bool path only (no session synthesis).
func newAdminAPIKeyAuthForTest(keys apiKeyValidator, users apiKeyUserLoader) *AdminAPIKeyAuthenticator {
	return NewAdminAPIKeyAuthenticator(keys, users, nil, nil)
}

type fakeAPIKeyValidator struct {
	key                   *models.APIKey
	returnNilWithoutError bool
	getCalls              int
	update                func(context.Context, int64) error
}

func (f *fakeAPIKeyValidator) GetByKey(_ context.Context, key string) (*models.APIKey, error) {
	f.getCalls++
	if f.returnNilWithoutError {
		return nil, nil
	}
	if f.key != nil && f.key.Key == key {
		return f.key, nil
	}
	return nil, auth.ErrAPIKeyNotFound
}

func (f *fakeAPIKeyValidator) UpdateLastUsed(ctx context.Context, id int64) error {
	if f.update != nil {
		return f.update(ctx, id)
	}
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
