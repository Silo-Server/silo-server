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

func TestImageCacheFailureRetryDelayDefersStableProviderFailures(t *testing.T) {
	tests := []string{
		"imagecache: download https://example.invalid/missing.jpg: unexpected status 403",
		"imagecache: download https://example.invalid/missing.jpg: unexpected status 404",
		"imagecache: download https://example.invalid/missing.jpg: unexpected status 410",
		"imagecache: download https://example.invalid/missing.jpg: unexpected status 418",
	}
	for _, errText := range tests {
		if got := imageCacheFailureRetryDelay(1, errText); got != 7*24*time.Hour {
			t.Fatalf("imageCacheFailureRetryDelay(%q) = %s, want 7d", errText, got)
		}
	}
	if got := imageCacheFailureRetryDelay(1, "temporary network error"); got != time.Minute {
		t.Fatalf("transient failure delay = %s, want 1m", got)
	}
}

func TestClassifyImageCacheFailureRetriesEmptyResolverURL(t *testing.T) {
	// The resolver also returns an empty URL while a plugin is disabled,
	// upgrading, or still loading, so this must never tombstone artwork on the
	// first attempt.
	got := classifyImageCacheFailure(0, imageCacheEmptyResolvedURLError)
	if got.status != ImageCacheStatusQueued {
		t.Fatalf("status = %q, want %q", got.status, ImageCacheStatusQueued)
	}
	if got.attempt != 1 {
		t.Fatalf("attempt = %d, want 1", got.attempt)
	}
	if got.retryDelay != time.Minute {
		t.Fatalf("retry delay = %s, want 1m", got.retryDelay)
	}
}

func TestClassifyImageCacheFailureRetriesTransientError(t *testing.T) {
	got := classifyImageCacheFailure(0, "temporary network error")
	if got.status != ImageCacheStatusQueued {
		t.Fatalf("status = %q, want %q", got.status, ImageCacheStatusQueued)
	}
	if got.attempt != 1 {
		t.Fatalf("attempt = %d, want 1", got.attempt)
	}
	if got.retryDelay != time.Minute {
		t.Fatalf("retry delay = %s, want 1m", got.retryDelay)
	}
}

func TestClassifyImageCacheFailureParksExhaustedTransientErrorRecoverably(t *testing.T) {
	got := classifyImageCacheFailure(imageCacheMaxAttempts-1, "temporary network error")
	if got.status != ImageCacheStatusFailed {
		t.Fatalf("status = %q, want %q", got.status, ImageCacheStatusFailed)
	}
	if got.attempt != imageCacheMaxAttempts {
		t.Fatalf("attempt = %d, want %d", got.attempt, imageCacheMaxAttempts)
	}
	if got.retryDelay != imageCacheFailedCooldown {
		t.Fatalf("retry delay = %s, want the recoverable cooldown %s", got.retryDelay, imageCacheFailedCooldown)
	}
	if got.retryDelay >= imageCachePermanentPark {
		t.Fatal("an outage must not park a job past the recovery window")
	}
}

func TestClassifyImageCacheFailureTombstonesExhaustedStableFailure(t *testing.T) {
	got := classifyImageCacheFailure(
		imageCacheMaxAttempts-1,
		"imagecache: download https://example.invalid/missing.jpg: unexpected status 404",
	)
	if got.status != ImageCacheStatusFailed {
		t.Fatalf("status = %q, want %q", got.status, ImageCacheStatusFailed)
	}
	if got.retryDelay != imageCachePermanentPark {
		t.Fatalf("retry delay = %s, want the permanent park %s", got.retryDelay, imageCachePermanentPark)
	}
}

func TestNormalizeImageCacheJobInputSkipsNonProviderArtwork(t *testing.T) {
	for _, sourcePath := range []string{
		"",
		"tmdb/series/1396/poster/original.webp",
		"s3://media/tmdb/series/1396/poster/original.webp",
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

func TestImageCacheDiscoverySurfacesUseBoundedNativeKeyPages(t *testing.T) {
	providerColumns := [...]string{
		"mi.poster_source_path",
		"mi.backdrop_source_path",
		"mi.logo_source_path",
		"loc.poster_source_path",
		"loc.backdrop_source_path",
		"loc.logo_source_path",
		"s.poster_source_path",
		"loc.poster_source_path",
		"e.still_source_path",
		"p.photo_source_path",
	}
	uncachedColumns := [...]string{
		"mi.poster_path",
		"mi.backdrop_path",
		"mi.logo_path",
		"loc.poster_path",
		"loc.backdrop_path",
		"loc.logo_path",
		"s.poster_path",
		"loc.poster_path",
		"e.still_path",
		"p.photo_path",
	}
	for surface := 0; surface < imageCacheDiscoverySurfaceCount; surface++ {
		cursor := imageCacheDiscoveryCursor{Surface: surface, Key: "content-10", Subkey: "fr", NumericKey: 10}
		query, args := imageCacheDiscoveryQuery(cursor, 1000)
		if !strings.Contains(query, "LIMIT $1") {
			t.Fatalf("surface %d query is not page-bounded", surface)
		}
		if strings.Contains(strings.ToUpper(query), "UNION") {
			t.Fatalf("surface %d query rebuilds a cross-surface union", surface)
		}
		if strings.Contains(query, "%!") || !strings.Contains(query, "LIKE '%://%'") {
			t.Fatalf("surface %d query has a malformed provider-source predicate", surface)
		}
		providerPredicate := "AND " + providerColumns[surface] + " LIKE '%://%'"
		if !strings.Contains(query, providerPredicate) {
			t.Fatalf("surface %d query missing exact provider predicate %q:\n%s", surface, providerPredicate, query)
		}
		if strings.Contains(query, providerColumns[surface]+" "+providerColumns[surface]) {
			t.Fatalf("surface %d query duplicates provider column %q:\n%s", surface, providerColumns[surface], query)
		}
		uncachedPredicate := "AND (" + uncachedColumns[surface] + " LIKE '%://%' OR coalesce(" + uncachedColumns[surface] + ", '') = '')"
		if !strings.Contains(query, uncachedPredicate) {
			t.Fatalf("surface %d query missing exact uncached-target predicate %q:\n%s", surface, uncachedPredicate, query)
		}
		if strings.Contains(query, "@nonProviderSchemes") || !strings.Contains(query, nonProviderImageSchemesSQL) {
			t.Fatalf("surface %d query did not expand the non-provider scheme filter:\n%s", surface, query)
		}
		if !strings.Contains(query, "LEFT JOIN LATERAL") ||
			!strings.Contains(query, "FROM metadata_image_cache_jobs j") ||
			!strings.Contains(query, `j.target_content_id = c.target_content_id COLLATE "default"`) ||
			!strings.Contains(query, "j.source_path IS DISTINCT FROM c.source_path") {
			t.Fatalf("surface %d query is missing the indexed eligibility lookup:\n%s", surface, query)
		}
		wantArgs := 2
		if (surface >= 3 && surface <= 5) || surface == 7 {
			wantArgs = 3
		}
		if len(args) != wantArgs || args[0] != 1000 {
			t.Fatalf("surface %d args = %#v, want page limit and native cursor", surface, args)
		}
	}

	localizedQuery, localizedArgs := imageCacheDiscoveryQuery(imageCacheDiscoveryCursor{Surface: 3}, 1000)
	if !strings.Contains(localizedQuery, "loc.language > $3") || len(localizedArgs) != 3 {
		t.Fatalf("localized discovery does not use its composite primary key: args=%#v", localizedArgs)
	}
	personQuery, personArgs := imageCacheDiscoveryQuery(imageCacheDiscoveryCursor{Surface: 9, NumericKey: 41}, 1000)
	if !strings.Contains(personQuery, "p.id > $2") || len(personArgs) != 2 || personArgs[1] != int64(41) {
		t.Fatalf("person discovery does not preserve its numeric cursor: args=%#v", personArgs)
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
