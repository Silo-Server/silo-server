package metadata

import (
	"context"
	"testing"
)

const (
	applyImageTestExt         = ".webp"
	applyImageTestProviderID  = "tmdb"
	applyImageTestContentType = "movie"
)

type recordingImageCacher struct {
	request CacheImageRequest
}

func (c *recordingImageCacher) CacheImage(_ context.Context, req CacheImageRequest) (*CacheImageResult, error) {
	c.request = req
	return &CacheImageResult{
		BasePath:     "tmdb/movies/335984/poster",
		OriginalPath: "tmdb/movies/335984/poster/original.new-revision.webp",
		Revision:     "new-revision",
		Thumbhash:    "new-thumbhash",
		Ext:          applyImageTestExt,
	}, nil
}

func TestApplyItemImageReturnsExactImmutableRevision(t *testing.T) {
	cacher := &recordingImageCacher{}
	service := &MetadataService{imageCacher: cacher}

	result, err := service.ApplyItemImage(context.Background(), ApplyItemImageRequest{
		OriginalURL: "https://image.tmdb.org/t/p/original/new-poster.jpg",
		ProviderID:  applyImageTestProviderID,
		ContentType: applyImageTestContentType,
		ContentID:   "335984",
		ImageType:   ImagePoster,
	})
	if err != nil {
		t.Fatalf("ApplyItemImage: %v", err)
	}
	if result.StoredPath != "tmdb/movies/335984/poster/original.new-revision.webp" {
		t.Fatalf("StoredPath = %q, want exact immutable revision path", result.StoredPath)
	}
	if result.Revision != "new-revision" {
		t.Fatalf("Revision = %q, want new-revision", result.Revision)
	}
}
