package transcodenode

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
)

// The node admits only requests signed for exactly the route they reach, plus
// the previous release's bearer while a deployment upgrades.
func TestNodeRoutesRequireASignatureForTheRequest(t *testing.T) {
	server := newTestServer(t)
	status := func(r *http.Request) int {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, r)
		return recorder.Code
	}

	signed := httptest.NewRequest(http.MethodGet, "/status", nil)
	nodeauthtest.Sign(t, signed, testSecret)
	if code := status(signed); code != http.StatusOK {
		t.Fatalf("signed request: status %d, want 200", code)
	}

	if code := status(httptest.NewRequest(http.MethodGet, "/status", nil)); code != http.StatusUnauthorized {
		t.Fatalf("unsigned request: status %d, want 401", code)
	}

	moved := httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil)
	moved.Header.Set("Authorization", signed.Header.Get("Authorization"))
	if code := status(moved); code != http.StatusUnauthorized {
		t.Fatalf("signature reused on another route: status %d, want 401", code)
	}

	otherSecret := httptest.NewRequest(http.MethodGet, "/status", nil)
	nodeauthtest.Sign(t, otherSecret, "another-secret")
	if code := status(otherSecret); code != http.StatusUnauthorized {
		t.Fatalf("request signed with another secret: status %d, want 401", code)
	}

	// Remove with the legacy bearer in nodeauth.Verify.
	legacy := httptest.NewRequest(http.MethodGet, "/status", nil)
	legacy.Header.Set("Authorization", "Bearer "+testSecret)
	if code := status(legacy); code != http.StatusOK {
		t.Fatalf("legacy bearer during the upgrade window: status %d, want 200", code)
	}
}
