package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// newUnreachableDBServer serves the complete listener with the real
// repositories configured against an unavailable database, so every lookup
// fails without requiring one. Request rate limiting is off.
func newUnreachableDBServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	cipher, err := secret.New(bytes.Repeat([]byte{0}, secret.MinMasterKeyLen))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewRouter(Dependencies{
		Config:           cfg,
		AppContext:       t.Context(),
		DB:               pool,
		SecretCipher:     cipher,
		ClientIPResolver: clientip.NewResolver(nil),
		NodeID:           "auth-envelope-test",
		PublicURL:        "https://silo.example.test",
	}))
	t.Cleanup(server.Close)
	return server
}

// TestRouterAuthDenialEnvelopes checks the shared auth middleware through
// the complete listener, including the outer middleware and v2 delegation.
func TestRouterAuthDenialEnvelopes(t *testing.T) {
	// The API-key lookup fails on the unavailable database. A lookup that
	// fails judges nothing about the key, so it is a retryable 503, not a 401.
	server := newUnreachableDBServer(t)

	for _, tc := range []struct {
		name        string
		header      string
		legacyError string
		legacy      string
		problem     apiv2.ProblemType
	}{
		{"missing credential", "", "unauthorized", "Missing or malformed authorization header", apiv2.TypeAuthenticationRequired},
		{"malformed header", "Basic invalid", "unauthorized", "Missing or malformed authorization header", apiv2.TypeAuthenticationRequired},
		{"invalid JWT", "Bearer invalid", "unauthorized", "Invalid or expired token", apiv2.TypeInvalidToken},
		{"failed API key lookup", "Bearer sa_missing", "service_unavailable", "Sign-in could not be checked right now; try again shortly", apiv2.TypeDependencyUnavailable},
	} {
		for _, path := range []string{"/api/v1/auth/me", "/api/v2/account/me"} {
			t.Run(tc.name+path, func(t *testing.T) {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", tc.header)
				req.Header.Set("X-Request-ID", "client-selected")
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = response.Body.Close() }()
				body, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != tc.problem.Status {
					t.Fatalf("status = %d, body = %s", response.StatusCode, body)
				}
				if tc.problem.Status == http.StatusServiceUnavailable && response.Header.Get("Retry-After") == "" {
					t.Fatalf("503 without Retry-After: headers = %v", response.Header)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(body, &fields); err != nil {
					t.Fatalf("invalid JSON: %v: %s", err, body)
				}
				if path == "/api/v1/auth/me" {
					var legacy struct{ Error, Message string }
					if err := json.Unmarshal(body, &legacy); err != nil {
						t.Fatal(err)
					}
					if response.Header.Get("Content-Type") != "application/json" || len(fields) != 2 || legacy.Error != tc.legacyError || legacy.Message != tc.legacy {
						t.Fatalf("legacy denial changed: headers = %v, body = %s", response.Header, body)
					}
					return
				}
				var problem apiv2.Problem
				if err := json.Unmarshal(body, &problem); err != nil {
					t.Fatal(err)
				}
				requestID := response.Header.Get(apiv2.RequestIDHeader)
				if response.Header.Get("Content-Type") != "application/problem+json" || response.Header.Get("Cache-Control") != "no-store" {
					t.Fatalf("problem headers = %v", response.Header)
				}
				if len(fields) != 5 || problem.Type != tc.problem.URI() || problem.Title != tc.problem.Title || problem.Status != response.StatusCode || problem.Detail == "" {
					t.Fatalf("invalid problem envelope: %s", body)
				}
				if requestID == "" || requestID == "client-selected" || problem.Instance != "urn:silo:request:"+requestID {
					t.Fatalf("request ID = %q, problem instance = %q", requestID, problem.Instance)
				}
			})
		}
	}
}

// Password sign-in spends one attempt budget per sign-in name, on v1 and v2
// alike and with request rate limiting off, so once a name has used its ten
// attempts the next is refused with 429 and Retry-After, while other names
// keep their own budget. The attempts here fail on the unavailable database;
// they count all the same.
func TestRouterLimitsSignInAttemptsPerName(t *testing.T) {
	server := newUnreachableDBServer(t)
	signIn := func(path, name string) (status int, retryAfter string) {
		t.Helper()
		body := `{"username":"` + name + `","password":"guess"}`
		response, err := server.Client().Post(server.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		return response.StatusCode, response.Header.Get("Retry-After")
	}

	const budget = 10 // ratelimit.SignInNamePolicy
	paths := []string{"/api/v1/auth/login", "/api/v2/auth/login"}
	for i := range budget {
		if status, _ := signIn(paths[i%2], "alice"); status == http.StatusTooManyRequests {
			t.Fatalf("attempt %d refused, want %d attempts allowed", i+1, budget)
		}
	}
	for _, path := range paths {
		if status, retryAfter := signIn(path, "alice"); status != http.StatusTooManyRequests || retryAfter == "" {
			t.Fatalf("%s after the budget: status = %d, Retry-After = %q; want 429 with Retry-After", path, status, retryAfter)
		}
	}
	if status, _ := signIn(paths[1], "bob"); status == http.StatusTooManyRequests {
		t.Fatal("another name was refused")
	}
}
