package catalog

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// A title can be known to a provider only through its provider ID rows; the
// batched lookup and the list of titles without a TMDB ID both read them.
func TestExternalIDLookupsReadProviderIDRowsDB(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	suffix := time.Now().UnixNano()

	direct := fmt.Sprintf("ext-direct-%d", suffix)         // TMDB ID in its column
	providerOnly := fmt.Sprintf("ext-provider-%d", suffix) // TMDB ID only in a provider row
	orphan := fmt.Sprintf("ext-orphan-%d", suffix)         // IMDb ID only
	noIDs := fmt.Sprintf("ext-none-%d", suffix)            // no external ID at all
	directTMDB := fmt.Sprintf("97%d", suffix%10000000)
	providerTMDB := fmt.Sprintf("98%d", suffix%10000000)
	orphanIMDb := fmt.Sprintf("tt99%d", suffix%10000000)

	for _, seed := range []struct{ id, tmdb, imdb string }{
		{direct, directTMDB, ""},
		{providerOnly, "", ""},
		{orphan, "", orphanIMDb},
		{noIDs, "", ""},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, sort_title, tmdb_id, imdb_id, genres) VALUES ($1,'movie',$1,$1,$2,$3,'{}'::text[])`, seed.id, seed.tmdb, seed.imdb); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type) VALUES ($1,'tmdb',$2,'movie')`, providerOnly, providerTMDB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{direct, providerOnly, orphan, noIDs})
	})

	repo := NewItemRepository(pool)
	lookup, err := repo.GetByExternalIDs(ctx, ExternalIDBatch{TMDBIDs: []string{directTMDB, providerTMDB}}, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if lookup.ByTMDB[directTMDB] != direct || lookup.ByTMDB[providerTMDB] != providerOnly {
		t.Fatalf("ByTMDB = %v, want %s→%s and %s→%s", lookup.ByTMDB, directTMDB, direct, providerTMDB, providerOnly)
	}

	// A long, repeating batch is looked up in chunks; an ID past the first
	// chunk still resolves, and IDs of other providers resolve alongside.
	long := make([]string, 0, 2*externalIDLookupChunk+3)
	for i := range 2 * externalIDLookupChunk {
		long = append(long, fmt.Sprintf("missing-%d-%d", suffix, i))
	}
	long = append(long, providerTMDB, directTMDB, providerTMDB)
	lookup, err = repo.GetByExternalIDs(ctx, ExternalIDBatch{TMDBIDs: long, IMDbIDs: []string{orphanIMDb, orphanIMDb}}, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if lookup.ByTMDB[directTMDB] != direct || lookup.ByTMDB[providerTMDB] != providerOnly || lookup.ByIMDb[orphanIMDb] != orphan {
		t.Fatalf("chunked lookup = %v / %v, want %s→%s, %s→%s and %s→%s", lookup.ByTMDB, lookup.ByIMDb, directTMDB, direct, providerTMDB, providerOnly, orphanIMDb, orphan)
	}

	titles, err := repo.TitlesWithoutTMDBID(ctx, "movie", 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	var mine []string
	for _, title := range titles {
		switch title.ContentID {
		case direct, providerOnly, noIDs:
			mine = append(mine, title.ContentID)
		case orphan:
			if title.IMDbID != orphanIMDb {
				t.Fatalf("orphan listed with IMDb ID %q, want %q", title.IMDbID, orphanIMDb)
			}
			mine = append(mine, title.ContentID)
		}
	}
	if !slices.Equal(mine, []string{orphan}) {
		t.Fatalf("titles without a TMDB ID among the seeds = %v, want only %s", mine, orphan)
	}
}
