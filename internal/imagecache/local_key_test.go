package imagecache

import (
	"context"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

func TestCacheBytesKeyDiscriminatorPrecedesImageType(t *testing.T) {
	s3 := &mockS3{bucket: "images"}
	cacher := New(s3)
	data := makeTestJPEG(t)

	res, err := cacher.CacheBytes(context.Background(), data, CacheRequest{
		ProviderID:       "local",
		ContentType:      "movies",
		ContentID:        "movie-1",
		ImageType:        metadata.ImagePoster,
		KeyDiscriminator: "deadbeef",
	})
	if err != nil {
		t.Fatalf("CacheBytes: %v", err)
	}
	// Hash segment sits BEFORE the image type so the variant's parent
	// directory stays the image type (imageTypeFromCachedPath contract).
	if res.BasePath != "local/movies/movie-1/deadbeef/poster" {
		t.Fatalf("BasePath = %q", res.BasePath)
	}
	for _, call := range s3.calls {
		if !strings.HasPrefix(call.key, "local/movies/movie-1/deadbeef/poster/") {
			t.Fatalf("uploaded key %q outside discriminated prefix", call.key)
		}
	}
}

func TestCacheBytesWithoutDiscriminatorKeepsLegacyLayout(t *testing.T) {
	s3 := &mockS3{bucket: "images"}
	cacher := New(s3)

	res, err := cacher.CacheBytes(context.Background(), makeTestJPEG(t), CacheRequest{
		ProviderID:  "local",
		ContentType: "ebooks",
		ContentID:   "book-1",
		ImageType:   metadata.ImagePoster,
	})
	if err != nil {
		t.Fatalf("CacheBytes: %v", err)
	}
	if res.BasePath != "local/ebooks/book-1/poster" {
		t.Fatalf("BasePath = %q", res.BasePath)
	}
}

func TestCacheImageBytesAdapter(t *testing.T) {
	s3 := &mockS3{bucket: "images"}
	cacher := New(s3)

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
	if res.BasePath != "local/movies/movie-1/cafef00d/backdrop" {
		t.Fatalf("BasePath = %q", res.BasePath)
	}
	if res.Thumbhash == "" {
		t.Fatal("thumbhash missing")
	}
}
