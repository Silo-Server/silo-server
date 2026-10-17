package playback

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// The catalog hands playback the fspath-encoded form of a non-UTF-8 file
// name; direct play must open the real file, ranges included.
func TestServeDirectPlayOpensEncodedNonUTF8Path(t *testing.T) {
	const content = "0123456789"
	rawPath := filepath.Join(t.TempDir(), "Takk for alt B\xe5rd Ylvis\xe5ker.mkv")
	if err := os.WriteFile(rawPath, []byte(content), 0o600); err != nil {
		t.Skipf("file system rejects non-UTF-8 names: %v", err)
	}
	encoded := fspath.Encode(rawPath)

	full := httptest.NewRecorder()
	if err := ServeDirectPlay(full, httptest.NewRequest(http.MethodGet, "/stream", nil), encoded); err != nil {
		t.Fatalf("ServeDirectPlay: %v", err)
	}
	if full.Code != http.StatusOK || full.Body.String() != content {
		t.Fatalf("full response = %d %q, want 200 %q", full.Code, full.Body.String(), content)
	}

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	req.Header.Set("Range", "bytes=2-4")
	partial := httptest.NewRecorder()
	if err := ServeDirectPlay(partial, req, encoded); err != nil {
		t.Fatalf("ServeDirectPlay range: %v", err)
	}
	if partial.Code != http.StatusPartialContent || partial.Body.String() != "234" {
		t.Fatalf("range response = %d %q, want 206 %q", partial.Code, partial.Body.String(), "234")
	}
}
