package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A credential check that fails in the store (Postgres unreachable, a
// timeout) judges nothing about the credential. Answering it with a 401 tells
// every client its sign-in is gone, so a short database outage would sign
// them all out. These tests pin the split: a session or key the store says is
// gone stays a 401; one the store could not look up is a retryable 503.

var errStoreDown = errors.New("dial tcp: connection refused")

// apiKeyStore answers one key, reports any other as unknown, and fails every
// lookup when err is set.
type apiKeyStore struct {
	key *models.APIKey
	err error
}

func (s apiKeyStore) GetByKey(_ context.Context, key string) (*models.APIKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.key != nil && s.key.Key == key {
		return s.key, nil
	}
	return nil, auth.ErrAPIKeyNotFound
}

func (apiKeyStore) UpdateLastUsed(context.Context, int64) error { return nil }

// accountStore answers one account, reports any other as not found the way
// the repository does, and fails every lookup when err is set.
type accountStore struct {
	user *models.User
	err  error
}

func (s accountStore) GetByID(_ context.Context, id int) (*models.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.user != nil && s.user.ID == id {
		return s.user, nil
	}
	return nil, fmt.Errorf("getting user: %w", auth.ErrNotFound)
}

func requireCredentialCheckUnavailable(t *testing.T, rec *reasonWriter, reached bool) {
	t.Helper()
	if reached {
		t.Fatal("the handler ran although the credential was never checked")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if got := decodeDenial(t, rec); got.Error != CodeServiceUnavailable {
		t.Fatalf("error = %q, want %q", got.Error, CodeServiceUnavailable)
	}
	if got := rec.Header().Get("Retry-After"); got != strconv.Itoa(CredentialCheckRetryAfterSeconds) {
		t.Fatalf("Retry-After = %q", got)
	}
	// No 401 reason: internal/apiv2 must not translate this into a sign-out.
	if rec.reason != "" {
		t.Fatalf("reason = %q, want none", rec.reason)
	}
}

func TestRequireAuthAnswers503WhenTheSessionCannotBeChecked(t *testing.T) {
	claims := auth.Claims{UserID: 42, Role: models.RoleUser, SessionID: "sess", TokenType: auth.TokenTypeAccess}
	serve := func(sessions *fakeSessionValidator) (*reasonWriter, bool) {
		reached := false
		am := NewAuthMiddleware(claimsValidator{claims}, sessions, nil, nil)
		h := am.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusNoContent)
		}))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		req.Header.Set("Authorization", "Bearer token")
		rec := newReasonWriter()
		h.ServeHTTP(rec, req)
		return rec, reached
	}

	t.Run("store failure", func(t *testing.T) {
		rec, reached := serve(&fakeSessionValidator{err: errStoreDown})
		requireCredentialCheckUnavailable(t, rec, reached)
	})
	t.Run("deadline", func(t *testing.T) {
		rec, reached := serve(&fakeSessionValidator{err: fmt.Errorf("checking session validity: %w", context.DeadlineExceeded)})
		requireCredentialCheckUnavailable(t, rec, reached)
	})
	t.Run("inactive session is still a 401", func(t *testing.T) {
		rec, reached := serve(&fakeSessionValidator{roles: map[string]string{}})
		if reached || rec.Code != http.StatusUnauthorized || rec.reason != ReasonSessionInvalid {
			t.Fatalf("reached=%v status=%d reason=%q", reached, rec.Code, rec.reason)
		}
		if rec.Header().Get("Retry-After") != "" {
			t.Fatal("a refused session carries no Retry-After")
		}
	})
}

func TestRequireAuthAnswers503WhenAnAPIKeyCannotBeChecked(t *testing.T) {
	key := &models.APIKey{ID: 1, UserID: 7, Key: "sa_test"}
	owner := &models.User{ID: 7, Role: models.RoleUser, Enabled: true}
	tests := []struct {
		name       string
		keys       apiKeyStore
		users      accountStore
		wantStatus int
	}{
		{name: "key lookup fails", keys: apiKeyStore{err: errStoreDown}, users: accountStore{user: owner}, wantStatus: http.StatusServiceUnavailable},
		{name: "owner lookup fails", keys: apiKeyStore{key: key}, users: accountStore{err: errStoreDown}, wantStatus: http.StatusServiceUnavailable},
		{name: "unknown key", keys: apiKeyStore{}, users: accountStore{user: owner}, wantStatus: http.StatusUnauthorized},
		{name: "owner gone", keys: apiKeyStore{key: key}, users: accountStore{}, wantStatus: http.StatusUnauthorized},
		{name: "ok", keys: apiKeyStore{key: key}, users: accountStore{user: owner}, wantStatus: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			am := NewAuthMiddleware(claimsValidator{}, &fakeSessionValidator{}, tt.keys, tt.users)
			h := am.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			req.Header.Set("Authorization", "Bearer sa_test")
			rec := newReasonWriter()
			h.ServeHTTP(rec, req)
			if tt.wantStatus == http.StatusServiceUnavailable {
				requireCredentialCheckUnavailable(t, rec, reached)
				return
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestApplePushDisplayAuthAnswers503WhenTheSessionCannotBeChecked(t *testing.T) {
	jwt := auth.NewJWTService("test-secret", 15*time.Minute, 7*24*time.Hour)
	display, _, err := jwt.GenerateApplePushDisplayToken(42, models.RoleUser, "sess-live", "profile-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	am := NewAuthMiddleware(jwt, &fakeSessionValidator{err: errStoreDown}, nil, nil)
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	})
	h := am.RequireApplePushDisplayAuth(am.RequireAuth, nil)(next)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/apple-push/display", nil)
	req.Header.Set("Authorization", "Bearer "+display)
	rec := newReasonWriter()
	h.ServeHTTP(rec, req)
	requireCredentialCheckUnavailable(t, rec, reached)
}
