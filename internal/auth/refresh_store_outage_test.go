package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// unreachableStorePool is a pool whose every query fails to connect, as when
// Postgres is down. Nothing listens on port 1, so the dial is refused at once.
func unreachableStorePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), "postgres://silo@127.0.0.1:1/silo?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// A refresh that cannot read the session store has not judged the token: it
// must be ErrSessionCheckUnavailable, which callers answer with a retryable
// 503, and never ErrSessionRevoked or an invalid-token error, which sign the
// client out. Tokens that do not verify are refused before the store is
// read, so an outage cannot turn them into a retry.
func TestRefreshReportsStoreOutageAsUnavailable(t *testing.T) {
	jwt := NewJWTService("refresh-outage-secret", 15*time.Minute, 24*time.Hour)
	pool := unreachableStorePool(t)
	svc := NewService(nil, jwt, NewSessionRepository(pool), NewUserRepository(pool), nil, nil, nil)

	refresh, err := jwt.GenerateRefreshToken(42, "user", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Refresh(t.Context(), refresh)
	if !errors.Is(err, ErrSessionCheckUnavailable) {
		t.Fatalf("Refresh with the store down = %v, want ErrSessionCheckUnavailable", err)
	}
	if errors.Is(err, ErrSessionRevoked) || errors.Is(err, ErrInvalidToken) {
		t.Fatalf("store outage reported as a refused session: %v", err)
	}

	access, err := jwt.GenerateAccessToken(42, "user", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := NewJWTService("another-secret", 15*time.Minute, 24*time.Hour).GenerateRefreshToken(42, "user", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{
		"garbage":                       "not-a-jwt",
		"access token":                  access,
		"refresh token of other secret": foreign,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Refresh(t.Context(), token)
			if err == nil || errors.Is(err, ErrSessionCheckUnavailable) {
				t.Fatalf("Refresh(%s) = %v, want an invalid-token refusal", name, err)
			}
		})
	}
}
