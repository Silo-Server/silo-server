package scantrigger

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeFolderRepo struct {
	folders []*models.MediaFolder
}

func (r *fakeFolderRepo) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	for _, folder := range r.folders {
		if folder.ID == id {
			return folder, nil
		}
	}
	return nil, catalog.ErrFolderNotFound
}

func (r *fakeFolderRepo) List(context.Context) ([]*models.MediaFolder, error) {
	return r.folders, nil
}

func TestResolverClassifiesLibraryRoot(t *testing.T) {
	root := t.TempDir()
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      7,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	target, err := NewResolver(repo).Resolve(context.Background(), Request{Path: root})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if target.LibraryID != 7 || target.Mode != ModeLibrary || target.Path != "" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolverClassifiesSubtree(t *testing.T) {
	root := t.TempDir()
	subtree := filepath.Join(root, "Show")
	if err := os.Mkdir(subtree, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      8,
		Name:    "TV",
		Enabled: true,
		Paths:   []string{root},
	}}}

	target, err := NewResolver(repo).Resolve(context.Background(), Request{Path: subtree})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if target.LibraryID != 8 || target.Mode != ModeSubtree || target.Path != filepath.Clean(subtree) {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolverClassifiesVideoFile(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "Movie (2024).mkv")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      9,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	target, err := NewResolver(repo).Resolve(context.Background(), Request{Path: filePath})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if target.LibraryID != 9 || target.Mode != ModeFile || target.Path != filepath.Clean(filePath) {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolverRejectsDisabledLibrary(t *testing.T) {
	root := t.TempDir()
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      10,
		Name:    "Disabled",
		Enabled: false,
		Paths:   []string{root},
	}}}

	_, err := NewResolver(repo).Resolve(context.Background(), Request{Path: root})
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Status != http.StatusConflict || reqErr.Code != "conflict" {
		t.Fatalf("unexpected error: %#v", reqErr)
	}
}

func TestResolveAllIsAllOrFail(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(valid, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      11,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	_, err := NewResolver(repo).ResolveAll(context.Background(), []Request{
		{Path: valid},
		{Path: filepath.Join(root, "missing.mkv")},
	})
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Message != "Path does not exist" {
		t.Fatalf("unexpected error message: %q", reqErr.Message)
	}
}
