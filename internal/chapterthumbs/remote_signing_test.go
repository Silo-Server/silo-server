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

	data, reason, err := (&httpRemoteFrameExtractor{}).ExtractFrame(t.Context(), &nodepool.Node{URL: node.URL}, secret,
		RemoteExtractRequest{InputPath: "/media/movie.mkv", SeekSeconds: 12})
	if err != nil || reason != "" || string(data) != "frame" {
		t.Fatalf("ExtractFrame = %q, %q, %v; want the frame from a signed request", data, reason, err)
	}
}
