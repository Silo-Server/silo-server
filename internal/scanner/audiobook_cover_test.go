package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

const audiobookCoverTestThumbhash = "thumb"

type fakeAudiobookCoverCacher struct {
	calls     int
	data      []byte
	contentID string
	err       error
}

func (f *fakeAudiobookCoverCacher) CacheAudiobookCover(_ context.Context, data []byte, contentID string) (string, string, error) {
	f.calls++
	f.data = append([]byte(nil), data...)
	f.contentID = contentID
	if f.err != nil {
		return "", "", f.err
	}
	return "local/audiobooks/" + contentID + "/poster/original.test-revision.webp", audiobookCoverTestThumbhash, nil
}

type fakeAudiobookCoverStore struct {
	posterPath string
	getErr     error
	contentID  string
	update     *catalog.MetadataUpdate
	err        error
}

func (f *fakeAudiobookCoverStore) GetPosterPath(_ context.Context, _ string) (string, error) {
	return f.posterPath, f.getErr
}

func (f *fakeAudiobookCoverStore) UpdateMetadata(_ context.Context, contentID string, update *catalog.MetadataUpdate) error {
	f.contentID = contentID
	f.update = update
	return f.err
}

func TestApplyAudiobookSidecarCoverCachesFolderCover(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "folder.png"), []byte("audiobook-sidecar-cover"), 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}
	cacher := &fakeAudiobookCoverCacher{}
	store := &fakeAudiobookCoverStore{}

	err := applyAudiobookSidecarCover(context.Background(), store, cacher, "content-1", dir)
	if err != nil {
		t.Fatalf("applyAudiobookSidecarCover: %v", err)
	}

	if cacher.calls != 1 || string(cacher.data) != "audiobook-sidecar-cover" {
		t.Fatalf("cache call = calls %d data %q", cacher.calls, string(cacher.data))
	}
	if store.update == nil || store.update.PosterPath == nil || *store.update.PosterPath != "local/audiobooks/content-1/poster/original.test-revision.webp" {
		t.Fatalf("poster update = %#v", store.update)
	}
	if store.update.PosterThumbhash == nil || *store.update.PosterThumbhash != "thumb" {
		t.Fatalf("poster thumbhash = %#v", store.update.PosterThumbhash)
	}
}

func TestApplyAudiobookSidecarCoverPreservesExistingPoster(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("audiobook-sidecar-cover"), 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}
	cacher := &fakeAudiobookCoverCacher{}
	store := &fakeAudiobookCoverStore{
		posterPath: "provider/poster.webp",
	}

	err := applyAudiobookSidecarCover(context.Background(), store, cacher, "content-1", dir)
	if err != nil {
		t.Fatalf("applyAudiobookSidecarCover: %v", err)
	}

	if cacher.calls != 0 {
		t.Fatalf("cache calls = %d, want 0", cacher.calls)
	}
	if store.update != nil {
		t.Fatalf("unexpected update = %#v", store.update)
	}
}

func TestFindSidecarAudiobookCoverSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.jpg")
	if err := os.WriteFile(target, []byte("not-a-cover"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "cover.jpg")); err != nil {
		t.Fatalf("symlink cover: %v", err)
	}

	data, path, err := findSidecarAudiobookCover(dir)
	if err != nil {
		t.Fatalf("findSidecarAudiobookCover: %v", err)
	}
	if data != nil || path != "" {
		t.Fatalf("findSidecarAudiobookCover returned data %q path %q, want no cover", string(data), path)
	}
}
