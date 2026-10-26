package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Corrected NFO matches preserve a surviving destination's own IDs while
// removing rejected source IDs after a merge or rename.
func TestManualRefresh_CorrectedNFOReanchorKeepsDestinationIDs(t *testing.T) {
	for _, tc := range []struct {
		name              string
		targetExists      bool
		targetHasIMDb     bool
		durableOnlyIMDb   bool
		sourceDurableOnly bool
		sourceIMDbDiffers bool
	}{
		{name: "existing destination", targetExists: true, targetHasIMDb: true},
		{name: "durable destination identity", targetExists: true, targetHasIMDb: true, durableOnlyIMDb: true, sourceDurableOnly: true},
		{name: "source identity moved to destination", targetExists: true},
		{name: "rename source"},
		{name: "conflicting source IDs after rename", sourceIMDbDiffers: true},
		{name: "conflicting source IDs after merge", targetExists: true, sourceIMDbDiffers: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := chainBuiltinTestPool(t)
			ctx := t.Context()
			items := catalog.NewItemRepository(pool)
			providerIDs := catalog.NewProviderIDRepository(pool)
			service := NewMetadataService(nil, nil, nil, items, providerIDs,
				catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool),
				catalog.NewLibraryItemRepository(pool), catalog.NewFolderRepository(pool),
				nil, nil, nil, nil, nil)
			nonce := time.Now().UnixNano() % 100_000_000
			wrongTMDB := fmt.Sprintf("%d", 600_000_000+nonce)
			rightTMDB := fmt.Sprintf("%d", 700_000_000+nonce)
			wrongIMDb := fmt.Sprintf("tt8%08d", nonce)
			rightIMDb := fmt.Sprintf("tt9%08d", nonce)
			from, to := "movie-tmdb-"+wrongTMDB, "movie-tmdb-"+rightTMDB
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{from, to})
			})
			seed := func(id, tmdb, imdb string, durableOnly bool) {
				t.Helper()
				item := &models.MediaItem{
					ContentID: id, Type: "movie", Title: "Rematch fixture", Status: "matched",
					TmdbID: tmdb, ImdbID: imdb, DefaultMetadataLanguage: "en",
					Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
				}
				if durableOnly {
					item.ImdbID = ""
				}
				if err := items.Upsert(ctx, item); err != nil {
					t.Fatal(err)
				}
				ids := map[string]string{"tmdb": tmdb}
				if imdb != "" {
					ids["imdb"] = imdb
				}
				if err := providerIDs.ReplaceByContentID(ctx, id, ids); err != nil {
					t.Fatal(err)
				}
			}
			seed(from, wrongTMDB, wrongIMDb, tc.sourceDurableOnly)
			if tc.sourceIMDbDiffers {
				if err := providerIDs.ReplaceByContentID(ctx, from, map[string]string{
					"tmdb": wrongTMDB, "imdb": fmt.Sprintf("tt6%08d", nonce),
				}); err != nil {
					t.Fatal(err)
				}
			}
			wantIMDb := ""
			if tc.targetHasIMDb {
				wantIMDb = rightIMDb
			}
			if tc.targetExists {
				seed(to, rightTMDB, wantIMDb, tc.durableOnlyIMDb)
			}
			nfo := &localHintStubProvider{
				hints:    map[string]string{"tmdb": rightTMDB},
				metadata: &MetadataResult{HasMetadata: true, Title: "Corrected fixture"},
			}
			remote := &remoteStubProvider{
				slug:     "tmdb",
				metadata: &MetadataResult{HasMetadata: true, Title: "Corrected fixture", ProviderIDs: map[string]string{"tmdb": rightTMDB}},
			}
			request := ProcessRequest{ContentID: from, Language: "en", Mode: ModeManualRefresh}
			result, err := service.ProcessWithProviders(ctx, request, []Provider{nfo, remote})
			if err != nil {
				t.Fatal(err)
			}
			if result == nil || !result.Updated || result.ContentID != to {
				t.Fatalf("refresh result = %#v, want destination %s", result, to)
			}
			item, err := items.GetByID(ctx, to)
			if err != nil {
				t.Fatal(err)
			}
			if item.TmdbID != rightTMDB || item.ImdbID != wantIMDb {
				t.Errorf("destination columns = tmdb:%q imdb:%q, want %q and %q", item.TmdbID, item.ImdbID, rightTMDB, wantIMDb)
			}
			rows, err := providerIDs.GetByContentID(ctx, to)
			if err != nil {
				t.Fatal(err)
			}
			persisted := providerIDMapFromRows(rows)
			if persisted["tmdb"] != rightTMDB || persisted["imdb"] != wantIMDb {
				t.Errorf("destination provider rows = %#v, want tmdb:%q imdb:%q", persisted, rightTMDB, wantIMDb)
			}
		})
	}
}
