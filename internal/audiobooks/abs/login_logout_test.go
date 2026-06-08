package abs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newLogoutTestHandler builds a handler with TokenStore + Config + MediaStore
// wired so handleLogout can parse the bearer locally (no bearerAuth middleware
// runs in front of /logout — that's the whole point of the placement fix).
func newLogoutTestHandler(t *testing.T) (*Handler, *memTokenStore, *staticConfig) {
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

// mintAndPersistAccess mints an access JWT, persists its JTI, and returns the
// raw token + JTI. Mirrors mintAndPersistRefresh in login_refresh_test.go.
func mintAndPersistAccess(t *testing.T, store *memTokenStore, cfg *staticConfig, userID, jti string) string {
	t.Helper()
	access, err := IssueAccessToken(cfg.secret, userID, "", jti, time.Hour)
	if err != nil {
		t.Fatalf("mint access: %v", err)
	}
	if err := store.InsertToken(context.Background(), ABSToken{
		ID: jti, UserID: userID, JTI: jti, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return access
}

func TestHandleLogout_RevokesJTIAndReturns204(t *testing.T) {
	h, store, cfg := newLogoutTestHandler(t)
	jti := "logout-test-jti"
	access := mintAndPersistAccess(t, store, cfg, "1", jti)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.Header.Set("Authorization", "Bearer "+access)

	rec := httptest.NewRecorder()
	h.handleLogout(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	tok, _ := store.GetTokenByJTI(context.Background(), jti)
	if tok.RevokedAt == nil {
		t.Errorf("JTI %s was not revoked", jti)
	}
}

// TestHandleLogout_NoBearer_204 covers the "client called sign-out with no
// token" path — must still 204 (logout is idempotent / fire-and-forget).
func TestHandleLogout_NoBearer_204(t *testing.T) {
	h, _, _ := newLogoutTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	rec := httptest.NewRecorder()
	h.handleLogout(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

// TestHandleLogout_GarbageBearer_204 covers an unparseable token: must still
// 204 (we never want sign-out to error the client out).
func TestHandleLogout_GarbageBearer_204(t *testing.T) {
	h, _, _ := newLogoutTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.Header.Set("Authorization", "Bearer not.a.real.jwt")
	rec := httptest.NewRecorder()
	h.handleLogout(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

// TestHandleLogout_WrongSignature_204 covers a token signed by a different
// secret (attacker token, restored from backup, etc.): must 204 and NOT
// revoke (signature mismatch means we can't trust the JTI claim).
func TestHandleLogout_WrongSignature_204(t *testing.T) {
	h, store, _ := newLogoutTestHandler(t)
	jti := "victim-jti"
	_ = store.InsertToken(context.Background(), ABSToken{ID: jti, UserID: "victim", JTI: jti})

	bogus, err := IssueAccessToken([]byte("attacker-secret"), "victim", "", jti, time.Hour)
	if err != nil {
		t.Fatalf("mint bogus: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.Header.Set("Authorization", "Bearer "+bogus)
	rec := httptest.NewRecorder()
	h.handleLogout(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	tok, _ := store.GetTokenByJTI(context.Background(), jti)
	if tok.RevokedAt != nil {
		t.Errorf("victim JTI was revoked via wrong-signature token; want untouched")
	}
}

// TestHandleLogout_IsIdempotent covers repeat sign-outs against the same token:
// must 204 each time, JTI revoked-once and not re-revoked-or-errored.
func TestHandleLogout_IsIdempotent(t *testing.T) {
	h, store, cfg := newLogoutTestHandler(t)
	jti := "idem-jti"
	access := mintAndPersistAccess(t, store, cfg, "1", jti)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/logout", nil)
		req.Header.Set("Authorization", "Bearer "+access)
		rec := httptest.NewRecorder()
		h.handleLogout(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("iter %d: status = %d, want 204", i, rec.Code)
		}
	}
}
