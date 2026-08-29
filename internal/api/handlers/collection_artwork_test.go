package handlers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkupload"
)

// memoryArtworkStore is an in-memory artworkupload.Store recording every write.
type memoryArtworkStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemoryArtworkStore() *memoryArtworkStore {
	return &memoryArtworkStore{objects: make(map[string][]byte)}
}

func (s *memoryArtworkStore) WriteImmutable(_ context.Context, key string, data []byte, _ artworkstore.ObjectMetadata) error {
	if err := artworkstore.ValidateKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), data...)
	return nil
}

func (s *memoryArtworkStore) Matches(_ context.Context, key string, data []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.objects[key]
	return ok && bytes.Equal(stored, data), nil
}

func (s *memoryArtworkStore) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestStoreBundledCollectionPoster_NoStorageKeepsPath(t *testing.T) {
	path := "/images/collection-templates/template.jpg"
	gotPath, gotThumbhash, stored, err := storeBundledCollectionPoster(
		context.Background(),
		nil,
		fstest.MapFS{},
		path,
		true,
	)
	if err != nil {
		t.Fatalf("storeBundledCollectionPoster: %v", err)
	}
	if stored {
		t.Fatal("stored = true, want false")
	}
	if gotPath != path {
		t.Fatalf("path = %q, want %q", gotPath, path)
	}
	if gotThumbhash != "" {
		t.Fatalf("thumbhash = %q, want empty", gotThumbhash)
	}
}

func TestStoreBundledCollectionPoster_IgnoresNonTemplatePath(t *testing.T) {
	store := newMemoryArtworkStore()
	path := "collection-images/existing/poster/original.webp"

	gotPath, gotThumbhash, stored, err := storeBundledCollectionPoster(
		context.Background(),
		artworkupload.NewMaterializer(store),
		fstest.MapFS{},
		path,
		true,
	)
	if err != nil {
		t.Fatalf("storeBundledCollectionPoster: %v", err)
	}
	if stored {
		t.Fatal("stored = true, want false")
	}
	if gotPath != path {
		t.Fatalf("path = %q, want %q", gotPath, path)
	}
	if gotThumbhash != "" {
		t.Fatalf("thumbhash = %q, want empty", gotThumbhash)
	}
	if keys := store.keys(); len(keys) != 0 {
		t.Fatalf("stored keys = %#v, want none", keys)
	}
}

func TestStoreBundledCollectionPoster_MaterializesTemplatePoster(t *testing.T) {
	store := newMemoryArtworkStore()
	frontendFS := fstest.MapFS{
		"images/collection-templates/template.jpg": {
			Data: testCollectionPosterJPEG(t),
		},
	}

	gotPath, gotThumbhash, stored, err := storeBundledCollectionPoster(
		context.Background(),
		artworkupload.NewMaterializer(store),
		frontendFS,
		"/images/collection-templates/template.jpg",
		true,
	)
	if err != nil {
		t.Fatalf("storeBundledCollectionPoster: %v", err)
	}
	if !stored {
		t.Fatal("stored = false, want true")
	}
	if gotThumbhash == "" {
		t.Fatal("thumbhash is empty")
	}

	// The stored path must be the portable original-variant key for the
	// collection-poster upload type, and nothing about the collection's
	// identity may appear in it.
	info, ok := artworkkey.ParsePortableKey(gotPath)
	if !ok {
		t.Fatalf("stored path %q is not a portable artwork key", gotPath)
	}
	if info.ImageType != artworkkey.ImageTypeCollectionPoster {
		t.Fatalf("image type = %q, want %q", info.ImageType, artworkkey.ImageTypeCollectionPoster)
	}
	if info.Variant != artworkkey.OriginalVariant {
		t.Fatalf("variant = %q, want %q", info.Variant, artworkkey.OriginalVariant)
	}

	// The revision must be complete: every ladder entry plus the manifest.
	want := map[string]bool{}
	for _, name := range artworkkey.VariantNames(artworkkey.ImageTypeCollectionPoster) {
		want[info.Directory+"/"+name+info.Ext] = true
	}
	want[info.Directory+"/"+artworkkey.ManifestName] = true
	keys := store.keys()
	if len(keys) != len(want) {
		t.Fatalf("stored keys = %#v, want %d objects", keys, len(want))
	}
	for _, key := range keys {
		if !want[key] {
			t.Fatalf("unexpected stored key %q in %#v", key, keys)
		}
	}

	// Card-sized delivery has to land on a variant that was actually written.
	card := cardThumbnailPath(gotPath)
	if card == gotPath || !strings.HasSuffix(card, "/w300"+info.Ext) {
		t.Fatalf("card variant path = %q", card)
	}
	if !want[card] {
		t.Fatalf("card variant %q was not stored", card)
	}
}

// TestMaterializeCollectionImage_BackdropLadder pins the backdrop ladder: the
// collector expands a trigger-queued revision through it, so a mismatch here
// leaves orphans behind.
func TestMaterializeCollectionImage_BackdropLadder(t *testing.T) {
	store := newMemoryArtworkStore()
	storedPath, _, err := materializeCollectionImage(
		context.Background(),
		artworkupload.NewMaterializer(store),
		artworkkey.ImageTypeBackdrop,
		testCollectionPosterJPEG(t),
		true,
	)
	if err != nil {
		t.Fatalf("materializeCollectionImage: %v", err)
	}
	info, ok := artworkkey.ParsePortableKey(storedPath)
	if !ok || info.ImageType != artworkkey.ImageTypeCollectionBackdrop {
		t.Fatalf("stored path = %q, want a collection-backdrop key", storedPath)
	}
	got := store.keys()
	want := append(artworkkey.ObjectKeys(storedPath, info.ImageType), []string{}...)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("stored keys = %#v, expanded keys = %#v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("stored keys = %#v, expanded keys = %#v", got, want)
		}
	}
}

func TestMaterializeCollectionImage_RejectsUnknownSlot(t *testing.T) {
	_, _, err := materializeCollectionImage(
		context.Background(),
		artworkupload.NewMaterializer(newMemoryArtworkStore()),
		"logo",
		testCollectionPosterJPEG(t),
		true,
	)
	if err == nil {
		t.Fatal("expected an error for an unsupported collection artwork slot")
	}
}

func testCollectionPosterJPEG(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 32, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 6), G: uint8(y * 4), B: 120, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}
