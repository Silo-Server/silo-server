package chapterthumbs

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

func testFrameJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 255 / width), G: uint8(y * 255 / height), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func storedKeys(t *testing.T, store blobstore.Store) []string {
	t.Helper()
	infos, _, err := store.List(t.Context(), chapterImagesPrefix, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(infos))
	for _, info := range infos {
		keys = append(keys, info.Key)
	}
	return keys
}

func TestOriginalsCleanerDeletesOnlyOldUnreferencedOriginals(t *testing.T) {
	root := t.TempDir()
	store, err := blobstore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	objects := map[string]time.Time{
		"chapter-images/1/0/original.webp":   old,                 // deleted
		"chapter-images/1/0/w300.webp":       old,                 // served thumbnail
		"chapter-images/1/1/original.webp":   old,                 // deleted
		"chapter-images/2/0/original.webp":   old,                 // referenced by a row: kept
		"chapter-images/3/0/original.webp":   now.Add(-time.Hour), // too new
		"chapter-images/4/0/original.jpg":    old,                 // not a shape this code wrote
		"tmdb/movies/1/poster/original.webp": old,                 // outside the namespace
	}
	for key, modified := range objects {
		if err := store.Put(t.Context(), key, []byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(key)), modified, modified); err != nil {
			t.Fatal(err)
		}
	}

	var lookedUpIDs []int
	var lookedUpKeys []string
	cleaner := &OriginalsCleaner{
		store: store,
		now:   func() time.Time { return now },
		referenced: func(_ context.Context, fileIDs []int, keys []string) (map[string]struct{}, error) {
			lookedUpIDs = append(lookedUpIDs, fileIDs...)
			lookedUpKeys = append(lookedUpKeys, keys...)
			return map[string]struct{}{"chapter-images/2/0/original.webp": {}}, nil
		},
	}

	stats, next, err := cleaner.Page(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatalf("next = %q, want the end of the listing", next)
	}
	if stats.Originals != 4 || stats.Deleted != 2 || stats.Referenced != 1 || stats.TooNew != 1 || stats.DeleteFailed != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	wantLookup := []string{"chapter-images/1/0/original.webp", "chapter-images/1/1/original.webp", "chapter-images/2/0/original.webp"}
	if !slices.Equal(lookedUpKeys, wantLookup) || !slices.Equal(lookedUpIDs, []int{1, 1, 2}) {
		t.Fatalf("reference lookup = %v %v, want %v", lookedUpIDs, lookedUpKeys, wantLookup)
	}

	want := []string{
		"chapter-images/1/0/w300.webp",
		"chapter-images/2/0/original.webp",
		"chapter-images/3/0/original.webp",
		"chapter-images/4/0/original.jpg",
	}
	if got := storedKeys(t, store); !slices.Equal(got, want) {
		t.Fatalf("remaining chapter objects = %v, want %v", got, want)
	}
	if _, err := store.Stat(t.Context(), "tmdb/movies/1/poster/original.webp"); err != nil {
		t.Fatalf("object outside chapter-images/ was touched: %v", err)
	}
}

func TestOriginalsCleanerKeepsEverythingWhenTheReferenceCheckFails(t *testing.T) {
	root := t.TempDir()
	store, err := blobstore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	key := "chapter-images/1/0/original.webp"
	if err := store.Put(t.Context(), key, []byte("x")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(key)), old, old); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("database unavailable")
	cleaner := &OriginalsCleaner{
		store: store,
		now:   time.Now,
		referenced: func(context.Context, []int, []string) (map[string]struct{}, error) {
			return nil, boom
		},
	}
	if _, next, err := cleaner.Page(t.Context(), ""); !errors.Is(err, boom) || next != "" {
		t.Fatalf("Page() = %q, %v; want the reference-check error and no progress", next, err)
	}
	if _, err := store.Stat(t.Context(), key); err != nil {
		t.Fatalf("original deleted without a reference check: %v", err)
	}
}
