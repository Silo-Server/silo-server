package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestExternalIDLookupSkipsItemsWithAnotherTMDBIDDB pins that a TVDB or IMDb
// match only stands in for a TMDB match when the item is the same title by
// its type's anchor precedence. A movie that carries a different TMDB ID, or
// a series with a different TVDB ID, is another title with stale or wrong
// IDs, and request presence must not report the requested title as available
// because of it.
func TestExternalIDLookupSkipsItemsWithAnotherTMDBIDDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	id := func(n int) string { return fmt.Sprintf("%d%d", 9_000+n, suffix%1_000_000_000) }

	var lib int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("external-id-lookup-%d", suffix),
	).Scan(&lib); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	var contentIDs []string
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, contentIDs)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = $1`, lib)
	})

	type item struct{ contentID, itemType, tmdb, tvdb, imdb, mipProvider, mipID string }
	items := []item{
		// Another movie: its own TMDB ID, plus IDs a request might share.
		{contentID: "other", itemType: "movie", tmdb: id(1), tvdb: id(2), imdb: "tt" + id(3), mipProvider: "tvdb", mipID: id(4)},
		// No TMDB ID: the case the fallback exists for.
		{contentID: "missing", itemType: "movie", imdb: "tt" + id(7)},
		// The requested TMDB ID in TMDB's "id-slug" URL form.
		{contentID: "slug", itemType: "movie", tmdb: id(8) + "-some-title", imdb: "tt" + id(9)},
		// The requested TMDB ID typed with whitespace in the admin metadata editor.
		{contentID: "padded", itemType: "movie", tmdb: "\t" + id(5) + "\n", imdb: "tt" + id(6)},
		// A series is anchored on TVDB, so its TMDB ID does not decide.
		{contentID: "show", itemType: "series", tmdb: id(11), tvdb: id(12), imdb: "tt" + id(13)},
	}
	for _, it := range items {
		contentID := fmt.Sprintf("external-id-lookup-%s-%d", it.contentID, suffix)
		contentIDs = append(contentIDs, contentID)
		batchEquivExec(t, pool, `
			INSERT INTO media_items (content_id, type, title, tmdb_id, tvdb_id, imdb_id)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			contentID, it.itemType, it.contentID, it.tmdb, it.tvdb, it.imdb)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, lib)
		if it.mipProvider != "" {
			batchEquivExec(t, pool, `
				INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
				VALUES ($1, $2, $3, $4)`, contentID, it.itemType, it.mipProvider, it.mipID)
		}
	}

	requested := id(10)
	cases := []struct {
		name      string
		mediaType string
		candidate ExternalIDLookupCandidate
		wantTitle string
	}{
		{"imdb on a movie with another tmdb id", "movie", ExternalIDLookupCandidate{TMDBID: requested, IMDbID: "tt" + id(3)}, ""},
		{"tvdb on a movie with another tmdb id", "movie", ExternalIDLookupCandidate{TMDBID: requested, TVDBID: id(2)}, ""},
		{"durable tvdb on a movie with another tmdb id", "movie", ExternalIDLookupCandidate{TMDBID: requested, TVDBID: id(4)}, ""},
		{"rejected tvdb falls through to an imdb match", "movie", ExternalIDLookupCandidate{TMDBID: requested, TVDBID: id(2), IMDbID: "tt" + id(7)}, "missing"},
		{"imdb on a movie without a tmdb id", "movie", ExternalIDLookupCandidate{TMDBID: requested, IMDbID: "tt" + id(7)}, "missing"},
		{"imdb on a movie with the slugged tmdb id", "movie", ExternalIDLookupCandidate{TMDBID: id(8), IMDbID: "tt" + id(9)}, "slug"},
		{"imdb on a movie with the padded tmdb id", "movie", ExternalIDLookupCandidate{TMDBID: id(5), IMDbID: "tt" + id(6)}, "padded"},
		{"tvdb on a series with another tmdb id", "series", ExternalIDLookupCandidate{TMDBID: requested, TVDBID: id(12)}, "show"},
		{"imdb on a series with another tvdb id", "series", ExternalIDLookupCandidate{TMDBID: requested, TVDBID: id(14), IMDbID: "tt" + id(13)}, ""},
	}
	repo := NewItemRepository(pool)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := repo.LookupExternalIDs(ctx, tc.mediaType, []ExternalIDLookupCandidate{tc.candidate})
			if err != nil {
				t.Fatalf("LookupExternalIDs: %v", err)
			}
			got := ""
			if len(rows) > 0 {
				got = rows[0].Title
			}
			if len(rows) > 1 || got != tc.wantTitle {
				t.Fatalf("matched %+v, want title %q", rows, tc.wantTitle)
			}
		})
	}
}
