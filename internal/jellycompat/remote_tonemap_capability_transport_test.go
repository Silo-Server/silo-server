package jellycompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/playback"
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

	handler := &PlaybackHandler{JWTSecret: "node-secret"}
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

	handler := &PlaybackHandler{JWTSecret: "node-secret"}
	withTestNodeKeys(handler)
	if _, err := handler.remoteToneMapCapabilityInfo(context.Background(), nodeauth.TargetTranscode, node.URL); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("remoteToneMapCapabilityInfo error = %v, want oversized-response rejection", err)
	}
}

// roleKeyring signs a URL only for the kind of worker registered there, as
// the credential registry does: a proxy with the shared secret, a transcode
// node with its own key.
type roleKeyring struct {
	proxyURL, transcodeURL string
}

func (k roleKeyring) KeyFor(target nodeauth.Target, nodeURL string) (string, error) {
	switch {
	case target == nodeauth.TargetProxy && nodeURL == k.proxyURL:
		return "shared-secret", nil
	case target == nodeauth.TargetTranscode && nodeURL == k.transcodeURL:
		return "node-key", nil
	}
	return "", nodeauth.ErrUnknownNode
}

// The audio-boost sweep reads proxies as well as transcode nodes, so each
// read is signed for the pool it came from. Signed as a transcode node, the
// proxy's report is refused and surround remux loses every proxy executor.
func TestCompatAudioBoostSweepSignsEachPoolForItsOwnRole(t *testing.T) {
	worker := func(key string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if nodeauth.VerifySigned(r, key) != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"transformations":[{"name":%q,"executor":%q,"recipe_version":%q}]}`,
				playback.TransformationAudioToAACV3, playback.ExecutorServerV3, playback.TransformationAudioToAACRecipeVersionV3)
		}))
		t.Cleanup(server.Close)
		return server
	}
	proxy := worker("shared-secret")
	transcode := worker("node-key")
	handler := &PlaybackHandler{
		NodeKeys:    roleKeyring{proxyURL: proxy.URL, transcodeURL: transcode.URL},
		NodePlanner: compatTimeoutPlanner{transcodes: []string{transcode.URL}, proxies: []string{proxy.URL}},
	}

	proxies, err := handler.compatAudioBoostProxyNodeURLs(context.Background(), 5*time.Second)
	if _, ok := proxies[proxy.URL]; err != nil || !ok {
		t.Fatalf("proxy sweep = (%v, %v), want the proxy read with the shared secret", proxies, err)
	}
	transcodes, err := handler.compatAudioBoostNodeURLs(context.Background(), 5*time.Second)
	if _, ok := transcodes[transcode.URL]; err != nil || !ok {
		t.Fatalf("transcode sweep = (%v, %v), want the node read with its own key", transcodes, err)
	}
}
