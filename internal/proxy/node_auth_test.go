package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
)

// The proxy's admin routes admit only signed requests, plus the previous
// release's bearer while a deployment upgrades.
func TestProxyAdminRoutesRequireASignature(t *testing.T) {
	const secret = "proxy-admin-secret"
	server := newCapabilityProxyServer(t, secret)
	status := func(r *http.Request) int {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, r)
		return recorder.Code
	}

	signed := httptest.NewRequest(http.MethodGet, "/status", nil)
	nodeauthtest.Sign(t, signed, secret)
	if code := status(signed); code != http.StatusOK {
		t.Fatalf("signed request: status %d, want 200", code)
	}
	if code := status(httptest.NewRequest(http.MethodGet, "/status", nil)); code != http.StatusUnauthorized {
		t.Fatalf("unsigned request: status %d, want 401", code)
	}
	wrong := httptest.NewRequest(http.MethodGet, "/status", nil)
	wrong.Header.Set("Authorization", "Bearer not-the-secret")
	if code := status(wrong); code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: status %d, want 401", code)
	}

	// Remove with the legacy bearer in nodeauth.Verify.
	legacy := httptest.NewRequest(http.MethodGet, "/status", nil)
	legacy.Header.Set("Authorization", "Bearer "+secret)
	if code := status(legacy); code != http.StatusOK {
		t.Fatalf("legacy bearer during the upgrade window: status %d, want 200", code)
	}
}
