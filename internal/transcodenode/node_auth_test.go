package transcodenode

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
)

// The node admits only requests signed with its own key for exactly the route
// they reach. No sender ever held the node's key to send as a bearer, so the
// legacy bearer is refused.
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

	legacy := httptest.NewRequest(http.MethodGet, "/status", nil)
	legacy.Header.Set("Authorization", "Bearer "+testSecret)
	if code := status(legacy); code != http.StatusUnauthorized {
		t.Fatalf("legacy bearer: status %d, want 401", code)
	}
}

// A node started without its key refuses every authenticated request rather
// than falling back to the shared secret, which it may still hold.
func TestNodeWithoutAKeyRefusesSignedRequests(t *testing.T) {
	server := newTestServer(t)
	server.SetNodeKey("")
	signed := httptest.NewRequest(http.MethodGet, "/status", nil)
	nodeauthtest.Sign(t, signed, testSecret)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, signed)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("request signed with the shared secret: status %d, want 401", recorder.Code)
	}
}
