package nodecontrol

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// The bytes come from the descriptor that passed the check: a file renamed
// over the path between the check and the serving is never served.
func TestSourceServesTheCheckedDescriptor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	original := bytes.Repeat([]byte("original "), 1000)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	grants := nodesource.NewGrants(func() string { return "descriptor-secret" })
	grant, err := grants.ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Options{Grants: grants})
	server.sourceOpened = func() {
		replacement := filepath.Join(dir, "replacement.mkv")
		if err := os.WriteFile(replacement, bytes.Repeat([]byte("swapped! "), 1000), 0o600); err != nil {
			t.Error(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Error(err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, nodesource.PathSources, nil)
	req.Header.Set(nodesource.HeaderSourceGrant, grant)
	req = req.WithContext(context.WithValue(req.Context(), nodeIDContextKey{}, 7))
	rec := httptest.NewRecorder()
	server.HandleSource(rec, req)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), original) {
		t.Fatalf("status %d, served the swapped file: %v", rec.Code, !bytes.Equal(rec.Body.Bytes(), original))
	}
}
