package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// newOutageAuthHandler is an AuthHandler whose session store refuses every
// connection, as when Postgres is down.
func newOutageAuthHandler(t *testing.T) (*AuthHandler, *auth.JWTService) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), "postgres://silo@127.0.0.1:1/silo?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	jwt := auth.NewJWTService("refresh-outage-secret", 15*time.Minute, 24*time.Hour)
	svc := auth.NewService(nil, jwt, auth.NewSessionRepository(pool), auth.NewUserRepository(pool), nil, nil, nil)
	return NewAuthHandler(svc, jwt, nil), jwt
}

// A refresh that could not reach the session store keeps the client signed
// in: 503 service_unavailable with Retry-After, carrying
// auth.ErrSessionCheckUnavailable for the v2 listener. A token that does not
// verify is still 401 invalid_token.
func TestRefreshStoreOutageIsRetryable(t *testing.T) {
	h, jwt := newOutageAuthHandler(t)
	refresh, err := jwt.GenerateRefreshToken(42, "user", "sess-1")
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.Refresh(t.Context(), refresh)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable || apiErr.Code != apimw.CodeServiceUnavailable {
		t.Fatalf("Refresh with the store down = %v, want 503 %s", err, apimw.CodeServiceUnavailable)
	}
	if apiErr.RetryAfter != apimw.CredentialCheckRetryAfterSeconds || !errors.Is(err, auth.ErrSessionCheckUnavailable) {
		t.Fatalf("retry_after=%d cause matches=%v", apiErr.RetryAfter, errors.Is(err, auth.ErrSessionCheckUnavailable))
	}
	if strings.Contains(apiErr.Message, "refused") {
		t.Fatalf("message leaks the store error: %q", apiErr.Message)
	}

	_, err = h.Refresh(t.Context(), "not-a-jwt")
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || apiErr.Code != "invalid_token" {
		t.Fatalf("Refresh(garbage) = %v, want 401 invalid_token", err)
	}

	// The v1 route renders the same decision with its Retry-After header.
	rec := httptest.NewRecorder()
	h.HandleRefresh(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", strings.NewReader(`{"refresh_token":"`+refresh+`"}`)))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != strconv.Itoa(apimw.CredentialCheckRetryAfterSeconds) {
		t.Fatalf("v1 refresh = %d Retry-After=%q: %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error != apimw.CodeServiceUnavailable {
		t.Fatalf("v1 refresh body = %s", rec.Body.String())
	}
}
