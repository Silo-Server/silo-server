package transcodenode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type staticMediaFolders struct {
	folders []*models.MediaFolder
	err     error
}

func (s staticMediaFolders) List(context.Context) ([]*models.MediaFolder, error) {
	return s.folders, s.err
}

func TestMediaRootAuthorizerAllowsOnlyExistingFilesWithinLibraryRoots(t *testing.T) {
	root := t.TempDir()
	mediaPath := filepath.Join(root, "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	authorizer := NewMediaRootAuthorizer(staticMediaFolders{
		folders: []*models.MediaFolder{{Paths: []string{root}}},
	})

	allowed, err := authorizer.Allowed(context.Background(), mediaPath)
	if err != nil || !allowed {
		t.Fatalf("approved media path: allowed = %v, err = %v", allowed, err)
	}

	for _, candidate := range []string{
		"relative/movie.mkv",
		"http://example.test/movie.mkv",
		"file:" + mediaPath,
		"concat:" + mediaPath + "|" + mediaPath,
		filepath.Join(t.TempDir(), "outside.mkv"),
		filepath.Join(root, "missing.mkv"),
	} {
		allowed, err := authorizer.Allowed(context.Background(), candidate)
		if err != nil {
			t.Fatalf("candidate %q: %v", candidate, err)
		}
		if allowed {
			t.Errorf("candidate %q was allowed", candidate)
		}
	}
}

func TestMediaRootAuthorizerRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.mkv")
	if err := os.WriteFile(outside, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.mkv")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	authorizer := NewMediaRootAuthorizer(staticMediaFolders{
		folders: []*models.MediaFolder{{Paths: []string{root}}},
	})

	allowed, err := authorizer.Allowed(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("symlink escape was allowed")
	}
}

func TestMediaRootAuthorizerPropagatesRepositoryErrors(t *testing.T) {
	wantErr := errors.New("database unavailable")
	authorizer := NewMediaRootAuthorizer(staticMediaFolders{err: wantErr})
	allowed, err := authorizer.Allowed(context.Background(), "/media/movie.mkv")
	if allowed || !errors.Is(err, wantErr) {
		t.Fatalf("allowed = %v, err = %v", allowed, err)
	}
}

func TestPathWithinRootRejectsSymlinkedOutputParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	linkedParent := filepath.Join(root, "linked")
	if err := os.Symlink(outside, linkedParent); err != nil {
		t.Fatal(err)
	}
	if pathWithinRoot(root, filepath.Join(linkedParent, "artifact.mp4")) {
		t.Fatal("output under symlinked escape was accepted")
	}
	if !pathWithinRoot(root, filepath.Join(root, "artifact.mp4")) {
		t.Fatal("direct child output was rejected")
	}
}
