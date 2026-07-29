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
	folders   []*models.MediaFolder
	listCalls int
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
	r.listCalls++
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
	if target.Folder == nil || target.Folder.ID != 7 || target.Mode != ModeLibrary || target.Path != "" {
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
	if target.Folder == nil || target.Folder.ID != 8 || target.Mode != ModeSubtree || target.Path != filepath.Clean(subtree) {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolverResolvesMissingSubtreeWithoutStat(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "Missing Movie")
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      18,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	target, err := NewResolver(repo).ResolveMissingSubtree(context.Background(), missing, "autoscan")
	if err != nil {
		t.Fatalf("ResolveMissingSubtree returned error: %v", err)
	}
	if target.Folder == nil || target.Folder.ID != 18 || target.Mode != ModeSubtree || target.Path != filepath.Clean(missing) {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolverRejectsMissingSubtreeAtLibraryRoot(t *testing.T) {
	root := t.TempDir()
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      19,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	_, err := NewResolver(repo).ResolveMissingSubtree(context.Background(), root, "autoscan")
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Status != http.StatusBadRequest || reqErr.Code != "bad_request" {
		t.Fatalf("unexpected error: %#v", reqErr)
	}
}

func TestResolverResolvesVanishedMediaFileAsExactFile(t *testing.T) {
	for _, tc := range []struct {
		name        string
		libraryType string
	}{
		{name: "Movie.mkv", libraryType: "movies"},
		{name: "Book.epub", libraryType: "ebooks"},
		{name: "Audiobook.m4b", libraryType: "audiobooks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mediaDir := filepath.Join(root, "Title")
			if err := os.Mkdir(mediaDir, 0o755); err != nil {
				t.Fatal(err)
			}
			vanished := filepath.Join(mediaDir, tc.name)
			repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
				ID:      20,
				Name:    "Media",
				Type:    tc.libraryType,
				Enabled: true,
				Paths:   []string{root},
			}}}

			target, err := NewResolver(repo).ResolveVanishedPath(context.Background(), vanished, "autoscan")
			if err != nil {
				t.Fatalf("ResolveVanishedPath returned error: %v", err)
			}
			if target.Folder == nil || target.Folder.ID != 20 || target.Mode != ModeFile || target.Path != vanished {
				t.Fatalf("unexpected target: %#v", target)
			}
		})
	}
}

func TestResolverResolvesVanishedDirToItself(t *testing.T) {
	root := t.TempDir()
	vanishedDir := filepath.Join(root, "Removed Movie (2026)")
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      21,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	target, err := NewResolver(repo).ResolveVanishedPath(context.Background(), vanishedDir, "autoscan")
	if err != nil {
		t.Fatalf("ResolveVanishedPath returned error: %v", err)
	}
	if target.Folder == nil || target.Folder.ID != 21 || target.Mode != ModeSubtree || target.Path != vanishedDir {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolverResolvesVanishedFileDirectlyUnderRootAsExactFile(t *testing.T) {
	root := t.TempDir()
	vanished := filepath.Join(root, "Movie (2026).mkv")
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      22,
		Name:    "Movies",
		Type:    "movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	target, err := NewResolver(repo).ResolveVanishedPath(context.Background(), vanished, "autoscan")
	if err != nil {
		t.Fatalf("ResolveVanishedPath returned error: %v", err)
	}
	if target.Folder == nil || target.Folder.ID != 22 || target.Mode != ModeFile || target.Path != vanished {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolverRejectsVanishedPathWhenRootIsGone(t *testing.T) {
	root := t.TempDir()
	// Simulate an unmounted share: the configured root itself is gone.
	goneRoot := filepath.Join(root, "mount")
	vanished := filepath.Join(goneRoot, "Movie (2026)", "Movie (2026).mkv")
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      23,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{goneRoot},
	}}}

	_, err := NewResolver(repo).ResolveVanishedPath(context.Background(), vanished, "autoscan")
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Status != http.StatusConflict || reqErr.Code != "conflict" {
		t.Fatalf("unexpected error: %#v", reqErr)
	}
}

func TestResolverRejectsVanishedPathInDisabledLibrary(t *testing.T) {
	root := t.TempDir()
	vanished := filepath.Join(root, "Movie (2026)", "Movie (2026).mkv")
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      26,
		Name:    "Movies",
		Enabled: false,
		Paths:   []string{root},
	}}}

	_, err := NewResolver(repo).ResolveVanishedPath(context.Background(), vanished, "autoscan")
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Status != http.StatusConflict || reqErr.Code != "conflict" {
		t.Fatalf("unexpected error: %#v", reqErr)
	}
}

func TestResolverRejectsVanishedPathOnNonNotExistStatError(t *testing.T) {
	root := t.TempDir()
	// A regular file where a directory is expected makes Lstat on a child
	// path fail with ENOTDIR — a stat failure that is not ENOENT.
	notADir := filepath.Join(root, "Movie (2026)")
	if err := os.WriteFile(notADir, []byte("file, not dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(notADir, "Movie (2026).mkv")
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      25,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	_, err := NewResolver(repo).ResolveVanishedPath(context.Background(), child, "autoscan")
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Status != http.StatusBadRequest {
		t.Fatalf("unexpected error: %#v", reqErr)
	}
}

func TestResolverRejectsVanishedPathThatStillExists(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "Movie (2026).mkv")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      24,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	_, err := NewResolver(repo).ResolveVanishedPath(context.Background(), filePath, "autoscan")
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Status != http.StatusBadRequest {
		t.Fatalf("unexpected error: %#v", reqErr)
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
	if target.Folder == nil || target.Folder.ID != 9 || target.Mode != ModeFile || target.Path != filepath.Clean(filePath) {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolverClassifiesAudioAndEbookFiles(t *testing.T) {
	for _, tc := range []struct {
		name        string
		libraryType string
	}{
		{name: "Book.epub", libraryType: "ebooks"},
		{name: "Book.fb2.zip", libraryType: "ebooks"},
		{name: "Audiobook.m4b", libraryType: "audiobooks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			filePath := filepath.Join(root, tc.name)
			if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
				t.Fatal(err)
			}
			repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
				ID:      25,
				Name:    "Media",
				Type:    tc.libraryType,
				Enabled: true,
				Paths:   []string{root},
			}}}

			target, err := NewResolver(repo).Resolve(context.Background(), Request{Path: filePath})
			if err != nil {
				t.Fatalf("Resolve returned error: %v", err)
			}
			if target.Mode != ModeFile || target.Path != filepath.Clean(filePath) {
				t.Fatalf("unexpected target: %#v", target)
			}
		})
	}
}

func TestResolverRejectsPodcastFileTargetUntilPodcastPipelineSupportsIt(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "Episode.mp3")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      26,
		Name:    "Podcasts",
		Type:    "podcasts",
		Enabled: true,
		Paths:   []string{root},
	}}}

	_, err := NewResolver(repo).Resolve(context.Background(), Request{Path: filePath})
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.Status != http.StatusBadRequest || reqErr.Message != "Unsupported media file extension for library type" {
		t.Fatalf("unexpected error: %#v", reqErr)
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

func TestResolveAllReusesPathOnlyLibraryList(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "First.mkv")
	second := filepath.Join(root, "Second.mkv")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := &fakeFolderRepo{folders: []*models.MediaFolder{{
		ID:      12,
		Name:    "Movies",
		Enabled: true,
		Paths:   []string{root},
	}}}

	targets, err := NewResolver(repo).ResolveAll(context.Background(), []Request{
		{Path: first},
		{Path: second},
	})
	if err != nil {
		t.Fatalf("ResolveAll returned error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected two targets, got %d", len(targets))
	}
	if repo.listCalls != 1 {
		t.Fatalf("expected one folder list lookup, got %d", repo.listCalls)
	}
}

type fakeQueue struct {
	calls    []Target
	batches  [][]Target
	batchErr error
}

func (q *fakeQueue) EnqueueScan(_ context.Context, folderID int, mode, path, trigger string) (bool, error) {
	q.calls = append(q.calls, Target{Folder: &models.MediaFolder{ID: folderID}, Mode: mode, Path: path, Trigger: trigger})
	return true, nil
}

func (q *fakeQueue) EnqueueScans(_ context.Context, targets []Target) error {
	copied := append([]Target(nil), targets...)
	q.batches = append(q.batches, copied)
	if q.batchErr != nil {
		return q.batchErr
	}
	q.calls = append(q.calls, targets...)
	return nil
}

func TestEnqueueAllUsesBatchQueue(t *testing.T) {
	queue := &fakeQueue{}
	folder := &models.MediaFolder{ID: 1}
	targets := []Target{
		{Folder: folder, Mode: ModeFile, Path: "/media/one.mkv", Trigger: "autoscan"},
		{Folder: folder, Mode: ModeFile, Path: "/media/two.mkv", Trigger: "autoscan"},
	}

	if err := EnqueueAll(context.Background(), queue, targets); err != nil {
		t.Fatalf("EnqueueAll returned error: %v", err)
	}
	if len(queue.batches) != 1 {
		t.Fatalf("expected one batch enqueue, got %d", len(queue.batches))
	}
	if len(queue.calls) != 2 {
		t.Fatalf("expected two queued calls from batch, got %d", len(queue.calls))
	}
}
