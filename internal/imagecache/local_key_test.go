package imagecache

import (
	"context"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

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
	for _, call := range s3.calls {
		if !strings.HasPrefix(call.key, "local/movies/movie-1/cafef00d/backdrop/") {
			t.Fatalf("uploaded key %q outside discriminated prefix", call.key)
		}
	}
}
