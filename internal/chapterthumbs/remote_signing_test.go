package chapterthumbs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

func TestRemoteFrameExtractorSignsTheRequest(t *testing.T) {
	const key = "snk1_1_chapter-key"
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chapter-thumbnails/extract" || !nodeauthtest.Signed(r, key) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("frame"))
	}))
	t.Cleanup(node.Close)

	data, reason, err := (&httpRemoteFrameExtractor{}).ExtractFrame(t.Context(), &nodepool.Node{URL: node.URL}, nodeauthtest.NodeKey(key),
		RemoteExtractRequest{InputPath: "/media/movie.mkv", SeekSeconds: 12})
	if err != nil || reason != "" || string(data) != "frame" {
		t.Fatalf("ExtractFrame = %q, %q, %v; want the frame from a signed request", data, reason, err)
	}
}

// A node that answers with more than the frame limit is refused rather than
// read into memory.
func TestRemoteFrameExtractorRefusesAnOversizedFrame(t *testing.T) {
	const key = "snk1_1_chapter-key"
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(make([]byte, 64<<20+1))
	}))
	t.Cleanup(node.Close)

	data, reason, err := (&httpRemoteFrameExtractor{}).ExtractFrame(t.Context(), &nodepool.Node{URL: node.URL}, nodeauthtest.NodeKey(key),
		RemoteExtractRequest{InputPath: "/media/movie.mkv", SeekSeconds: 12})
	if err == nil || data != nil || reason != chapterThumbnailNodeUnavailableReason {
		t.Fatalf("ExtractFrame = %d bytes, %q, %v; want %q and an error", len(data), reason, err, chapterThumbnailNodeUnavailableReason)
	}
}
