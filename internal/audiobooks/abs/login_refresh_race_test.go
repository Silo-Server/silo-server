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

// barrierStore wraps memTokenStore and gates the FIRST IssueTokens call on
// `release` so both goroutines in the concurrent test can complete their
// initial GetTokenByJTI lookup before either rotation fires. Without this,
// the Go scheduler can serialize the test enough that goroutine B's
// GetTokenByJTI sees A's already-revoked old JTI and 401s — which is a
// real race-loser scenario in production but not the case where both
// requests reach the rotation.
type barrierStore struct {
	*memTokenStore
	release    chan struct{}
	gateOnce   sync.Once
	gateClosed chan struct{}
}

func (b *barrierStore) IssueTokens(ctx context.Context, from TokenSource, tokens ...ABSToken) error {
	b.gateOnce.Do(func() {
		<-b.release
		close(b.gateClosed)
	})
	return b.memTokenStore.IssueTokens(ctx, from, tokens...)
}

func TestHandleRefresh_ConcurrentRotations_OnlyOneSucceeds(t *testing.T) {
	cfg := &staticConfig{secret: []byte("test-secret-32-bytes-aaaaaaaaaaaaa")}
	mem := newMemTokenStore()
	gate := &barrierStore{memTokenStore: mem, release: make(chan struct{}), gateClosed: make(chan struct{})}
	h := New(Dependencies{Config: cfg, TokenStore: gate, MediaStore: noopMediaStore{}})

	jti := "race-old-jti"
	refresh, err := IssueRefreshToken(cfg.secret, "race-user", "", nil, jti, 30*24*time.Hour)
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

	// Give both goroutines time to reach the rotation barrier, then
	// release the gate so one rotation wins and the other sees a revoked JTI.
	time.Sleep(50 * time.Millisecond)
	close(gate.release)
	wg.Wait()
	close(results)

	tokens := map[string]bool{}
	successes := 0
	unauthorized := 0
	for r := range results {
		switch r.code {
		case http.StatusOK:
			successes++
		case http.StatusUnauthorized:
			unauthorized++
		default:
			t.Errorf("concurrent refresh got code %d; want 200 or 401", r.code)
		}
		if r.access != "" {
			tokens[r.access] = true
		}
	}
	if successes != 1 || unauthorized != 1 {
		t.Errorf("got successes=%d unauthorized=%d; want 1 each", successes, unauthorized)
	}
	if len(tokens) != 1 {
		t.Errorf("got %d distinct access tokens; want 1", len(tokens))
	}
	old, _ := mem.GetTokenByJTI(context.Background(), jti)
	if old.RevokedAt == nil {
		t.Errorf("old refresh JTI %s not revoked after concurrent rotations", jti)
	}
}

// failingIssueStore wraps memTokenStore but fails every IssueTokens call
// without writing, as a rotation transaction that rolls back does.
type failingIssueStore struct{ *memTokenStore }

func (failingIssueStore) IssueTokens(context.Context, TokenSource, ...ABSToken) error {
	return errors.New("rotation unavailable")
}

// TestHandleRefresh_RotationFailure_OldTokenStillValid: when the rotation
// fails, handleRefresh returns 500 and the OLD refresh JTI is left untouched
// (still valid), so the client can retry with it.
func TestHandleRefresh_RotationFailure_OldTokenStillValid(t *testing.T) {
	cfg := &staticConfig{secret: []byte("test-secret-32-bytes-aaaaaaaaaaaaa")}
	mem := newMemTokenStore()
	store := failingIssueStore{memTokenStore: mem}

	h := New(Dependencies{
		Config:     cfg,
		TokenStore: store,
		MediaStore: noopMediaStore{},
	})

	jti := "old-refresh-jti"
	refresh, err := IssueRefreshToken(cfg.secret, "u1", "", nil, jti, 30*24*time.Hour)
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
		t.Errorf("old refresh JTI was revoked despite the rotation error")
	}
}
