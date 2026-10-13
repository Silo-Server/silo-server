package playback

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
)

func TestDeleteRemoteTranscodeSignsTheRequest(t *testing.T) {
	const secret = "delete-secret"
	var signed atomic.Bool
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/transcode/session-1" {
			signed.Store(nodeauthtest.Signed(r, secret))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(node.Close)

	manager := NewTranscodeManager()
	manager.JWTSecretFn = func() string { return secret }
	if err := manager.deleteRemoteTranscode(t.Context(), "session-1", node.URL); err != nil {
		t.Fatalf("deleteRemoteTranscode: %v", err)
	}
	if !signed.Load() {
		t.Fatal("remote DELETE was not signed with the shared secret")
	}
}
