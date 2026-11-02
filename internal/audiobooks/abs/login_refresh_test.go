package abs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// noopMediaStore satisfies the MediaStore interface with empty returns.
// abs.New panics on a nil MediaStore, so handler tests that don't exercise
// catalog reads pass this to satisfy the contract.
type noopMediaStore struct{}

func (noopMediaStore) GetAudiobookByID(context.Context, string, catalog.AccessFilter) (*models.MediaItem, error) {
	return nil, nil
}
func (noopMediaStore) GetAudiobooksByIDs(context.Context, []string, catalog.AccessFilter) (map[string]*models.MediaItem, error) {
	return map[string]*models.MediaItem{}, nil
}
func (noopMediaStore) ListAudiobooks(context.Context, int64, int, int, catalog.AccessFilter, Filter) ([]*models.MediaItem, int, error) {
	return nil, 0, nil
}
func (noopMediaStore) GetMediaFiles(context.Context, string, catalog.AccessFilter) ([]*models.MediaFile, error) {
	return nil, nil
}
func (noopMediaStore) ListAudiobookLibraries(context.Context, catalog.AccessFilter) ([]AudiobookLibrary, error) {
	return nil, nil
}
func (noopMediaStore) SearchAudiobooks(context.Context, int64, string, int, catalog.AccessFilter) ([]*models.MediaItem, error) {
	return nil, nil
}
func (noopMediaStore) ListContinueListening(context.Context, string, string, int64, int, catalog.AccessFilter) ([]*models.MediaItem, error) {
	return nil, nil
}
func (noopMediaStore) ListRecentlyAdded(context.Context, int64, int, catalog.AccessFilter) ([]*models.MediaItem, error) {
	return nil, nil
}
func (noopMediaStore) ListDiscover(context.Context, int64, int, catalog.AccessFilter) ([]*models.MediaItem, error) {
	return nil, nil
}
func (noopMediaStore) ListLibraryAuthors(context.Context, int64, int, int, string, bool, catalog.AccessFilter) ([]AuthorSummary, int, error) {
	return nil, 0, nil
}
func (noopMediaStore) ListLibrarySeries(context.Context, int64, int, int, catalog.AccessFilter) ([]SeriesSummary, int, error) {
	return nil, 0, nil
}
func (noopMediaStore) GetAuthorByID(context.Context, string, catalog.AccessFilter) (Author, error) {
	return Author{}, ErrNotFound
}
func (noopMediaStore) GetSeriesByName(context.Context, string, catalog.AccessFilter) (Series, error) {
	return Series{}, ErrNotFound
}

// memTokenStore is an in-memory TokenStore for handleRefresh tests.
type memTokenStore struct {
	mu     sync.Mutex
	tokens map[string]ABSToken
}

func newMemTokenStore() *memTokenStore { return &memTokenStore{tokens: map[string]ABSToken{}} }

func (m *memTokenStore) InsertToken(_ context.Context, tok ABSToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[tok.JTI] = tok
	return nil
}
func (m *memTokenStore) GetTokenByJTI(_ context.Context, jti string) (ABSToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[jti]
	if !ok {
		return ABSToken{}, ErrNotFound
	}
	return t, nil
}
func (m *memTokenStore) RevokeTokenByJTI(_ context.Context, jti string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[jti]
	if !ok {
		return nil
	}
	now := time.Now()
	t.RevokedAt = &now
	m.tokens[jti] = t
	return nil
}

// IssueTokens checks a presented JTI, revoking it under Rotate, and stores
// the tokens in one step. A login session is only required, not checked.
func (m *memTokenStore) IssueTokens(_ context.Context, from TokenSource, tokens ...ABSToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if from.JTI == "" && from.LoginSessionID == "" {
		return errors.New("tokens issued with no source credential")
	}
	if from.JTI != "" {
		t, ok := m.tokens[from.JTI]
		if !ok || t.RevokedAt != nil {
			return ErrNotFound
		}
		if from.Rotate {
			now := time.Now()
			t.RevokedAt = &now
			m.tokens[from.JTI] = t
		}
	}
	for _, tok := range tokens {
		m.tokens[tok.JTI] = tok
	}
	return nil
}
func (m *memTokenStore) RevokeTokensForPrincipal(_ context.Context, userID, profileID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for jti, tok := range m.tokens {
		if tok.UserID == userID && tok.ProfileID == profileID && tok.RevokedAt == nil {
			tok.RevokedAt = &now
			m.tokens[jti] = tok
		}
	}
	return nil
}
func (m *memTokenStore) TouchToken(_ context.Context, _ string) error { return nil }

// staticConfig satisfies ConfigProvider with fixed values.
type staticConfig struct{ secret []byte }

func (s *staticConfig) JWTSecret(_ context.Context) ([]byte, error) { return s.secret, nil }
func (s *staticConfig) AccessTTL(_ context.Context) (time.Duration, error) {
	return 24 * time.Hour, nil
}
func (s *staticConfig) RefreshTTL(_ context.Context) (time.Duration, error) {
	return 30 * 24 * time.Hour, nil
}
func (s *staticConfig) StandaloneLoginEnabled(_ context.Context) (bool, error) { return true, nil }

func newRefreshTestHandler(t *testing.T) (*Handler, *memTokenStore, *staticConfig) {
	t.Helper()
	store := newMemTokenStore()
	cfg := &staticConfig{secret: []byte("test-secret-32-bytes-aaaaaaaaaaaaa")}
	h := New(Dependencies{
		Config:     cfg,
		TokenStore: store,
		MediaStore: noopMediaStore{},
	})
	return h, store, cfg
}

func mintAndPersistRefresh(t *testing.T, store *memTokenStore, cfg *staticConfig, userID string) (string, string) {
	t.Helper()
	jti := "test-refresh-jti-" + userID
	refresh, err := IssueRefreshToken(cfg.secret, userID, "", nil, jti, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("mint refresh: %v", err)
	}
	if err := store.InsertToken(context.Background(), ABSToken{
		ID: jti, UserID: userID, JTI: jti, ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return refresh, jti
}

func TestHandleRefresh_HeaderToken_RotatesAndReturnsBothForms(t *testing.T) {
	h, store, cfg := newRefreshTestHandler(t)
	refresh, oldJTI := mintAndPersistRefresh(t, store, cfg, "42")

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("x-refresh-token", refresh)
	rec := httptest.NewRecorder()
	h.handleRefresh(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["accessToken"] == "" || resp["accessToken"] == nil {
		t.Errorf("top-level accessToken missing")
	}
	user, ok := resp["user"].(map[string]any)
	if !ok {
		t.Fatalf("user object missing")
	}
	if user["accessToken"] == "" || user["accessToken"] == nil {
		t.Errorf("user.accessToken missing")
	}
	old, _ := store.GetTokenByJTI(context.Background(), oldJTI)
	if old.RevokedAt == nil {
		t.Errorf("old refresh JTI %s was not revoked", oldJTI)
	}
}

func TestHandleRefresh_AccessTokenRejected(t *testing.T) {
	h, store, cfg := newRefreshTestHandler(t)
	jti := "an-access-jti"
	access, _ := IssueAccessToken(cfg.secret, "9", "", nil, jti, time.Hour)
	_ = store.InsertToken(context.Background(), ABSToken{ID: jti, UserID: "9", JTI: jti, ExpiresAt: time.Now().Add(time.Hour)})

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(""))
	req.Header.Set("x-refresh-token", access)
	rec := httptest.NewRecorder()
	h.handleRefresh(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRefresh_RevokedTokenRejected(t *testing.T) {
	h, store, cfg := newRefreshTestHandler(t)
	refresh, oldJTI := mintAndPersistRefresh(t, store, cfg, "7")
	_ = store.RevokeTokenByJTI(context.Background(), oldJTI)

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("x-refresh-token", refresh)
	rec := httptest.NewRecorder()
	h.handleRefresh(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestHandleRefresh_ResolvesUsername verifies that refresh responses expose
// the resolved username instead of the internal user ID.
func TestHandleRefresh_ResolvesUsername(t *testing.T) {
	// Given
	h, store, cfg := newRefreshTestHandler(t)
	h.deps.UsernameResolver = func(context.Context, string, string) string { return "sara" }
	refresh, _ := mintAndPersistRefresh(t, store, cfg, "15")

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("x-refresh-token", refresh)
	rec := httptest.NewRecorder()

	// When
	h.handleRefresh(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	user, ok := resp["user"].(map[string]any)
	if !ok {
		t.Fatalf("user object missing")
	}
	if user["username"] != "sara" {
		t.Errorf("username = %v, want sara", user["username"])
	}
	if user["id"] != "15" {
		t.Errorf("id = %v, want 15", user["id"])
	}
}

func TestHandleABSAuthorize_ResolvesUsername(t *testing.T) {
	// Given
	h, _, _ := newRefreshTestHandler(t)
	h.deps.UsernameResolver = func(context.Context, string, string) string { return "sara" }

	req := httptest.NewRequest(http.MethodPost, "/api/authorize", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, ctxAuth{
		UserID:    "15",
		ProfileID: "profile-1",
		Token:     "access.jwt",
	}))
	rec := httptest.NewRecorder()

	// When
	h.handleABSAuthorize(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	user, ok := env["user"].(map[string]any)
	if !ok {
		t.Fatalf("user is not a map: %T", env["user"])
	}
	if user["username"] != "sara" {
		t.Errorf("username = %v, want sara", user["username"])
	}
	if user["id"] != "15" {
		t.Errorf("id = %v, want 15", user["id"])
	}
}
