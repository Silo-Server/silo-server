package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The scanner anchors a tagged folder's skeleton on its tag ids
// (movie-tmdb-<tag>). When the folder's NFO names a different title, the
// initial match must not persist the tag ids next to the NFO's title: they
// are dropped and the item moves to the NFO's anchor, so the tag's own title
// cannot later merge into it.
func TestInitialMatch_NFOIDsReplaceTagAnchoredSkeleton(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	items := catalog.NewItemRepository(pool)
	providerIDs := catalog.NewProviderIDRepository(pool)
	service := NewMetadataService(nil, nil, nil, items, providerIDs,
		catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool),
		catalog.NewLibraryItemRepository(pool), catalog.NewFolderRepository(pool),
		nil, nil, nil, nil, nil)
	nonce := time.Now().UnixNano() % 100_000_000
	tagTMDB, nfoTMDB := fmt.Sprintf("%d", 600_000_000+nonce), fmt.Sprintf("%d", 700_000_000+nonce)
	tagIMDb, nfoIMDb := fmt.Sprintf("tt8%08d", nonce), fmt.Sprintf("tt9%08d", nonce)
	from, to := "movie-tmdb-"+tagTMDB, "movie-tmdb-"+nfoTMDB
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{from, to})
	})
	if err := items.Upsert(ctx, &models.MediaItem{
		ContentID: from, Type: "movie", Title: "Fixture Tagged", Year: 1979, Status: "pending",
		TmdbID: tagTMDB, ImdbID: tagIMDb, DefaultMetadataLanguage: "en",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatal(err)
	}

	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": nfoTMDB},
		metadata: &MetadataResult{HasMetadata: true, Title: "The Thing"},
	}
	remote := &remoteStubProvider{
		slug: "tmdb",
		searchResults: []SearchResult{{
			Name: "The Thing", Year: 1982, Provider: "tmdb",
			ProviderIDs: map[string]string{"tmdb": nfoTMDB, "imdb": nfoIMDb},
		}},
		metadata: &MetadataResult{
			HasMetadata: true, Title: "The Thing", Year: 1982,
			ProviderIDs: map[string]string{"tmdb": nfoTMDB, "imdb": nfoIMDb},
		},
	}
	result, err := service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: from,
		Hints:     &MatchHints{Title: "Fixture Tagged", Year: 1979, Type: "movie", TmdbID: tagTMDB, ImdbID: tagIMDb},
		Language:  "en",
		Mode:      ModeInitialMatch,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Updated || result.ContentID != to {
		t.Fatalf("match result = %#v, want the item re-anchored to %s", result, to)
	}
	item, err := items.GetByID(ctx, to)
	if err != nil {
		t.Fatal(err)
	}
	if item.TmdbID != nfoTMDB || item.ImdbID != nfoIMDb || item.Title != "The Thing" {
		t.Errorf("item = %q tmdb:%q imdb:%q, want The Thing with %s / %s", item.Title, item.TmdbID, item.ImdbID, nfoTMDB, nfoIMDb)
	}
	rows, err := providerIDs.GetByContentID(ctx, to)
	if err != nil {
		t.Fatal(err)
	}
	if persisted := providerIDMapFromRows(rows); persisted["tmdb"] != nfoTMDB || persisted["imdb"] != nfoIMDb {
		t.Errorf("provider rows = %#v, want tmdb:%s imdb:%s and no tag ids", persisted, nfoTMDB, nfoIMDb)
	}
	if _, err := items.GetByID(ctx, from); err == nil {
		t.Errorf("%s still exists; the tag anchor must not keep holding the NFO's title", from)
	}
}

// With no remote provider to look the NFO's ids up, they stand as the item's
// identity: the tag ids they replaced are not persisted beside them, and the
// tag-anchored skeleton moves to the NFO's anchor.
func TestInitialMatch_NFOOnlyChainReplacesTagAnchoredSkeleton(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	items := catalog.NewItemRepository(pool)
	providerIDs := catalog.NewProviderIDRepository(pool)
	service := NewMetadataService(nil, nil, nil, items, providerIDs,
		catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool),
		catalog.NewLibraryItemRepository(pool), catalog.NewFolderRepository(pool),
		nil, nil, nil, nil, nil)
	nonce := time.Now().UnixNano() % 100_000_000
	tagTMDB, nfoTMDB := fmt.Sprintf("%d", 610_000_000+nonce), fmt.Sprintf("%d", 710_000_000+nonce)
	tagIMDb := fmt.Sprintf("tt7%08d", nonce)
	from, to := "movie-tmdb-"+tagTMDB, "movie-tmdb-"+nfoTMDB
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{from, to})
	})
	if err := items.Upsert(ctx, &models.MediaItem{
		ContentID: from, Type: "movie", Title: "Fixture Tagged", Year: 1979, Status: "pending",
		TmdbID: tagTMDB, ImdbID: tagIMDb, DefaultMetadataLanguage: "en",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatal(err)
	}

	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": nfoTMDB},
		metadata: &MetadataResult{HasMetadata: true, Title: "The Thing", Year: 1982},
	}
	result, err := service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: from,
		Hints:     &MatchHints{Title: "Fixture Tagged", Year: 1979, Type: "movie", TmdbID: tagTMDB, ImdbID: tagIMDb},
		Language:  "en",
		Mode:      ModeInitialMatch,
	}, []Provider{nfo})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Updated || result.ContentID != to {
		t.Fatalf("match result = %#v, want the item re-anchored to %s", result, to)
	}
	item, err := items.GetByID(ctx, to)
	if err != nil {
		t.Fatal(err)
	}
	if item.TmdbID != nfoTMDB || item.ImdbID == tagIMDb {
		t.Errorf("item tmdb:%q imdb:%q, want %s and not the tag's %s", item.TmdbID, item.ImdbID, nfoTMDB, tagIMDb)
	}
}
