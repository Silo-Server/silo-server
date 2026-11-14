package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// The retired /api/v1 namespace answers 410 client_upgrade_required on its
// root and every descendant, for every method and without looking at
// credentials, while the retained operational probes keep answering.
func TestV1NamespaceIsTombstoned(t *testing.T) {
	server := newUnreachableDBServer(t)
	do := func(method, path string) (int, http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer invalid")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, body
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1"},
		{http.MethodGet, "/api/v1/"},
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodPost, "/api/v1/auth/login"},
		{http.MethodDelete, "/api/v1/devices/d1"},
		{http.MethodGet, "/api/v1/stream/s1"},
		{http.MethodPost, "/api/v1/webhook-sync/webhooks/secret"},
		{http.MethodPost, "/api/v1/health"},
		{http.MethodHead, "/api/v1/catalog"},
		{http.MethodOptions, "/api/v1/auth/me"},
	} {
		status, header, body := do(tc.method, tc.path)
		if status != http.StatusGone || header.Get("Cache-Control") != "no-store" || header.Get("Location") != "" {
			t.Fatalf("%s %s: status %d headers %v", tc.method, tc.path, status, header)
		}
		if tc.method == http.MethodHead {
			if len(body) != 0 {
				t.Fatalf("HEAD %s has a body: %s", tc.path, body)
			}
			continue
		}
		var envelope struct {
			Error   string `json:"error"`
			DocsURL string `json:"docs_url"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error != "client_upgrade_required" || envelope.DocsURL == "" {
			t.Fatalf("%s %s: body %s (%v)", tc.method, tc.path, body, err)
		}
	}

	if status, _, _ := do(http.MethodGet, "/api/v1/health"); status != http.StatusOK {
		t.Fatalf("health probe: %d", status)
	}
	// The database is unreachable, so readiness reports the outage rather
	// than the tombstone.
	if status, _, _ := do(http.MethodGet, "/api/v1/ready"); status != http.StatusServiceUnavailable {
		t.Fatalf("ready probe: %d", status)
	}
}

// Integrations keep calling retired URLs that carry a credential in the path;
// the request log must redact it the way it did for the live routes.
func TestV1TombstoneRedactsPathCredentials(t *testing.T) {
	server := newUnreachableDBServer(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	for _, path := range []string{
		"/api/v1/autoscan/webhooks/synthetic-token-1",
		"/api/v1/plex-sync/webhooks/synthetic-secret-2",
		"/api/v1/webhook-sync/webhooks/synthetic-secret-3",
		"/api/v1/invitations/synthetic-token-4",
		"/api/v1/invitations/synthetic-token-5/accept",
	} {
		response, err := server.Client().Post(server.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusGone {
			t.Fatalf("%s: status %d", path, response.StatusCode)
		}
	}
	if strings.Contains(logs.String(), "synthetic-") {
		t.Fatalf("a path credential reached the log:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "[redacted]") {
		t.Fatalf("no redacted request was logged:\n%s", logs.String())
	}
}
