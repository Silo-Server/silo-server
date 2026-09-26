package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// TestRouterAuthDenialEnvelopes checks the shared auth middleware through
// the complete listener, including the outer middleware and v2 delegation.
func TestRouterAuthDenialEnvelopes(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	// Configure the real repositories against an unavailable target so the
	// API-key lookup takes its failure path without requiring a database.
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

	for _, tc := range []struct {
		name    string
		header  string
		legacy  string
		problem apiv2.ProblemType
	}{
		{"missing credential", "", "Missing or malformed authorization header", apiv2.TypeAuthenticationRequired},
		{"malformed header", "Basic invalid", "Missing or malformed authorization header", apiv2.TypeAuthenticationRequired},
		{"invalid JWT", "Bearer invalid", "Invalid or expired token", apiv2.TypeInvalidToken},
		{"failed API key lookup", "Bearer sa_missing", "Invalid API key", apiv2.TypeInvalidToken},
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
				if response.StatusCode != http.StatusUnauthorized {
					t.Fatalf("status = %d, body = %s", response.StatusCode, body)
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
					if response.Header.Get("Content-Type") != "application/json" || len(fields) != 2 || legacy.Error != "unauthorized" || legacy.Message != tc.legacy {
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
