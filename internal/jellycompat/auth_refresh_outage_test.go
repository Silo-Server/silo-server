package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// A compat session whose Silo tokens are due for refresh while the session
// store is unreachable must survive: the request is a retryable 503 and the
// session stays in the store. A refresh token the server refuses still ends
// the compat session.
func TestRequireSession_StoreOutageKeepsTheSession(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://silo@127.0.0.1:1/silo?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	jwt := auth.NewJWTService("compat-outage-secret", 15*time.Minute, 24*time.Hour)
	svc := auth.NewService(nil, jwt, auth.NewSessionRepository(pool), auth.NewUserRepository(pool), nil, nil, nil)
	refresh, err := jwt.GenerateRefreshToken(1, "user", "sess-1")
	if err != nil {
		t.Fatal(err)
	}

	now := fixedNow()
	clock := func() time.Time { return now }
	store := NewSessionStore(30*24*time.Hour, clock)
	for token, refreshToken := range map[string]string{"outage-tok": refresh, "refused-tok": "not-a-jwt"} {
		_ = store.Put(Session{
			Token:                 token,
			StreamAppUserID:       1,
			StreamAppAccessToken:  "old-access",
			StreamAppRefreshToken: refreshToken,
			// Within the refresh buffer, so the request refreshes first.
			StreamAppTokenExpiry: now.Add(3 * time.Minute),
		})
	}
	authn := &Authenticator{sessions: store, refresher: svc, now: clock}
	handler := authn.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("the request was served without a refreshed session")
	}))
	serve := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
		req.Header.Set("X-Emby-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	rec := serve("outage-tok")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("store outage = %d Retry-After=%q, want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	if _, ok := store.Get("outage-tok"); !ok {
		t.Fatal("a store outage deleted the compat session")
	}

	if rec := serve("refused-tok"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refused refresh = %d, want 401", rec.Code)
	}
	if _, ok := store.Get("refused-tok"); ok {
		t.Fatal("a refused refresh kept the compat session")
	}
}

// pairRefresher issues a fresh token pair. during, when set, stands in for
// whatever happens elsewhere while the refresh is in flight.
type pairRefresher struct{ during func() }

func (r pairRefresher) Refresh(context.Context, string) (*auth.TokenPair, error) {
	if r.during != nil {
		r.during()
	}
	return &auth.TokenPair{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 900}, nil
}

// A revocation that commits just after the refreshed tokens are stored is
// ordered after this request: it runs with the session as stored, never the
// pre-refresh copy, and the next request is refused.
func TestRequireSession_RevokedAfterRefreshStoredEndsNextRequest(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	repo := &rowSessionRepo{rows: map[string]Session{}}
	store := NewPersistentSessionStore(30*24*time.Hour, clock, repo)
	if err := store.Put(Session{Token: "live-tok", StreamAppUserID: 1, StreamAppAccessToken: "old-access", StreamAppRefreshToken: "refresh", StreamAppTokenExpiry: now.Add(3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	repo.afterUpdate = func() {
		repo.afterUpdate = nil
		delete(repo.rows, "live-tok")
		store.EvictUser(1)
	}
	authn := &Authenticator{sessions: store, refresher: pairRefresher{}, now: clock}
	var served []*Session
	handler := authn.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = append(served, SessionFromContext(r.Context()))
	}))
	serve := func() int {
		req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
		req.Header.Set("X-Emby-Token", "live-tok")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	serve()
	if len(served) != 1 || served[0] == nil || served[0].StreamAppAccessToken != "new-access" {
		t.Fatalf("served sessions = %+v, want one with the refreshed access token", served)
	}
	if code := serve(); code != http.StatusUnauthorized || len(served) != 1 {
		t.Fatalf("request after the revocation = %d (served %d), want 401", code, len(served))
	}
}

// A revocation that commits while the bridged tokens refresh ends the
// request with a 401, and storing the new tokens does not bring the deleted
// session back.
func TestRequireSession_RevokedDuringRefreshIsUnauthorized(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	repo := &rowSessionRepo{rows: map[string]Session{}}
	store := NewPersistentSessionStore(30*24*time.Hour, clock, repo)
	if err := store.Put(Session{Token: "revoked-tok", StreamAppUserID: 1, StreamAppRefreshToken: "refresh", StreamAppTokenExpiry: now.Add(3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	authn := &Authenticator{sessions: store, refresher: pairRefresher{during: func() { clear(repo.rows) }}, now: clock}
	handler := authn.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("the request was served after the session was revoked")
	}))
	req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	req.Header.Set("X-Emby-Token", "revoked-tok")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked during refresh = %d, want 401", rec.Code)
	}
	if len(repo.rows) != 0 {
		t.Fatalf("the refresh wrote the revoked session back: %v", repo.rows)
	}
}

// providerDownRefresher answers every refresh like a sign-in provider that
// could not be reached under the fail_closed outage policy.
type providerDownRefresher struct{}

func (providerDownRefresher) Refresh(context.Context, string) (*auth.TokenPair, error) {
	return nil, fmt.Errorf("re-checking provider identity: %w", auth.ErrProviderUnavailable)
}

// A provider outage refuses this refresh only; it must not end the compat
// session, the same as a native client keeps its session on a 503.
func TestRequireSession_ProviderOutageKeepsTheSession(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	store := NewSessionStore(30*24*time.Hour, clock)
	_ = store.Put(Session{
		Token:                 "provider-tok",
		StreamAppUserID:       1,
		StreamAppRefreshToken: "refresh",
		StreamAppTokenExpiry:  now.Add(3 * time.Minute),
	})
	authn := &Authenticator{sessions: store, refresher: providerDownRefresher{}, now: clock}
	handler := authn.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("the request was served without a refreshed session")
	}))
	req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	req.Header.Set("X-Emby-Token", "provider-tok")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("provider outage = %d Retry-After=%q, want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	if _, ok := store.Get("provider-tok"); !ok {
		t.Fatal("a provider outage deleted the compat session")
	}
}

// outageSessionRepo is a persistent compat session store whose reads fail
// for the token "outage-tok" and find nothing for any other.
type outageSessionRepo struct{ lookups []string }

func (r *outageSessionRepo) Upsert(context.Context, Session) error { return nil }

func (r *outageSessionRepo) UpdateByToken(context.Context, Session) error { return nil }

func (r *outageSessionRepo) GetByToken(_ context.Context, token string, _ time.Time) (*Session, error) {
	r.lookups = append(r.lookups, token)
	if token == "outage-tok" {
		return nil, errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
	}
	return nil, ErrSessionNotFound
}

func (r *outageSessionRepo) DeleteByToken(context.Context, string) error { return nil }

// A node that has not cached a compat session reads it from the database.
// When that read fails, the token was not judged: the request is a
// retryable 503, not the 401 that signs a Jellyfin client out. A token with
// no session, or one Postgres cannot take as text, stays 401.
func TestRequireSession_UncachedSessionStoreOutageIsRetryable(t *testing.T) {
	now := fixedNow()
	repo := &outageSessionRepo{}
	store := NewPersistentSessionStore(30*24*time.Hour, func() time.Time { return now }, repo)
	authn := &Authenticator{sessions: store, now: func() time.Time { return now }}
	handler := authn.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("the request was served without a session")
	}))
	serve := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
		req.Header.Set("X-Emby-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := serve("outage-tok"); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("store outage = %d Retry-After=%q, want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := serve("unknown-tok"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token = %d, want 401", rec.Code)
	}
	if rec := serve("bad\xfftok"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token with invalid UTF-8 = %d, want 401", rec.Code)
	}
	for _, token := range repo.lookups {
		if token == "bad\xfftok" {
			t.Fatal("a token with invalid UTF-8 was sent to the database")
		}
	}
}

// Stream and HLS routes resolve the compat session the same way: a node that
// has not cached it and cannot read it from the database answers a retryable
// 503, whether the request carries the token or only a PlaySessionId. An
// unknown token still gets 401.
func TestPlaybackSessionAuth_UncachedSessionStoreOutageIsRetryable(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	store := NewPersistentSessionStore(30*24*time.Hour, clock, &outageSessionRepo{})
	playbackStore := NewPlaybackSessionStore(time.Hour, clock)
	playbackStore.Put(PlaybackSession{ID: "ps-outage", CompatToken: "outage-tok", RouteItemID: "itm", MediaSources: []PlaybackMediaSource{{ID: "x"}}})
	mw := PlaybackSessionAuth(store, playbackStore, nil)

	cases := []struct {
		name     string
		rawQuery string
		wantCode int
	}{
		{"token", "api_key=outage-tok", http.StatusServiceUnavailable},
		{"PlaySessionId only", "PlaySessionId=ps-outage", http.StatusServiceUnavailable},
		{"unknown token", "api_key=unknown-tok", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := requestWithCompatRouteItem(httptest.NewRequest(http.MethodGet, "/Videos/itm/stream?"+tc.rawQuery, nil), "itm")
			rec := httptest.NewRecorder()
			mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("the request was served without a session")
			})).ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
				t.Fatal("503 without Retry-After")
			}
		})
	}
}

// compatRow is a jellycompat_sessions row as scanCompatSession reads it.
type compatRow struct{ session Session }

func (r compatRow) Scan(dest ...any) error {
	s := r.session
	values := []any{s.Token, s.Username, s.AccountUsername, s.ProfileID, s.ProfileName, s.PseudoUserID,
		s.StreamAppUserID, s.StreamAppAccessToken, s.StreamAppRefreshToken, s.StreamAppTokenExpiry, s.CreatedAt, s.ExpiresAt}
	for i, v := range values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}

// undecryptableRepo returns a stored session sealed under another
// encryption key, read the way SessionRepository reads it.
type undecryptableRepo struct{ outageSessionRepo }

func (r *undecryptableRepo) GetByToken(_ context.Context, token string, _ time.Time) (*Session, error) {
	writer, err := secret.New([]byte("pr1951-old-key-0123456789abcdef0123"))
	if err != nil {
		return nil, err
	}
	reader, err := secret.New([]byte("pr1951-new-key-0123456789abcdef0123"))
	if err != nil {
		return nil, err
	}
	sealed, err := writer.Encrypt("access", jellycompatTokenAAD("streamapp_access_token", token))
	if err != nil {
		return nil, err
	}
	repo := &SessionRepository{cipher: reader}
	return repo.scanCompatSession(compatRow{Session{Token: token, StreamAppAccessToken: sealed, ExpiresAt: fixedNow().Add(time.Hour)}})
}

// A stored compat session that cannot be decrypted (the encryption key
// changed) is not an outage: retrying cannot read it, so the client gets the
// 401 that sends it to sign in again rather than a 503 until it expires.
func TestRequireSession_UndecryptableSessionIsSignedOut(t *testing.T) {
	now := fixedNow()
	store := NewPersistentSessionStore(30*24*time.Hour, func() time.Time { return now }, &undecryptableRepo{})
	authn := &Authenticator{sessions: store, now: func() time.Time { return now }}
	handler := authn.RequireSession(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the request was served with an undecryptable session")
	}))
	req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	req.Header.Set("X-Emby-Token", "sealed-tok")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("undecryptable session = %d, want 401", rec.Code)
	}
}
