package transcodenode

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
)

// The node admits only requests signed with its own key for exactly the route
// they reach. The shared secret, signed or sent as a bearer, is refused: a
// transcode node never accepts a credential another worker also holds.
func TestNodeRoutesRequireASignatureWithTheNodeKey(t *testing.T) {
	server := newTestServer(t)
	status := func(r *http.Request) int {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, r)
		return recorder.Code
	}

	signed := httptest.NewRequest(http.MethodGet, "/status", nil)
	nodeauthtest.Sign(t, signed, testNodeKey)
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

	sharedSecret := httptest.NewRequest(http.MethodGet, "/status", nil)
	nodeauthtest.Sign(t, sharedSecret, testSecret)
	if code := status(sharedSecret); code != http.StatusUnauthorized {
		t.Fatalf("request signed with the shared secret: status %d, want 401", code)
	}

	for _, credential := range []string{testSecret, testNodeKey} {
		bearer := httptest.NewRequest(http.MethodGet, "/status", nil)
		bearer.Header.Set("Authorization", "Bearer "+credential)
		if code := status(bearer); code != http.StatusUnauthorized {
			t.Fatalf("bearer credential: status %d, want 401", code)
		}
	}

	server.nodeKey = ""
	keyless := httptest.NewRequest(http.MethodGet, "/status", nil)
	nodeauthtest.Sign(t, keyless, testNodeKey)
	if code := status(keyless); code != http.StatusUnauthorized {
		t.Fatalf("node without a key: status %d, want 401", code)
	}
}
