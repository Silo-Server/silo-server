package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPlaybackSessionAuth_CaseInsensitivePlaySessionId(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	sessions := NewSessionStore(30*24*time.Hour, clock)
	_ = sessions.Put(Session{Token: "compat-tok", StreamAppUserID: 1})
	playbackStore := NewPlaybackSessionStore(time.Hour, clock)
	playbackStore.Put(PlaybackSession{ID: "ps-abc", CompatToken: "compat-tok", RouteItemID: "itm", MediaSources: []PlaybackMediaSource{{ID: "x"}}})

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
		{"unknown play session", "playSessionId=does-not-exist", http.StatusUnauthorized},
		{"no auth at all", "static=true", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := requestWithCompatRouteItem(httptest.NewRequest("GET", "/Videos/itm/stream?"+tc.rawQuery, nil), "itm")
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

func TestAdminAPIKeyActivityAttribution(t *testing.T) {
	for _, route := range []struct {
		method string
		path   string
		wrap   func(*AdminAPIKeyAuthenticator) func(http.Handler) http.Handler
	}{
		{http.MethodPost, "/Library/Media/Updated", func(a *AdminAPIKeyAuthenticator) func(http.Handler) http.Handler {
			return a.RequireAdminAPIKey
		}},
		{http.MethodGet, "/Library/VirtualFolders", func(a *AdminAPIKeyAuthenticator) func(http.Handler) http.Handler {
			return RequireSessionOrAdminAPIKey(NewAuthenticator(NewSessionStore(time.Hour, nil), nil), a)
		}},
	} {
		for _, tc := range []struct {
			name   string
			token  string
			role   string
			scopes []string
			status int
		}{
			{name: "valid", token: "sa_test", role: "admin", status: http.StatusNoContent},
			{name: "unknown", token: "sa_unknown", role: "admin", status: http.StatusUnauthorized},
			{name: "non-admin", token: "sa_test", role: "user", status: http.StatusForbidden},
			{name: "scoped", token: "sa_test", role: "admin", scopes: []string{auth.ScopeAdminUsers}, status: http.StatusForbidden},
		} {
			t.Run(route.path+"/"+tc.name, func(t *testing.T) {
				authn := newAdminAPIKeyAuthForTest(
					&fakeAPIKeyValidator{key: &models.APIKey{ID: 1, UserID: 2, Key: "sa_test", Scopes: tc.scopes}},
					&fakeAPIKeyUserLoader{user: &models.User{ID: 2, Role: tc.role, Enabled: true}},
				)
				capture := &activityCapture{}
				called := false
				handler := activitylog.NewMiddleware(capture, "node-a")(route.wrap(authn)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					if !AdminAPIKeyFromContext(r.Context()) {
						t.Fatal("missing admin API key marker")
					}
					w.WriteHeader(http.StatusNoContent)
				})))
				req := httptest.NewRequest(route.method, route.path, nil)
				req.Header.Set("X-Emby-Token", tc.token)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != tc.status {
					t.Fatalf("status = %d, want %d", rec.Code, tc.status)
				}
				accepted := tc.status == http.StatusNoContent
				if called != accepted {
					t.Fatalf("handler called = %v, want %v", called, accepted)
				}
				entries := capture.take()
				if len(entries) != 1 || entries[0].StatusCode != tc.status {
					t.Fatalf("entries = %+v, want one entry with status %d", entries, tc.status)
				}
				if accepted {
					if entries[0].UserID == nil || *entries[0].UserID != 2 {
						t.Fatalf("UserID = %v, want account 2", entries[0].UserID)
					}
				} else if entries[0].UserID != nil {
					t.Fatalf("rejected request UserID = %v, want nil", entries[0].UserID)
				}
			})
		}
	}
}

// newAdminAPIKeyAuthForTest builds an authenticator without a UserStoreProvider,
// exercising the admin-bool path only (no session synthesis).
func newAdminAPIKeyAuthForTest(keys apiKeyValidator, users apiKeyUserLoader) *AdminAPIKeyAuthenticator {
	return NewAdminAPIKeyAuthenticator(keys, users, nil, nil)
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
