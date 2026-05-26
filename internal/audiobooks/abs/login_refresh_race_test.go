package abs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// barrierStore wraps memTokenStore and gates the FIRST RevokeTokenByJTI
// call on `release` so both goroutines in the concurrent test can complete
// their initial GetTokenByJTI lookup before either revoke fires. Without
// this, the Go scheduler can serialize the test enough that goroutine B's
// GetTokenByJTI sees A's already-revoked old JTI and 401s — which is a
// real race-loser scenario in production but not the case the canonical's
// documented "both succeed" semantics describe.
type barrierStore struct {
	*memTokenStore
	release    chan struct{}
	gateOnce   sync.Once
	gateClosed chan struct{}
}

func (b *barrierStore) RevokeTokenByJTI(ctx context.Context, jti string) error {
	b.gateOnce.Do(func() {
		<-b.release
		close(b.gateClosed)
	})
	return b.memTokenStore.RevokeTokenByJTI(ctx, jti)
}

// TestHandleRefresh_ConcurrentRotations_BothSucceed exercises the documented
// race behavior: two clients presenting the same refresh token whose
// GetTokenByJTI lookups both land before either revoke MUST both receive
// 200 with a distinct new pair. The old JTI ends up revoked exactly once;
// both new pairs remain valid until natural TTL expiry. Mirrors
// continuum-plugin-audiobooks's documented behavior at handler.go:775-852.
func TestHandleRefresh_ConcurrentRotations_BothSucceed(t *testing.T) {
	cfg := &staticConfig{secret: []byte("test-secret-32-bytes-aaaaaaaaaaaaa")}
	mem := newMemTokenStore()
	gate := &barrierStore{memTokenStore: mem, release: make(chan struct{}), gateClosed: make(chan struct{})}
	h := New(Dependencies{Config: cfg, TokenStore: gate, MediaStore: noopMediaStore{}})

	jti := "race-old-jti"
	refresh, err := IssueRefreshToken(cfg.secret, "race-user", "", jti, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := mem.InsertToken(context.Background(), ABSToken{
		ID: jti, UserID: "race-user", JTI: jti, ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	type result struct {
		code   int
		access string
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
			req.Header.Set("x-refresh-token", refresh)
			rec := httptest.NewRecorder()
			h.handleRefresh(rec, req)
			access := ""
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
				if s, ok := resp["accessToken"].(string); ok {
					access = s
				}
			}
			results <- result{code: rec.Code, access: access}
		}()
	}

	// Give both goroutines time to reach the barrier (Get → mint → 2x Insert
	// → first Revoke), then release the gate so both revokes proceed.
	time.Sleep(50 * time.Millisecond)
	close(gate.release)
	wg.Wait()
	close(results)

	tokens := map[string]bool{}
	for r := range results {
		if r.code != http.StatusOK {
			t.Errorf("concurrent refresh got code %d; want 200", r.code)
		}
		if r.access != "" {
			tokens[r.access] = true
		}
	}
	if len(tokens) != 2 {
		t.Errorf("got %d distinct access tokens; want 2", len(tokens))
	}
	old, _ := mem.GetTokenByJTI(context.Background(), jti)
	if old.RevokedAt == nil {
		t.Errorf("old refresh JTI %s not revoked after concurrent rotations", jti)
	}
}

// failingRevokeStore wraps memTokenStore but returns an error from
// RevokeTokenByJTI. Used to exercise the partial-failure path in
// handleRefresh: if the revoke of the old JTI errors, the new tokens have
// already been persisted but the old token also remains valid — the
// canonical accepts this trade rather than rolling back partial writes.
type failingRevokeStore struct{ *memTokenStore }

func (failingRevokeStore) RevokeTokenByJTI(context.Context, string) error {
	return errors.New("revoke unavailable")
}

// TestHandleRefresh_RevokeFailure_OldTokenStillValid covers the documented
// partial-failure semantics: when revoke errors, handleRefresh returns 500
// and the OLD refresh JTI is left untouched (still valid). The client can
// retry with the same old token; the new tokens that were persisted
// before the revoke fail become orphaned but harmless (TTL'd out).
func TestHandleRefresh_RevokeFailure_OldTokenStillValid(t *testing.T) {
	cfg := &staticConfig{secret: []byte("test-secret-32-bytes-aaaaaaaaaaaaa")}
	mem := newMemTokenStore()
	store := failingRevokeStore{memTokenStore: mem}

	h := New(Dependencies{
		Config:     cfg,
		TokenStore: store,
		MediaStore: noopMediaStore{},
	})

	jti := "old-refresh-jti"
	refresh, err := IssueRefreshToken(cfg.secret, "u1", "", jti, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("mint refresh: %v", err)
	}
	if err := mem.InsertToken(context.Background(), ABSToken{
		ID: jti, UserID: "u1", JTI: jti, ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("x-refresh-token", refresh)
	rec := httptest.NewRecorder()
	h.handleRefresh(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (revoke failure)", rec.Code)
	}
	// Old JTI must remain unrevoked so the client can retry the rotation.
	old, _ := mem.GetTokenByJTI(context.Background(), jti)
	if old.RevokedAt != nil {
		t.Errorf("old refresh JTI was revoked despite RevokeTokenByJTI error")
	}
}
