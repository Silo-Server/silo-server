// Package nodesourcetest gives tests a node source relay without a core.
package nodesourcetest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// Grant is the grant a LocalRelay approves for path: the path itself.
func Grant(path string) string { return path }

// LocalRelay returns a relay with local source access whose core approves
// any grant that names an existing regular file by its absolute path (see
// Grant), and refuses anything else with 403. It needs no network.
func LocalRelay(t testing.TB) *nodesource.Relay {
	t.Helper()
	relay, err := nodesource.NewRelay(nodesource.RelayOptions{
		Open:    approveLocalPath,
		CoreURL: "http://core.test",
		NodeID:  1,
		NodeKey: "snk1_1_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	return relay
}

// CoreRelay returns a relay with core source access whose core approves any
// grant that names an existing regular file by its absolute path (see Grant)
// and serves that file's bytes, so the handles it opens are loopback URLs
// that nodesource's registry knows. The core runs on a local test server.
func CoreRelay(t testing.TB) *nodesource.Relay {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(serveLocalPaths))
	t.Cleanup(core.Close)
	relay, err := nodesource.NewRelay(nodesource.RelayOptions{
		Open:    approveLocalPath,
		CoreURL: core.URL,
		NodeID:  1,
		NodeKey: "snk1_1_test",
		Mode:    func() string { return nodesource.ModeCore },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	return relay
}

// Relayed opens path through a CoreRelay and returns its handle, closed when
// the test ends.
func Relayed(t testing.TB, path string) *nodesource.Handle {
	t.Helper()
	h, err := CoreRelay(t).Open(context.Background(), Grant(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// serveLocalPaths is CoreRelay's core: the source endpoint serving the
// granted path's bytes with its entity tag.
func serveLocalPaths(w http.ResponseWriter, r *http.Request) {
	path := r.Header.Get(nodesource.HeaderSourceGrant)
	info := regularFile(path)
	if r.URL.Path != nodesource.PathSources || !filepath.IsAbs(path) || info == nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("ETag", etag(info))
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func etag(info os.FileInfo) string {
	return fmt.Sprintf(`"file-1-%d-%d"`, info.Size(), nodesource.RevisionMtime(info.ModTime()))
}

// approveLocalPath is the test core's open endpoint: it approves a grant that
// names an existing regular file by its absolute path, and refuses anything
// else as the core would.
func approveLocalPath(_ context.Context, grant string) (*nodesource.OpenResponse, error) {
	info := regularFile(grant)
	switch {
	case !filepath.IsAbs(grant):
		return nil, &nodesource.StatusError{Op: "open", Status: http.StatusForbidden}
	case info == nil:
		return nil, &nodesource.StatusError{Op: "open", Status: http.StatusGone}
	}
	return &nodesource.OpenResponse{
		Grant: grant,
		Path:  grant,
		Size:  info.Size(),
		Mtime: nodesource.RevisionMtime(info.ModTime()),
		ETag:  etag(info),
	}, nil
}

// regularFile describes path when it is a regular file, else nil.
func regularFile(path string) os.FileInfo {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	return info
}

// Granter is a grant minter for dispatch tests: it mints GrantFor's grant,
// or fails with Err.
type Granter struct{ Err error }

// ForFile mints GrantFor(nodeID, file.ID, file.FilePath).
func (g Granter) ForFile(_ context.Context, nodeID int, file *models.MediaFile) (string, error) {
	if g.Err != nil {
		return "", g.Err
	}
	if file == nil {
		return "", nodesource.ErrInvalidGrant
	}
	return GrantFor(nodeID, file.ID, file.FilePath), nil
}

// GrantFor is the grant Granter mints for node nodeID to use media file
// fileID at path.
func GrantFor(nodeID, fileID int, path string) string {
	return fmt.Sprintf("grant:%d:%d:%s", nodeID, fileID, path)
}
