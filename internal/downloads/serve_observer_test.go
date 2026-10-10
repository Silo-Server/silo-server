package downloads

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestServeLocalFileEntityTag: native downloads send no ETag; a route that
// asks for one gets it from the descriptor being served.
func TestServeLocalFileEntityTag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, []byte("synthetic media"), 0o600); err != nil {
		t.Fatal(err)
	}
	serve := func(ctx context.Context) string {
		t.Helper()
		w := httptest.NewRecorder()
		if err := (&Service{}).serveLocalFile(ctx, w, httptest.NewRequest(http.MethodHead, "/file", nil), path, 1); err != nil {
			t.Fatal(err)
		}
		return w.Header().Get("ETag")
	}

	if got := serve(t.Context()); got != "" {
		t.Fatalf("ETag without a tag function = %q, want none", got)
	}
	var tagged string
	ctx := WithServeEntityTag(t.Context(), func(file *os.File, _ os.FileInfo) string {
		tagged = file.Name()
		return `"synthetic"`
	})
	if got := serve(ctx); got != `"synthetic"` || tagged != path {
		t.Fatalf("ETag = %q from %q, want %q from the served file %q", got, tagged, `"synthetic"`, path)
	}
}
