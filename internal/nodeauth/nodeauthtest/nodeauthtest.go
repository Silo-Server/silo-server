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

// SharedSecret is a Keyring that signs for every worker with one key. Only
// tests use it: production signs through the node credential registry
// (nodepool.KeyRegistry), which keeps transcode nodes off the shared secret.
type SharedSecret string

// KeyFor returns the shared secret for every worker.
func (s SharedSecret) KeyFor(nodeauth.Target, string) (string, error) { return string(s), nil }

// Sign signs a request for a worker handler under test with secret. Set the
// URL and body first: the signature covers them.
func Sign(t testing.TB, r *http.Request, secret string) {
	t.Helper()
	if err := nodeauth.SignWith(r, nodeauth.TargetProxy, "", SharedSecret(secret)); err != nil {
		t.Fatalf("sign worker request: %v", err)
	}
}

// SignFromNode signs a request to a core handler under test as transcode node
// nodeID with its key. Set the URL and body first: the signature covers them.
func SignFromNode(t testing.TB, r *http.Request, nodeID int, key string) {
	t.Helper()
	if err := nodeauth.SignToCore(r, "", nodeID, key); err != nil {
		t.Fatalf("sign node request: %v", err)
	}
}
