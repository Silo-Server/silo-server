package metadata

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
)

// TestLadderBackfillCandidatesDB runs the ladder backfill candidate query
// against the migrated schema. Every cached backdrop surface (the item, its
// localization, and an extra backdrop) stays a candidate while its manifest
// predates the backdrop's widest rung, and drops out once the manifest lists
// it.
func TestLadderBackfillCandidatesDB(t *testing.T) {
	pool := imageCacheQueueTestPool(t)

	// EXPLAIN plans every UNION arm, so a column the schema lacks fails here.
	if _, err := pool.Exec(t.Context(), "EXPLAIN "+ladderCandidateRowsSQL()); err != nil {
		t.Fatalf("EXPLAIN ladder candidates: %v", err)
	}

	contentID := fmt.Sprintf("zz-ladder-backfill-%d", time.Now().UnixNano())
	originals, extraID := seedLadderBackdrops(t, pool, contentID)
	all := []string{ImageCacheTargetItem, ImageCacheTargetItemBackdrop, ImageCacheTargetItemLocalization}

	if got := ladderBackdropCandidates(t, pool, contentID, extraID); !slices.Equal(got, all) {
		t.Fatalf("without manifests, candidates = %v, want %v", got, all)
	}

	// Artwork cached under the previous ladder has every rung but the new one.
	recordLadderManifests(t, pool, originals, []string{"original", "w1920", "w1280", "w300"})
	if got := ladderBackdropCandidates(t, pool, contentID, extraID); !slices.Equal(got, all) {
		t.Fatalf("with previous-ladder manifests, candidates = %v, want %v", got, all)
	}

	recordLadderManifests(t, pool, originals, artworkkey.VariantNames(artworkkey.ImageBackdrop))
	if got := ladderBackdropCandidates(t, pool, contentID, extraID); len(got) != 0 {
		t.Fatalf("with current-ladder manifests, candidates = %v, want none", got)
	}
}

// seedLadderBackdrops caches a provider backdrop on the item, its German
// localization, and one extra backdrop row, returning each surface's original
// key and the extra backdrop's row ID.
func seedLadderBackdrops(t *testing.T, pool *pgxpool.Pool, contentID string) (map[string]string, int64) {
	t.Helper()
	ctx := t.Context()
	base := "tmdb/movies/" + contentID
	originals := map[string]string{
		ImageCacheTargetItem:             base + "/backdrop/original.r1.webp",
		ImageCacheTargetItemLocalization: base + "/localizations/de/backdrop/original.r2.webp",
		ImageCacheTargetItemBackdrop:     base + "/x1/backdrop/original.r3.webp",
	}
	cleanup := func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM media_item_localizations WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(bg, `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1 || '%'`, base)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, tmdb_id, backdrop_source_path, backdrop_path)
		VALUES ($1, 'movie', 'Ladder backfill', '1', 'https://image.tmdb.org/t/p/original/item.jpg', $2)
	`, contentID, originals[ImageCacheTargetItem]); err != nil {
		t.Fatalf("seed media item: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_localizations (content_id, language, backdrop_source_path, backdrop_path)
		VALUES ($1, 'de', 'https://image.tmdb.org/t/p/original/de.jpg', $2)
	`, contentID, originals[ImageCacheTargetItemLocalization]); err != nil {
		t.Fatalf("seed localization: %v", err)
	}
	var extraID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_item_backdrops (content_id, ordinal, source_path, path)
		VALUES ($1, 1, 'https://image.tmdb.org/t/p/original/extra.jpg', $2)
		RETURNING id
	`, contentID, originals[ImageCacheTargetItemBackdrop]).Scan(&extraID); err != nil {
		t.Fatalf("seed extra backdrop: %v", err)
	}
	return originals, extraID
}

// ladderBackdropCandidates returns the target types of the item's backdrop
// ladder candidates, sorted, checking that the extra backdrop is targeted by
// its row ID.
func ladderBackdropCandidates(t *testing.T, pool *pgxpool.Pool, contentID string, extraID int64) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT c.target_type, c.target_content_id
		FROM (`+ladderCandidateRowsSQL()+`) c
		WHERE c.image_type = 'backdrop' AND c.series_id = $1
		ORDER BY c.target_type
	`, contentID)
	if err != nil {
		t.Fatalf("query ladder candidates: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var targetType, targetID string
		if err := rows.Scan(&targetType, &targetID); err != nil {
			t.Fatalf("scan ladder candidate: %v", err)
		}
		if targetType == ImageCacheTargetItemBackdrop && targetID != strconv.FormatInt(extraID, 10) {
			t.Fatalf("extra backdrop target_content_id = %q, want its row id %d", targetID, extraID)
		}
		got = append(got, targetType)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate ladder candidates: %v", err)
	}
	return got
}

// recordLadderManifests records a revision manifest listing variants for each
// original, as the cacher does after a successful upload.
func recordLadderManifests(t *testing.T, pool *pgxpool.Pool, originals map[string]string, variants []string) {
	t.Helper()
	for _, original := range originals {
		keys := make([]string, 0, len(variants))
		for _, variant := range variants {
			keys = append(keys, artworkkey.Variant(original, variant))
		}
		if _, err := pool.Exec(t.Context(), `
			INSERT INTO artwork_revision_gc_candidates (original_path, image_type, object_keys, not_before)
			VALUES ($1, 'backdrop', $2, NOW())
			ON CONFLICT (original_path) DO UPDATE SET object_keys = EXCLUDED.object_keys
		`, original, keys); err != nil {
			t.Fatalf("record manifest for %s: %v", original, err)
		}
	}
}
