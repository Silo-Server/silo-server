package metadata

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestImageCacheRetryDelayCaps(t *testing.T) {
	if got := imageCacheRetryDelay(1); got != time.Minute {
		t.Fatalf("attempt 1 delay = %s, want 1m", got)
	}
	if got := imageCacheRetryDelay(20); got != 2*time.Hour {
		t.Fatalf("attempt 20 delay = %s, want 2h", got)
	}
}

func TestNormalizeImageCacheJobInputSkipsNonProviderArtwork(t *testing.T) {
	for _, sourcePath := range []string{
		"",
		"tmdb/series/1396/poster/original.webp",
		"s3://media/tmdb/series/1396/poster/original.webp",
		"file:///media/poster.jpg",
		"local://poster.jpg",
		"generated://collections/1/poster.jpg",
	} {
		if got, ok := normalizeImageCacheJobInput(EnqueueImageCacheJobInput{
			TargetType:      ImageCacheTargetItem,
			TargetContentID: "series-1",
			SourcePath:      sourcePath,
			ImageType:       ImageCacheImagePoster,
		}); ok {
			t.Fatalf("normalizeImageCacheJobInput(%q) = %#v, want skipped", sourcePath, got)
		}
	}
}

func TestNormalizeImageCacheJobInputKeepsLanguageAndDefaultsAttribution(t *testing.T) {
	got, ok := normalizeImageCacheJobInput(EnqueueImageCacheJobInput{
		TargetType:      ImageCacheTargetItemLocalization,
		TargetContentID: "series-1",
		TargetLanguage:  " fr-CA ",
		SeriesID:        "series-1",
		SourcePath:      "https://image.tmdb.org/t/p/original/poster.jpg",
		ImageType:       ImageCacheImagePoster,
	})
	if !ok {
		t.Fatal("normalizeImageCacheJobInput skipped remote HTTP source")
	}
	if got.TargetLanguage != "fr-CA" {
		t.Fatalf("TargetLanguage = %q, want fr-CA", got.TargetLanguage)
	}
	if got.ProviderID != "remote" {
		t.Fatalf("ProviderID = %q, want remote for unattributed HTTP source", got.ProviderID)
	}
	if got.ProviderContentID != "series-1" {
		t.Fatalf("ProviderContentID = %q, want series-1", got.ProviderContentID)
	}
	if got.ContentType != "series" {
		t.Fatalf("ContentType = %q, want series", got.ContentType)
	}
}

func TestImageCacheProviderIDFromSourceDoesNotUseURLSchemeAsProvider(t *testing.T) {
	if got := imageCacheProviderIDFromSource("https://image.tmdb.org/t/p/original/a.jpg", "tmdb"); got != "tmdb" {
		t.Fatalf("provider from HTTP source with fallback = %q, want tmdb", got)
	}
	if got := imageCacheProviderIDFromSource("https://image.tmdb.org/t/p/original/a.jpg", ""); got != "remote" {
		t.Fatalf("provider from HTTP source without fallback = %q, want remote", got)
	}
	if got := imageCacheProviderIDFromSource("tvdb://banners/poster.jpg", "tmdb"); got != "tvdb" {
		t.Fatalf("provider from plugin URL = %q, want tvdb", got)
	}
}

func TestExpandedImageCacheMigrationDefinesTargetMatrixAndLanguageUniqueKey(t *testing.T) {
	body, err := os.ReadFile("../../migrations/sql/20260617203000_expand_metadata_image_cache_jobs.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(body)
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS target_language text NOT NULL DEFAULT ''",
		"target_type IN ('item', 'item_localization', 'season', 'season_localization', 'episode', 'person')",
		"image_type IN ('poster', 'backdrop', 'logo', 'still', 'profile')",
		"UNIQUE (target_type, target_content_id, image_type, target_language)",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
}
