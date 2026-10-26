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

	"github.com/go-chi/chi/v5"
)

// pinVerifier is a ProfileVerifier over one profile whose PIN revision the
// test moves, as setting or changing the PIN would.
type pinVerifier struct {
	mu       sync.Mutex
	revision int64
	err      error
}

func (v *pinVerifier) VerifyTokenProfile(_ context.Context, _, _ string, pinRevision *int64) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return v.err
	}
	if pinRevision == nil || *pinRevision != v.revision {
		return ErrTokenProfileUnverified
	}
	return nil
}

func (v *pinVerifier) set(revision int64, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.revision, v.err = revision, err
}

type principalValidator struct{ principal Principal }

func (v principalValidator) Validate(context.Context, string, string) (Principal, error) {
	return v.principal, nil
}

func newProfilePINRouter(t *testing.T, verifier *pinVerifier) (http.Handler, *staticConfig) {
	t.Helper()
	cfg := &staticConfig{secret: []byte("test-secret-32-bytes-aaaaaaaaaaaaa")}
	h := New(Dependencies{
		Config:          cfg,
		TokenStore:      newMemTokenStore(),
		MediaStore:      noopMediaStore{},
		CredValidator:   principalValidator{Principal{UserID: "1", ProfileID: "parent", DisplayName: "Parent", PINRevision: 3}},
		ProfileVerifier: verifier,
	})
	r := chi.NewRouter()
	h.Mount(r)
	return r, cfg
}

func serveJSON(t *testing.T, router http.Handler, req *http.Request) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func tokenPINRevision(t *testing.T, cfg *staticConfig, token string) *int64 {
	t.Helper()
	claims, err := ParseToken(cfg.secret, token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	return claims.PINRevision
}

// Tokens carry the PIN revision the sign-in checked through refresh and
// /authorize, and every authenticated request and refresh stops accepting
// them once the profile's PIN revision moves.
func TestTokensEndWhenProfilePINChanges(t *testing.T) {
	verifier := &pinVerifier{revision: 3}
	router, cfg := newProfilePINRouter(t, verifier)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"alice#Parent","password":"pw#1234"}`))
	req.Header.Set("x-return-tokens", "true")
	code, body := serveJSON(t, router, req)
	if code != http.StatusOK {
		t.Fatalf("login status = %d", code)
	}
	access, _ := body["accessToken"].(string)
	refresh, _ := body["refreshToken"].(string)
	for name, tok := range map[string]string{"access": access, "refresh": refresh} {
		if rev := tokenPINRevision(t, cfg, tok); rev == nil || *rev != 3 {
			t.Fatalf("login %s token pin_rev = %v, want 3", name, rev)
		}
	}

	authorize := func(token string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/api/authorize", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return serveJSON(t, router, req)
	}
	refreshWith := func(token string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
		req.Header.Set("x-refresh-token", token)
		return serveJSON(t, router, req)
	}

	code, body = authorize(access)
	if code != http.StatusOK {
		t.Fatalf("authorize at the signed-in revision = %d", code)
	}
	if rev := tokenPINRevision(t, cfg, body["accessToken"].(string)); rev == nil || *rev != 3 {
		t.Fatalf("authorize token pin_rev = %v, want 3", rev)
	}
	code, body = refreshWith(refresh)
	if code != http.StatusOK {
		t.Fatalf("refresh at the signed-in revision = %d", code)
	}
	access, refresh = body["accessToken"].(string), body["refreshToken"].(string)
	for name, tok := range map[string]string{"access": access, "refresh": refresh} {
		if rev := tokenPINRevision(t, cfg, tok); rev == nil || *rev != 3 {
			t.Fatalf("refreshed %s token pin_rev = %v, want 3", name, rev)
		}
	}

	verifier.set(3, errors.New("database down"))
	if code, _ := authorize(access); code != http.StatusInternalServerError {
		t.Fatalf("authorize while the check fails = %d, want 500", code)
	}

	verifier.set(4, nil)
	if code, _ := authorize(access); code != http.StatusUnauthorized {
		t.Fatalf("authorize after the PIN changed = %d, want 401", code)
	}
	if code, _ := refreshWith(refresh); code != http.StatusUnauthorized {
		t.Fatalf("refresh after the PIN changed = %d, want 401", code)
	}
}

// A sign-in refused for the profile's PIN tells the client how to sign in,
// and never as an outage.
func TestLoginProfilePINRefusals(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{ErrProfilePINRequired, "username#profile with password#PIN"},
		{ErrProfilePINIncorrect, "incorrect profile PIN"},
		{ErrProfilePINLocked, "too many incorrect PINs"},
	} {
		h := New(Dependencies{
			Config:        &staticConfig{secret: []byte("test-secret-32-bytes-aaaaaaaaaaaaa")},
			TokenStore:    newMemTokenStore(),
			MediaStore:    noopMediaStore{},
			CredValidator: refusingValidator{err: tc.err},
		})
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"alice","password":"pw"}`))
		rec := httptest.NewRecorder()
		h.handleLogin(rec, req)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%v: status %d body %q, want 401 containing %q", tc.err, rec.Code, rec.Body.String(), tc.want)
		}
	}
}
