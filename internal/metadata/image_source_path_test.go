package metadata

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type stubImageCacher struct{}

func (stubImageCacher) CacheImage(_ context.Context, req CacheImageRequest) (*CacheImageResult, error) {
	return &CacheImageResult{
		BasePath:  req.ProviderID + "/" + req.ContentType + "/" + req.ContentID + "/poster",
		Thumbhash: "hash",
		Ext:       ".jpg",
	}, nil
}

// Caching rewrites poster_path to a local storage key; the provider-origin
// path must survive in PosterSourcePath so outbound notification embeds can
// keep building public provider-CDN URLs.
func TestCacheItemImagesKeepsPosterSourcePath(t *testing.T) {
	s := &MetadataService{imageCacher: stubImageCacher{}}
	item := &models.MediaItem{
		Type:       "series",
		TmdbID:     "95396",
		PosterPath: "https://image.tmdb.org/t/p/original/severance.jpg",
	}
	s.cacheItemImages(context.Background(), item, []RemoteImage{
		{URL: item.PosterPath, ProviderID: "tmdb", Type: ImagePoster},
	})
	if item.PosterSourcePath != "https://image.tmdb.org/t/p/original/severance.jpg" {
		t.Fatalf("provider poster path not preserved, got %q", item.PosterSourcePath)
	}
	if item.PosterPath != "tmdb/series/95396/poster/original.jpg" {
		t.Fatalf("poster path not rewritten to cached key, got %q", item.PosterPath)
	}
}

// Already-cached posters (bare storage keys) produce no cache job, so the
// source path must not be overwritten and the caller's carry-forward applies.
func TestCacheItemImagesSkipsBareKeys(t *testing.T) {
	s := &MetadataService{imageCacher: stubImageCacher{}}
	item := &models.MediaItem{
		Type:       "series",
		TmdbID:     "95396",
		PosterPath: "tmdb/series/95396/poster/original.jpg",
	}
	s.cacheItemImages(context.Background(), item, nil)
	if item.PosterSourcePath != "" {
		t.Fatalf("bare key must not produce a source path, got %q", item.PosterSourcePath)
	}
	if item.PosterPath != "tmdb/series/95396/poster/original.jpg" {
		t.Fatalf("bare key must pass through unchanged, got %q", item.PosterPath)
	}
}
