// Package nodeauthtest checks worker request signatures in tests.
package nodeauthtest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
)

// Signed reports whether r carries a valid core-to-worker signature for key
// (a proxy's shared secret or a transcode node's own key). Unlike
// nodeauth.Verify it refuses the legacy bearer, so a sender that goes back to
// sending the key fails the test. It reads and restores r.Body.
func Signed(r *http.Request, key string) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), nodeauth.Scheme+" ") &&
		nodeauth.VerifySigned(r, key) == nil
}

// SharedSecret is a Keyring that signs for every proxy with one key and
// refuses a transcode target. Only tests use it: production signs through the
// node credential registry (nodepool.KeyRegistry).
type SharedSecret string

// KeyFor returns the shared secret for a proxy target.
func (s SharedSecret) KeyFor(target nodeauth.Target, _ string) (string, error) {
	if target != nodeauth.TargetProxy {
		return "", nodeauth.ErrUnknownNode
	}
	return string(s), nil
}

// NodeKey is a Keyring that signs for every transcode node with one key and
// refuses a proxy target, so a sender that names the wrong kind of worker
// fails the test.
type NodeKey string

// KeyFor returns the key for a transcode target.
func (k NodeKey) KeyFor(target nodeauth.Target, _ string) (string, error) {
	if target != nodeauth.TargetTranscode {
		return "", nodeauth.ErrUnknownNode
	}
	return string(k), nil
}

// Keys is a Keyring with one key per target kind, for code that calls both
// proxies and transcode nodes.
type Keys struct {
	Proxy, Transcode string
}

// SameKey is a Keyring that signs for proxies and transcode nodes alike with
// key, for tests of code that calls both and is not about which key it uses.
func SameKey(key string) Keys {
	return Keys{Proxy: key, Transcode: key}
}

// KeyFor returns the key for target's kind.
func (k Keys) KeyFor(target nodeauth.Target, _ string) (string, error) {
	switch target {
	case nodeauth.TargetProxy:
		return k.Proxy, nil
	case nodeauth.TargetTranscode:
		return k.Transcode, nil
	}
	return "", nodeauth.ErrUnknownNode
}

// Sign signs a request for a worker handler under test with key (a proxy's
// shared secret or a transcode node's own key; both directions to a worker
// use one construction). Set the URL and body first: the signature covers
// them.
func Sign(t testing.TB, r *http.Request, key string) {
	t.Helper()
	if err := nodeauth.SignWith(r, nodeauth.TargetProxy, "", SharedSecret(key)); err != nil {
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
