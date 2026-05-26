package abs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleLogout_RevokesJTIAndReturns204(t *testing.T) {
	store := newMemTokenStore()
	jti := "logout-test-jti"
	_ = store.InsertToken(context.Background(), ABSToken{ID: jti, UserID: "1", JTI: jti})

	h := New(Dependencies{TokenStore: store})
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	// Simulate bearerAuth having populated the context.
	ctx := context.WithValue(req.Context(), ctxKey{}, ctxAuth{
		UserID: "1", JTI: jti, Token: "doesnt-matter",
	})
	req = req.WithContext(ctx)

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

func TestHandleLogout_NoAuthContext_204(t *testing.T) {
	store := newMemTokenStore()
	h := New(Dependencies{TokenStore: store})
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	rec := httptest.NewRecorder()
	h.handleLogout(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestHandleLogout_NilTokenStore_204(t *testing.T) {
	h := New(Dependencies{})
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	ctx := context.WithValue(req.Context(), ctxKey{}, ctxAuth{UserID: "1", JTI: "x"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.handleLogout(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestHandleLogout_IsIdempotent(t *testing.T) {
	store := newMemTokenStore()
	jti := "idem-jti"
	_ = store.InsertToken(context.Background(), ABSToken{ID: jti, UserID: "1", JTI: jti})
	h := New(Dependencies{TokenStore: store})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/logout", nil)
		ctx := context.WithValue(req.Context(), ctxKey{}, ctxAuth{UserID: "1", JTI: jti})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.handleLogout(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("iter %d: status = %d, want 204", i, rec.Code)
		}
	}
}
