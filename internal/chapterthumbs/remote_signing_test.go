package chapterthumbs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

func TestRemoteFrameExtractorSignsTheRequest(t *testing.T) {
	const secret = "chapter-secret"
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chapter-thumbnails/extract" || !nodeauthtest.Signed(r, secret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("frame"))
	}))
	t.Cleanup(node.Close)

	data, reason, err := (&httpRemoteFrameExtractor{}).ExtractFrame(t.Context(), &nodepool.Node{URL: node.URL}, nodeauthtest.SharedSecret(secret),
		RemoteExtractRequest{InputPath: "/media/movie.mkv", SeekSeconds: 12})
	if err != nil || reason != "" || string(data) != "frame" {
		t.Fatalf("ExtractFrame = %q, %q, %v; want the frame from a signed request", data, reason, err)
	}
}

// A node whose own mount does not show the approved source answers 409 in
// plain text. That is the node's problem, not the file's, so a preferring
// caller falls back to this server.
func TestRemoteFrameExtractorTreatsMountMismatchAsNodeFailure(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "local source access: this node's mount does not show the approved source", http.StatusConflict)
	}))
	t.Cleanup(node.Close)

	_, reason, err := (&httpRemoteFrameExtractor{}).ExtractFrame(t.Context(), &nodepool.Node{URL: node.URL}, nodeauthtest.SharedSecret("secret"),
		RemoteExtractRequest{SourceGrant: "grant", SeekSeconds: 12})
	if err == nil || reason != chapterThumbnailNodeUnavailableReason || !isInfrastructureRemoteFailure(reason) {
		t.Fatalf("ExtractFrame on a 409 = %q, %v; want a node failure that allows local fallback", reason, err)
	}
}
