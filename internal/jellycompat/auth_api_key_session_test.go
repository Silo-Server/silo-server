package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeUserStore embeds userstore.UserStore so only ListProfiles needs an
// implementation; any other method call panics (the synthesis path must not
// touch them).
type fakeUserStore struct {
	userstore.UserStore
	profiles []userstore.Profile
	listErr  error
}

func (s *fakeUserStore) ListProfiles(context.Context) ([]userstore.Profile, error) {
	return s.profiles, s.listErr
}

type fakeUserStoreProvider struct {
	store    *fakeUserStore
	forErr   error
	forCalls int
}

func (p *fakeUserStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	p.forCalls++
	if p.forErr != nil {
		return nil, p.forErr
	}
	return p.store, nil
}

func (p *fakeUserStoreProvider) Close() error { return nil }

// adminKeyAuthWithPrimary builds an authenticator backed by an admin key whose
// account has a non-primary and a primary profile (in that order, so the test
// proves IsPrimary selection is order-independent).
func adminKeyAuthWithPrimary(t *testing.T, clock func() time.Time) (*AdminAPIKeyAuthenticator, *fakeUserStoreProvider, *fakeAPIKeyValidator) {
	t.Helper()
	validator := &fakeAPIKeyValidator{key: &models.APIKey{ID: 1, UserID: 2, Key: "sa_test"}}
	users := &fakeAPIKeyUserLoader{user: &models.User{ID: 2, Username: "admin", Role: "admin", Enabled: true}}
	provider := &fakeUserStoreProvider{store: &fakeUserStore{profiles: []userstore.Profile{
		{ID: "kids", Name: "Kids", IsPrimary: false},
		{ID: "p1", Name: "Parent", IsPrimary: true},
	}}}
	keyAuth := NewAdminAPIKeyAuthenticator(validator, users, provider, clock)
	if keyAuth == nil {
		t.Fatal("expected non-nil authenticator")
	}
	return keyAuth, provider, validator
}

func apiKeyRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/Items", nil)
	req.Header.Set("X-Emby-Token", "sa_test")
	return req
}

func TestRequireSessionOrAPIKeySession_SynthesizesPrimaryProfile(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	keyAuth, provider, _ := adminKeyAuthWithPrimary(t, clock)
	sessionAuth := &Authenticator{sessions: NewSessionStore(time.Hour, clock)}

	var got *Session
	h := RequireSessionOrAPIKeySession(sessionAuth, keyAuth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = SessionFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, apiKeyRequest())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got == nil {
		t.Fatal("expected synthesized session in context")
	}
	if got.StreamAppUserID != 2 {
		t.Errorf("StreamAppUserID = %d, want 2", got.StreamAppUserID)
	}
	if got.ProfileID != "p1" || got.ProfileName != "Parent" {
		t.Errorf("profile = %q/%q, want p1/Parent", got.ProfileID, got.ProfileName)
	}
	if want := PseudoUserID(2, "p1"); got.PseudoUserID != want {
		t.Errorf("PseudoUserID = %s, want %s", got.PseudoUserID, want)
	}
	if got.Username != "admin" {
		t.Errorf("Username = %q, want admin", got.Username)
	}
	// Refresh-skip invariants: no upstream Silo token, zero expiry.
	if got.StreamAppAccessToken != "" || got.StreamAppRefreshToken != "" {
		t.Errorf("expected empty upstream tokens, got access=%q refresh=%q", got.StreamAppAccessToken, got.StreamAppRefreshToken)
	}
	if !got.StreamAppTokenExpiry.IsZero() {
		t.Error("expected zero StreamAppTokenExpiry so RequireSession skips refresh")
	}
	if provider.forCalls != 1 {
		t.Errorf("ForUser calls = %d, want 1", provider.forCalls)
	}
}

func TestRequireSessionOrAPIKeySession_RejectsNonAdminKey(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	validator := &fakeAPIKeyValidator{key: &models.APIKey{ID: 1, UserID: 2, Key: "sa_test"}}
	users := &fakeAPIKeyUserLoader{user: &models.User{ID: 2, Username: "bob", Role: "user", Enabled: true}}
	provider := &fakeUserStoreProvider{store: &fakeUserStore{profiles: []userstore.Profile{{ID: "p1", IsPrimary: true}}}}
	keyAuth := NewAdminAPIKeyAuthenticator(validator, users, provider, clock)
	sessionAuth := &Authenticator{sessions: NewSessionStore(time.Hour, clock)}

	h := RequireSessionOrAPIKeySession(sessionAuth, keyAuth)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run for a non-admin key")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, apiKeyRequest())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	// The non-admin account's profiles must never be consulted.
	if provider.forCalls != 0 {
		t.Errorf("ForUser calls = %d, want 0 (rejected before synthesis)", provider.forCalls)
	}
}

func TestRequireSessionOrAPIKeySession_RejectsUnknownKey(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	keyAuth, _, _ := adminKeyAuthWithPrimary(t, clock)
	sessionAuth := &Authenticator{sessions: NewSessionStore(time.Hour, clock)}

	h := RequireSessionOrAPIKeySession(sessionAuth, keyAuth)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run for an unknown key")
	}))
	req := httptest.NewRequest(http.MethodGet, "/Items", nil)
	req.Header.Set("X-Emby-Token", "sa_doesnotmatch")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestRequireSessionOrAPIKeySession_FallsThroughWhenSynthesisUnavailable guards
// the regression where the middleware fronts the whole browse group: a nil
// authenticator or one without a UserStoreProvider must fall through to session
// auth (clean 401) rather than panic.
func TestRequireSessionOrAPIKeySession_FallsThroughWhenSynthesisUnavailable(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	sessionAuth := &Authenticator{sessions: NewSessionStore(time.Hour, clock)}

	cases := map[string]*AdminAPIKeyAuthenticator{
		"nil authenticator": nil,
		"nil provider": NewAdminAPIKeyAuthenticator(
			&fakeAPIKeyValidator{key: &models.APIKey{ID: 1, UserID: 2, Key: "sa_test"}},
			&fakeAPIKeyUserLoader{user: &models.User{ID: 2, Role: "admin", Enabled: true}},
			nil, clock,
		),
	}
	for name, keyAuth := range cases {
		t.Run(name, func(t *testing.T) {
			h := RequireSessionOrAPIKeySession(sessionAuth, keyAuth)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("handler should not run without a valid session")
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, apiKeyRequest()) // must not panic
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}

// TestResolveSession_RevalidatesEachCallCachesProfile: the key + owning user are
// validated on every call (so revocation is immediate), while the primary-profile
// lookup is cached until apiKeyProfileCacheTTL.
func TestResolveSession_RevalidatesEachCallCachesProfile(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	keyAuth, provider, validator := adminKeyAuthWithPrimary(t, clock)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if s, _, handled := keyAuth.resolveSession(ctx, "sa_test"); !handled || s == nil {
			t.Fatalf("resolve %d should succeed", i)
		}
	}
	if validator.getCalls != 3 {
		t.Errorf("GetByKey calls = %d, want 3 (re-validated every request)", validator.getCalls)
	}
	if provider.forCalls != 1 {
		t.Errorf("ForUser calls = %d, want 1 (profile cached)", provider.forCalls)
	}

	// After the profile TTL, the profile is re-fetched (auth still every call).
	now = now.Add(apiKeyProfileCacheTTL + time.Second)
	if s, _, handled := keyAuth.resolveSession(ctx, "sa_test"); !handled || s == nil {
		t.Fatal("post-TTL resolve should succeed")
	}
	if validator.getCalls != 4 {
		t.Errorf("GetByKey calls = %d, want 4", validator.getCalls)
	}
	if provider.forCalls != 2 {
		t.Errorf("ForUser calls = %d, want 2 after profile TTL", provider.forCalls)
	}
}

// TestResolveSession_RevocationIsImmediate: once a key is revoked, the very next
// request fails — no cached authorization decision survives (Finding 2).
func TestResolveSession_RevocationIsImmediate(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	keyAuth, _, validator := adminKeyAuthWithPrimary(t, clock)
	ctx := context.Background()

	if s, _, handled := keyAuth.resolveSession(ctx, "sa_test"); !handled || s == nil {
		t.Fatal("initial resolve should succeed")
	}
	validator.key = nil // revoke: GetByKey now returns not-found

	s, res, handled := keyAuth.resolveSession(ctx, "sa_test")
	if !handled {
		t.Fatal("expected handled=true for an sa_ token")
	}
	if s != nil || res.ok {
		t.Fatalf("revoked key must not resolve a session; got session=%v ok=%v", s != nil, res.ok)
	}
	if res.status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.status)
	}
}

// TestPlaybackSessionAuth_APIKeyViaPlaySessionId: HLS follow-up requests carry
// only PlaySessionId. When the negotiated session's CompatToken is an sa_ key,
// PlaybackSessionAuth must still resolve it (Finding 1).
func TestPlaybackSessionAuth_APIKeyViaPlaySessionId(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	keyAuth, _, _ := adminKeyAuthWithPrimary(t, clock)
	sessions := NewSessionStore(time.Hour, clock)
	playback := NewPlaybackSessionStore(time.Hour, clock)
	playback.Put(PlaybackSession{ID: "ps1", CompatToken: "sa_test"})

	var got *Session
	h := PlaybackSessionAuth(sessions, playback, keyAuth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = SessionFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// No token header / no api_key — only PlaySessionId, like an HLS segment URL.
	req := httptest.NewRequest(http.MethodGet, "/Videos/x/hls/p/seg0.ts?PlaySessionId=ps1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got == nil || got.Token != "sa_test" || got.StreamAppUserID != 2 {
		t.Fatalf("expected synthesized api-key session (token sa_test, user 2); got %+v", got)
	}
}

// TestPlaybackSessionAuth_RevokedAPIKeyViaPlaySessionId: a revoked key fails even
// through the PlaySessionId fallback.
func TestPlaybackSessionAuth_RevokedAPIKeyViaPlaySessionId(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	keyAuth, _, validator := adminKeyAuthWithPrimary(t, clock)
	validator.key = nil // revoked
	sessions := NewSessionStore(time.Hour, clock)
	playback := NewPlaybackSessionStore(time.Hour, clock)
	playback.Put(PlaybackSession{ID: "ps1", CompatToken: "sa_test"})

	h := PlaybackSessionAuth(sessions, playback, keyAuth)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run for a revoked key")
	}))
	req := httptest.NewRequest(http.MethodGet, "/Videos/x/hls/p/seg0.ts?PlaySessionId=ps1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestValidatePseudoUser confirms the path-userId guard a synthesized session
// relies on: empty or matching ids pass; a mismatched id 404s.
func TestValidatePseudoUser(t *testing.T) {
	session := &Session{PseudoUserID: PseudoUserID(2, "p1")}

	if !validatePseudoUser(httptest.NewRecorder(), "", session) {
		t.Error("empty userID should pass")
	}
	if !validatePseudoUser(httptest.NewRecorder(), session.PseudoUserID.String(), session) {
		t.Error("matching userID should pass")
	}
	rec := httptest.NewRecorder()
	if validatePseudoUser(rec, PseudoUserID(2, "other").String(), session) {
		t.Error("mismatched userID should fail")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("mismatch status = %d, want 404", rec.Code)
	}
}

func TestHandleUsers_ReturnsCallerAsSingleElement(t *testing.T) {
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil)
	session := &Session{PseudoUserID: PseudoUserID(2, "p1"), Username: "admin"}

	req := httptest.NewRequest(http.MethodGet, "/Users", nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, session))
	rec := httptest.NewRecorder()
	h.HandleUsers(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var users []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &users); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(users) != 1 {
		t.Fatalf("len = %d, want 1 (current profile only)", len(users))
	}
	if users[0]["Id"] != session.PseudoUserID.String() {
		t.Errorf("Id = %v, want %s", users[0]["Id"], session.PseudoUserID.String())
	}
	if users[0]["Name"] != "admin" {
		t.Errorf("Name = %v, want admin", users[0]["Name"])
	}
}

func TestHandleUsers_RequiresSession(t *testing.T) {
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil)
	rec := httptest.NewRecorder()
	h.HandleUsers(rec, httptest.NewRequest(http.MethodGet, "/Users", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRouter_UsersRequiresAuth(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	router := NewRouter(Dependencies{Config: cfg})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Users", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /Users status = %d, want 401", rec.Code)
	}
}
