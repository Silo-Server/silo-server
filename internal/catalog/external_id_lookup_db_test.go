package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestExternalIDLookupSkipsItemsWithAnotherTMDBIDDB pins that a TVDB or IMDb
// match only stands in for a TMDB match when the item has no TMDB ID of its
// own, or the same one. An item that carries a different TMDB ID is another
// title with stale or wrong IDs, and request presence must not report the
// requested title as available because of it.
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

	type item struct{ contentID, tmdb, tvdb, imdb, mipProvider, mipID string }
	items := []item{
		// Another title: its own TMDB ID, plus IDs a request might share.
		{contentID: "other", tmdb: id(1), tvdb: id(2), imdb: "tt" + id(3), mipProvider: "tvdb", mipID: id(4)},
		// No TMDB ID anywhere: the case the fallback exists for.
		{contentID: "missing", imdb: "tt" + id(7)},
		// The requested TMDB ID in TMDB's "id-slug" URL form.
		{contentID: "slug", tmdb: id(8) + "-some-title", imdb: "tt" + id(9)},
		// The requested TMDB ID typed with spaces in the admin metadata editor.
		{contentID: "padded", tmdb: " " + id(5) + " ", imdb: "tt" + id(6)},
	}
	for _, it := range items {
		contentID := fmt.Sprintf("external-id-lookup-%s-%d", it.contentID, suffix)
		contentIDs = append(contentIDs, contentID)
		batchEquivExec(t, pool, `
			INSERT INTO media_items (content_id, type, title, tmdb_id, tvdb_id, imdb_id)
			VALUES ($1, 'movie', $2, $3, $4, $5)`,
			contentID, it.contentID, it.tmdb, it.tvdb, it.imdb)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, lib)
		if it.mipProvider != "" {
			batchEquivExec(t, pool, `
				INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
				VALUES ($1, 'movie', $2, $3)`, contentID, it.mipProvider, it.mipID)
		}
	}

	requested := id(10)
	cases := []struct {
		name      string
		candidate ExternalIDLookupCandidate
		wantTitle string
	}{
		{"imdb on an item with another tmdb id", ExternalIDLookupCandidate{TMDBID: requested, IMDbID: "tt" + id(3)}, ""},
		{"tvdb on an item with another tmdb id", ExternalIDLookupCandidate{TMDBID: requested, TVDBID: id(2)}, ""},
		{"durable tvdb on an item with another tmdb id", ExternalIDLookupCandidate{TMDBID: requested, TVDBID: id(4)}, ""},
		{"imdb on an item without a tmdb id", ExternalIDLookupCandidate{TMDBID: requested, IMDbID: "tt" + id(7)}, "missing"},
		{"imdb on an item with the slugged tmdb id", ExternalIDLookupCandidate{TMDBID: id(8), IMDbID: "tt" + id(9)}, "slug"},
		{"imdb on an item with the padded tmdb id", ExternalIDLookupCandidate{TMDBID: id(5), IMDbID: "tt" + id(6)}, "padded"},
	}
	repo := NewItemRepository(pool)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := repo.LookupExternalIDs(ctx, "movie", []ExternalIDLookupCandidate{tc.candidate})
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
