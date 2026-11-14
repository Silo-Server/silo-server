package catalog

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// TestResolveProviderAliasesSkipsItemsWithAnotherTMDBIDDB pins that an IMDb
// or TVDB alias only resolves a watchlist title to an item that is not
// another title by its type's anchor precedence. A movie holding a different
// TMDB ID is another movie, and promoting the watchlisted title onto it would
// replace the user's pick; a series sharing the title's TVDB ID is the same
// show whatever its TMDB ID.
func TestResolveProviderAliasesSkipsItemsWithAnotherTMDBIDDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	id := func(n int) string { return fmt.Sprintf("%d%d", 9_000+n, suffix%1_000_000_000) }

	var lib int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("provider-aliases-%d", suffix),
	).Scan(&lib); err != nil {
		t.Fatalf("seed folder: %v", err)
	}

	type item struct{ name, itemType, tmdb, tvdb, imdb, mipTMDB, staleTMDB string }
	items := []item{
		// Another title: its own TMDB ID, plus IDs a watchlist title shares.
		{name: "other", tmdb: id(1), tvdb: id(2), imdb: "tt" + id(3)},
		// No TMDB column, but a durable TMDB provider ID for another title.
		{name: "other-durable", imdb: "tt" + id(4), mipTMDB: id(5)},
		// No TMDB ID anywhere: the case IMDb and TVDB matching exists for.
		{name: "missing", imdb: "tt" + id(6)},
		// One of the title's TMDB IDs in TMDB's "id-slug" form.
		{name: "slug", tmdb: id(7) + "-some-title", imdb: "tt" + id(8)},
		// Only a TMDB ID the provider rejected: not the item's ID.
		{name: "stale", imdb: "tt" + id(9), staleTMDB: id(10)},
		// No TMDB ID, so IMDb decides: a different one is another movie.
		{name: "other-imdb", tvdb: id(11), imdb: "tt" + id(12)},
		// The title's TMDB ID stored with surrounding spaces.
		{name: "padded", tmdb: " " + id(16) + " ", imdb: "tt" + id(17)},
		// A series is anchored by TVDB first.
		{name: "show", itemType: "series", tmdb: id(13), tvdb: id(14), imdb: "tt" + id(15)},
	}
	contentID := func(name string) string { return fmt.Sprintf("provider-aliases-%s-%d", name, suffix) }
	var contentIDs []string
	for _, it := range items {
		cid := contentID(it.name)
		contentIDs = append(contentIDs, cid)
		if it.itemType == "" {
			it.itemType = "movie"
		}
		batchEquivExec(t, pool, `
			INSERT INTO media_items (content_id, type, title, tmdb_id, tvdb_id, imdb_id)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			cid, it.itemType, it.name, it.tmdb, it.tvdb, it.imdb)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, cid, lib)
		if it.mipTMDB != "" {
			batchEquivExec(t, pool, `
				INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
				VALUES ($1, $2, 'tmdb', $3)`, cid, it.itemType, it.mipTMDB)
		}
		if it.staleTMDB != "" {
			batchEquivExec(t, pool, `
				INSERT INTO stale_media_ids (content_id, provider, provider_id) VALUES ($1, 'tmdb', $2)`, cid, it.staleTMDB)
		}
	}
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, contentIDs)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = $1`, lib)
	})

	titleTMDB := id(20)
	alias := func(key int64, provider, providerID string) ProviderAlias {
		return ProviderAlias{Key: key, MediaType: "movie", Provider: provider, ProviderID: providerID}
	}
	seriesAlias := func(key int64, provider, providerID string) ProviderAlias {
		a := alias(key, provider, providerID)
		a.MediaType = "series"
		return a
	}
	cases := []struct {
		name    string
		aliases []ProviderAlias
		want    string
	}{
		{"imdb on an item with another tmdb id", []ProviderAlias{alias(1, "tmdb", titleTMDB), alias(1, "imdb", "tt"+id(3))}, ""},
		{"tvdb on an item with another tmdb id", []ProviderAlias{alias(1, "tmdb", titleTMDB), alias(1, "tvdb", id(2))}, ""},
		{"imdb on an item with another durable tmdb id", []ProviderAlias{alias(1, "tmdb", titleTMDB), alias(1, "imdb", "tt"+id(4))}, ""},
		{"imdb on an item without a tmdb id", []ProviderAlias{alias(1, "tmdb", titleTMDB), alias(1, "imdb", "tt"+id(6))}, "missing"},
		{"imdb on an item with a former tmdb alias, slugged", []ProviderAlias{alias(1, "tmdb", titleTMDB), alias(1, "tmdb", id(7)), alias(1, "imdb", "tt"+id(8))}, "slug"},
		{"imdb on an item with only a rejected tmdb id", []ProviderAlias{alias(1, "tmdb", titleTMDB), alias(1, "imdb", "tt"+id(9))}, "stale"},
		{"imdb with no tmdb alias to conflict with", []ProviderAlias{alias(1, "imdb", "tt"+id(3))}, "other"},
		{"tvdb on an item without a tmdb id but another imdb id", []ProviderAlias{alias(1, "tmdb", titleTMDB), alias(1, "imdb", "tt"+id(20)), alias(1, "tvdb", id(11))}, ""},
		{"imdb on an item with the tmdb id padded", []ProviderAlias{alias(1, "tmdb", id(16)), alias(1, "imdb", "tt"+id(17))}, "padded"},
		{"series tvdb on a show with another tmdb id", []ProviderAlias{seriesAlias(1, "tmdb", titleTMDB), seriesAlias(1, "tvdb", id(14))}, "show"},
		// A TMDB match always counts: a watchlist title is a TMDB entry, so a
		// show holding its TMDB ID is that title whatever TVDB ID it carries.
		{"series tmdb on a show with another tvdb id", []ProviderAlias{seriesAlias(1, "tmdb", id(13)), seriesAlias(1, "tvdb", id(22))}, "show"},
		{"series imdb on a show with another tvdb id", []ProviderAlias{seriesAlias(1, "tmdb", titleTMDB), seriesAlias(1, "tvdb", id(21)), seriesAlias(1, "imdb", "tt"+id(15))}, ""},
	}
	repo := NewItemRepository(pool)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.ResolveProviderAliases(ctx, tc.aliases)
			if err != nil {
				t.Fatalf("ResolveProviderAliases: %v", err)
			}
			var want []string
			if tc.want != "" {
				want = []string{contentID(tc.want)}
			}
			if !slices.Equal(got[1], want) {
				t.Fatalf("key 1 resolved to %v, want %v", got[1], want)
			}
		})
	}
}
