package metadata

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
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
	for _, errText := range []string{
		"imagecache: download https://example.invalid/missing.jpg: unexpected status 404",
		"local image is not a regular file allowed for artwork: /media/movie/poster.jpg",
	} {
		got := classifyImageCacheFailure(imageCacheMaxAttempts-1, errText)
		if got.status != ImageCacheStatusFailed {
			t.Fatalf("status for %q = %q, want %q", errText, got.status, ImageCacheStatusFailed)
		}
		if got.retryDelay != imageCachePermanentPark {
			t.Fatalf("retry delay for %q = %s, want the permanent park %s", errText, got.retryDelay, imageCachePermanentPark)
		}
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

func TestNaturalArtworkRepairTargetMigrationKeysSeriesChildrenByNumbers(t *testing.T) {
	body, err := os.ReadFile("../../migrations/sql/20260826045932_natural_artwork_repair_targets.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(body)
	for _, want := range []string{
		"SET target_content_id = series_id",
		"UNIQUE NULLS NOT DISTINCT",
		"target_language,\n            season_number,\n            episode_number",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("natural-target migration missing %q", want)
		}
	}
}

func TestCurrentTargetSourceQueriesUseSeriesNaturalKeys(t *testing.T) {
	season, episode := 2, 7
	tests := []struct {
		name     string
		job      *models.MetadataImageCacheJob
		wantSQL  []string
		wantArgs []any
	}{
		{
			name: "season",
			job: &models.MetadataImageCacheJob{
				TargetType: ImageCacheTargetSeason, TargetContentID: "series-1", SeasonNumber: &season,
			},
			wantSQL:  []string{"FROM seasons", "series_id = $1", "season_number = $2"},
			wantArgs: []any{"series-1", 2},
		},
		{
			name: "season localization",
			job: &models.MetadataImageCacheJob{
				TargetType: ImageCacheTargetSeasonLocalization, TargetContentID: "series-1", TargetLanguage: "fr", SeasonNumber: &season,
			},
			wantSQL:  []string{"FROM season_localizations", "JOIN seasons", "s.series_id = $1", "s.season_number = $2", "loc.language = $3"},
			wantArgs: []any{"series-1", 2, "fr"},
		},
		{
			name: "episode",
			job: &models.MetadataImageCacheJob{
				TargetType: ImageCacheTargetEpisode, TargetContentID: "series-1", SeasonNumber: &season, EpisodeNumber: &episode,
			},
			wantSQL:  []string{"FROM episodes", "series_id = $1", "season_number = $2", "episode_number = $3"},
			wantArgs: []any{"series-1", 2, 7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, args, ok := currentTargetSourceQuery(tt.job)
			if !ok {
				t.Fatal("currentTargetSourceQuery rejected a valid natural target")
			}
			for _, want := range tt.wantSQL {
				if !strings.Contains(query, want) {
					t.Fatalf("query missing %q:\n%s", want, query)
				}
			}
			if !reflect.DeepEqual(args, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", args, tt.wantArgs)
			}
		})
	}
}

// The ladder sweep enqueues jobs the processor then looks up by natural key
// (series id plus season/episode number). Emitting a child row's own content id
// would queue jobs no lookup can ever match, so they fail "repair target not
// found" every cooldown and the ladder version is never recorded.
func TestLadderBackfillCandidatesUseSeriesNaturalKeys(t *testing.T) {
	rows := collapseSQLWhitespace(ladderCandidateRowsSQL())
	for _, want := range []string{
		"'season'::text, s.series_id AS target_content_id,",
		"'season_localization'::text, s.series_id, loc.language,",
		"'episode'::text, e.series_id, ''::text, e.series_id,",
		"s.season_number, NULL::integer AS episode_number,",
		"e.season_number, e.episode_number,",
	} {
		if !strings.Contains(rows, want) {
			t.Fatalf("ladder candidate rows missing %q:\n%s", want, rows)
		}
	}
	for _, unwanted := range []string{
		"'season'::text, s.content_id",
		"'season_localization'::text, s.content_id",
		"'episode'::text, e.content_id",
	} {
		if strings.Contains(rows, unwanted) {
			t.Fatalf("ladder candidate rows still target a child content id: %q", unwanted)
		}
	}

	query := collapseSQLWhitespace(ladderBackfillCandidateQuerySQL())
	for _, want := range []string{
		"j.season_number IS NOT DISTINCT FROM ac.season_number",
		"j.episode_number IS NOT DISTINCT FROM ac.episode_number",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("ladder dedup join missing %q:\n%s", want, query)
		}
	}
}

func collapseSQLWhitespace(sql string) string {
	return strings.Join(strings.Fields(sql), " ")
}
