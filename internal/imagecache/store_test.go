package imagecache

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

func TestCacheImageBytesAdapter(t *testing.T) {
	store := &mockStore{}
	cacher := newWithHTTPClient(store, nil)

	res, err := cacher.CacheImageBytes(context.Background(), makeTestJPEG(t), metadata.CacheImageRequest{
		ProviderID:       "local",
		ContentType:      "movies",
		ContentID:        "movie-1",
		ImageType:        metadata.ImageBackdrop,
		KeyDiscriminator: "cafef00d",
	})
	if err != nil {
		t.Fatalf("CacheImageBytes: %v", err)
	}
	if res.Thumbhash == "" {
		t.Fatal("thumbhash missing")
	}
	// metadata.CachedImageOriginalPath is what the processor publishes.
	original := metadata.CachedImageOriginalPath(res)
	requirePortableKey(t, original, "backdrop")
	if artworkkey.Revision(original) != res.Revision {
		t.Fatalf("revision from key = %q, want %q", artworkkey.Revision(original), res.Revision)
	}
	if !strings.HasPrefix(res.BasePath, artworkkey.PortableObjectsPrefix+"/") {
		t.Fatalf("BasePath = %q, want a portable revision directory", res.BasePath)
	}
}

func TestCacheAudiobookAndEbookCoversMaterializeIdentically(t *testing.T) {
	store := &mockStore{}
	cacher := newWithHTTPClient(store, nil)
	data := makeTestJPEG(t)

	audiobook, hash, err := cacher.CacheAudiobookCover(context.Background(), data, "book-1")
	if err != nil {
		t.Fatalf("CacheAudiobookCover: %v", err)
	}
	if hash == "" {
		t.Fatal("thumbhash missing")
	}
	ebook, _, err := cacher.CacheEbookCover(context.Background(), data, "book-2")
	if err != nil {
		t.Fatalf("CacheEbookCover: %v", err)
	}
	if audiobook != ebook {
		t.Fatalf("identical cover bytes stored at %q and %q", audiobook, ebook)
	}
	requirePortableKey(t, audiobook, "poster")
}

func TestWriteObjectReturnsLastErrorAfterRetries(t *testing.T) {
	store := &countingStore{err: errors.New("backend down")}
	c := New(store)

	err := c.writeObject(context.Background(), "artwork/v1/objects/poster/aa/bb/original.webp", []byte("x"), "image/webp")
	if err == nil || !strings.Contains(err.Error(), "backend down") {
		t.Fatalf("writeObject error = %v, want the backend failure", err)
	}
	if got := store.attempts(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}
