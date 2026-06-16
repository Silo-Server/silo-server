package abs

import (
	"bytes"
	"context"
	"encoding/json"
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
func (noopMediaStore) ListAudiobooks(context.Context, int64, int, int, catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	return nil, 0, nil
}
func (noopMediaStore) GetMediaFiles(context.Context, string, catalog.AccessFilter) ([]*models.MediaFile, error) {
	return nil, nil
}
func (noopMediaStore) GetMediaFileByID(context.Context, int) (*models.MediaFile, error) {
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
func (noopMediaStore) ListLibraryAuthors(context.Context, int64, int, catalog.AccessFilter) ([]AuthorSummary, error) {
	return nil, nil
}
func (noopMediaStore) ListLibrarySeries(context.Context, int64, int, catalog.AccessFilter) ([]SeriesSummary, error) {
	return nil, nil
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
func (m *memTokenStore) RevokeTokenIfActive(_ context.Context, jti string) (ABSToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[jti]
	if !ok || t.RevokedAt != nil {
		return ABSToken{}, ErrNotFound
	}
	now := time.Now()
	t.RevokedAt = &now
	m.tokens[jti] = t
	return t, nil
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
	refresh, err := IssueRefreshToken(cfg.secret, userID, "", jti, 30*24*time.Hour)
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

func TestHandleRefresh_BodyToken_Works(t *testing.T) {
	h, store, cfg := newRefreshTestHandler(t)
	refresh, _ := mintAndPersistRefresh(t, store, cfg, "1")

	body := bytes.NewBufferString(`{"refreshToken":"` + refresh + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handleRefresh(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRefresh_NoToken_400(t *testing.T) {
	h, _, _ := newRefreshTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	rec := httptest.NewRecorder()
	h.handleRefresh(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRefresh_AccessTokenRejected(t *testing.T) {
	h, store, cfg := newRefreshTestHandler(t)
	jti := "an-access-jti"
	access, _ := IssueAccessToken(cfg.secret, "9", "", jti, time.Hour)
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
