package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestHandleBrowseFollowsDirectorySymlinks verifies that the folder browser
// lists symlinks that resolve to directories (issue #208). os.ReadDir returns
// lstat-based entries, so a directory symlink reports IsDir() == false and was
// silently dropped; the handler must os.Stat each entry to follow the link.
func TestHandleBrowseFollowsDirectorySymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is unreliable on Windows CI")
	}
	t.Parallel()

	root := t.TempDir()

	// A real directory that should always be listed.
	realDir := filepath.Join(root, "real-dir")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real-dir: %v", err)
	}

	// The symlink-to-directory target lives outside root so the browser cannot
	// reach it except by following the link — mirrors the reported setup where
	// entries under a bind mount point into a separate FUSE mount.
	linkTarget := filepath.Join(t.TempDir(), "link-target")
	if err := os.Mkdir(linkTarget, 0o755); err != nil {
		t.Fatalf("mkdir link-target: %v", err)
	}
	dirSymlink := filepath.Join(root, "symlinked-dir")
	if err := os.Symlink(linkTarget, dirSymlink); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}

	// A regular file: must never be listed by the folder browser.
	regularFile := filepath.Join(root, "note.txt")
	if err := os.WriteFile(regularFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// A symlink to a file: resolves to a non-directory, must not be listed.
	fileSymlink := filepath.Join(root, "note-link")
	if err := os.Symlink(regularFile, fileSymlink); err != nil {
		t.Fatalf("symlink file: %v", err)
	}

	// A broken symlink: os.Stat fails; it must be skipped, not error the request.
	brokenSymlink := filepath.Join(root, "broken-link")
	if err := os.Symlink(filepath.Join(root, "does-not-exist"), brokenSymlink); err != nil {
		t.Fatalf("symlink broken: %v", err)
	}

	handler := NewFilesystemHandler()
	req := httptest.NewRequest(http.MethodGet, "/filesystem/browse?path="+root, nil)
	rec := httptest.NewRecorder()
	handler.HandleBrowse(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp filesystemBrowseResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	got := make(map[string]string, len(resp.Entries))
	for _, e := range resp.Entries {
		got[e.Name] = e.Path
	}

	if _, ok := got["real-dir"]; !ok {
		t.Errorf("real-dir missing from entries: %+v", resp.Entries)
	}
	if path, ok := got["symlinked-dir"]; !ok {
		t.Errorf("symlinked-dir (directory symlink) missing from entries: %+v", resp.Entries)
	} else if want := filepath.Join(root, "symlinked-dir"); path != want {
		t.Errorf("symlinked-dir path = %q, want %q", path, want)
	}
	if _, ok := got["note.txt"]; ok {
		t.Errorf("regular file note.txt should not be listed: %+v", resp.Entries)
	}
	if _, ok := got["note-link"]; ok {
		t.Errorf("file symlink note-link should not be listed: %+v", resp.Entries)
	}
	if _, ok := got["broken-link"]; ok {
		t.Errorf("broken symlink broken-link should not be listed: %+v", resp.Entries)
	}
}
