// Package nodeauthtest checks worker request signatures in tests.
package nodeauthtest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
)

// Signed reports whether r carries a valid node signature for secret. Unlike
// nodeauth.Verify it refuses the legacy bearer, so a sender that goes back to
// sending the shared secret fails the test. It reads and restores r.Body.
func Signed(r *http.Request, secret string) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), nodeauth.Scheme+" ") &&
		nodeauth.Verify(r, secret) == nil
}

// Sign signs a request for a worker handler under test. Set the URL and body
// first: the signature covers them.
func Sign(t testing.TB, r *http.Request, secret string) {
	t.Helper()
	if err := nodeauth.Sign(r, "", secret); err != nil {
		t.Fatalf("sign worker request: %v", err)
	}
}
