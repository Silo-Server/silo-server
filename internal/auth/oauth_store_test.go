package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestInMemoryOAuthStore_InsertAndGetAndDelete(t *testing.T) {
	st := NewInMemoryOAuthStore()
	ctx := context.Background()

	in := OAuthSession{
		State:         "state-1",
		InstallID:     "42",
		RedirectURI:   "https://example.com/cb",
		ProviderState: []byte(`{"pkce_verifier":"abc"}`),
		NextURL:       "/me",
		ExpiresAt:     time.Now().Add(10 * time.Minute),
	}
	if err := st.Insert(ctx, in); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	out, err := st.GetAndDelete(ctx, "state-1")
	if err != nil {
		t.Fatalf("GetAndDelete: %v", err)
	}
	if out.InstallID != "42" || out.RedirectURI != in.RedirectURI || out.NextURL != "/me" {
		t.Errorf("out = %+v", out)
	}
	if string(out.ProviderState) != `{"pkce_verifier":"abc"}` {
		t.Errorf("ProviderState = %s", out.ProviderState)
	}

	if _, err := st.GetAndDelete(ctx, "state-1"); !errors.Is(err, ErrOAuthSessionNotFound) {
		t.Errorf("expected ErrOAuthSessionNotFound, got %v", err)
	}
}

func TestInMemoryOAuthStore_DefaultsAndValidation(t *testing.T) {
	st := NewInMemoryOAuthStore()
	ctx := context.Background()

	// Missing state — error.
	if err := st.Insert(ctx, OAuthSession{InstallID: "1", RedirectURI: "/x", ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Error("Insert should reject empty state")
	}
	// Missing install_id — error.
	if err := st.Insert(ctx, OAuthSession{State: "s", RedirectURI: "/x", ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Error("Insert should reject empty install_id")
	}
	// Missing expires_at — error.
	if err := st.Insert(ctx, OAuthSession{State: "s", InstallID: "1", RedirectURI: "/x"}); err == nil {
		t.Error("Insert should reject zero expires_at")
	}
	// Missing redirect_uri — error.
	if err := st.Insert(ctx, OAuthSession{State: "s", InstallID: "1", ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Error("Insert should reject empty redirect_uri")
	}

	// next_url and provider_state default.
	if err := st.Insert(ctx, OAuthSession{State: "s2", InstallID: "1", RedirectURI: "/x", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	out, err := st.GetAndDelete(ctx, "s2")
	if err != nil {
		t.Fatalf("GetAndDelete: %v", err)
	}
	if out.NextURL != "/" {
		t.Errorf("NextURL default = %q, want /", out.NextURL)
	}
	if string(out.ProviderState) != "{}" {
		t.Errorf("ProviderState default = %s, want {}", out.ProviderState)
	}
}

func TestInMemoryOAuthStore_DeleteExpired(t *testing.T) {
	st := NewInMemoryOAuthStore()
	ctx := context.Background()
	now := time.Now()

	_ = st.Insert(ctx, OAuthSession{State: "old", InstallID: "i", RedirectURI: "/", ExpiresAt: now.Add(-time.Hour)})
	_ = st.Insert(ctx, OAuthSession{State: "new", InstallID: "i", RedirectURI: "/", ExpiresAt: now.Add(time.Hour)})

	deleted, err := st.DeleteExpired(ctx, now)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	if _, err := st.GetAndDelete(ctx, "new"); err != nil {
		t.Errorf("'new' should still be present: %v", err)
	}
}

func TestInMemoryOAuthStore_DuplicateState(t *testing.T) {
	st := NewInMemoryOAuthStore()
	ctx := context.Background()
	s := OAuthSession{State: "dup", InstallID: "1", RedirectURI: "/", ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.Insert(ctx, s); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	if err := st.Insert(ctx, s); err == nil {
		t.Error("second Insert should reject duplicate state")
	}
}

func TestPGOAuthStore_SealsBoundToTheCode(t *testing.T) {
	st := NewPGOAuthStore(nil, []byte("test-secret"))
	plaintext := "synthetic-provider-answer"
	codeHash := oauthCompletionCodeHash("completion-code")

	ciphertext, err := st.seal([]byte(plaintext), pendingLinkAAD(codeHash))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(ciphertext, plaintext) {
		t.Fatalf("ciphertext includes the plaintext: %q", ciphertext)
	}
	out, err := st.open(ciphertext, pendingLinkAAD(codeHash))
	if err != nil || string(out) != plaintext {
		t.Fatalf("open = %q, %v", out, err)
	}
	if _, err := st.open(ciphertext, pendingLinkAAD(oauthCompletionCodeHash("another-code"))); err == nil {
		t.Fatal("a sealed answer opened under another code")
	}
}
