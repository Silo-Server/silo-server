package playback

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
)

func TestDeleteRemoteTranscodeSignsTheRequest(t *testing.T) {
	const key = "snk1_1_delete-key"
	var signed atomic.Bool
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/transcode/session-1" {
			signed.Store(nodeauthtest.Signed(r, key))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(node.Close)

	manager := NewTranscodeManager()
	manager.NodeKeysFn = func() nodeauth.Keyring { return nodeauthtest.NodeKey(key) }
	if err := manager.deleteRemoteTranscode(t.Context(), "session-1", node.URL, true, ""); err != nil {
		t.Fatalf("deleteRemoteTranscode: %v", err)
	}
	if !signed.Load() {
		t.Fatal("remote DELETE was not signed with the node's key")
	}
}

func TestDeleteRemoteTranscodeDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(elsewhere.Close)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(node.Close)

	manager := NewTranscodeManager()
	manager.NodeKeysFn = func() nodeauth.Keyring { return nodeauthtest.NodeKey("snk1_1_delete-key") }
	if err := manager.deleteRemoteTranscode(t.Context(), "session-1", node.URL, true, ""); err == nil {
		t.Fatal("deleteRemoteTranscode succeeded on a redirect, want an error")
	}
	if followed.Load() {
		t.Fatal("remote DELETE followed the node's redirect")
	}
}
