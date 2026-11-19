package jellycompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
)

func TestRemoteToneMapCapabilityInfoDoesNotForwardAuthorizationAcrossRedirect(t *testing.T) {
	var redirectedAuthorization string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(target.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/hw-capabilities", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	handler := &PlaybackHandler{NodeKeys: nodeauthtest.SameKey("node-secret")}
	if _, err := handler.remoteToneMapCapabilityInfo(context.Background(), nodeauth.TargetTranscode, redirector.URL); err == nil {
		t.Fatal("remoteToneMapCapabilityInfo followed a redirect, want redirect rejection")
	}
	if redirectedAuthorization != "" {
		t.Fatalf("redirected Authorization = %q, want empty", redirectedAuthorization)
	}
}

func TestRemoteToneMapCapabilityInfoRejectsResponseOverOneMiB(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"padding":%q}`, strings.Repeat("x", (1<<20)+1))
	}))
	t.Cleanup(node.Close)

	handler := &PlaybackHandler{NodeKeys: nodeauthtest.SameKey("node-secret")}
	if _, err := handler.remoteToneMapCapabilityInfo(context.Background(), nodeauth.TargetTranscode, node.URL); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("remoteToneMapCapabilityInfo error = %v, want oversized-response rejection", err)
	}
}
