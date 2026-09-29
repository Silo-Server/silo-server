package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A corrected NFO re-anchors a wrongly matched movie onto the item that
// already holds the right TMDB ID. The merge must keep that item's own IMDb ID
// even when the provider response names only TMDB.
func TestManualRefresh_CorrectedNFOReanchorKeepsDestinationIDs(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	items := catalog.NewItemRepository(pool)
	pids := catalog.NewProviderIDRepository(pool)
	svc := NewMetadataService(nil, nil, nil, items, pids,
		catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool),
		catalog.NewLibraryItemRepository(pool), catalog.NewFolderRepository(pool),
		catalog.NewPersonRepository(pool), nil, nil, nil, catalog.NewRootClaimRepository(pool))

	suffix := time.Now().UnixNano() % 100_000_000
	wrong, right := fmt.Sprintf("%d", 700_000_000+suffix), fmt.Sprintf("%d", 800_000_000+suffix)
	wrongIMDb, rightIMDb := fmt.Sprintf("tt7%08d", suffix), fmt.Sprintf("tt8%08d", suffix)
	from, to := "movie-tmdb-"+wrong, "movie-tmdb-"+right
	seed := func(contentID, title, tmdb, imdb string) {
		t.Helper()
		if err := items.Upsert(ctx, &models.MediaItem{
			ContentID: contentID, Type: "movie", Title: title, Year: 2015, Status: "matched",
			TmdbID: tmdb, ImdbID: imdb, DefaultMetadataLanguage: "en",
			Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
		}); err != nil {
			t.Fatalf("seed %s: %v", contentID, err)
		}
		if err := pids.ReplaceByContentID(ctx, contentID, map[string]string{"tmdb": tmdb, "imdb": imdb}); err != nil {
			t.Fatalf("seed provider ids for %s: %v", contentID, err)
		}
	}
	seed(from, "Wrong Film", wrong, wrongIMDb)
	seed(to, "Right Film", right, rightIMDb)

	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": right},
		metadata: &MetadataResult{HasMetadata: true, Title: "Right Film"},
	}
	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Right Film", Year: 2015, ProviderIDs: map[string]string{"tmdb": right}},
	}
	result, err := svc.ProcessWithProviders(ctx, ProcessRequest{ContentID: from, Language: "en", Mode: ModeManualRefresh},
		[]Provider{nfo, remote})
	if err != nil {
		t.Fatalf("manual refresh: %v", err)
	}
	if result == nil || result.ContentID != to {
		t.Fatalf("result = %#v, want re-anchored onto %s", result, to)
	}

	item, err := items.GetByID(ctx, to)
	if err != nil {
		t.Fatalf("load %s: %v", to, err)
	}
	if item.TmdbID != right || item.ImdbID != rightIMDb {
		t.Errorf("%s tmdb_id=%q imdb_id=%q, want %s and its own %s", to, item.TmdbID, item.ImdbID, right, rightIMDb)
	}
	rows, err := pids.GetByContentID(ctx, to)
	if err != nil {
		t.Fatalf("load provider ids for %s: %v", to, err)
	}
	stored := map[string]string{}
	for _, row := range rows {
		stored[row.Provider] = row.ProviderID
	}
	if stored["tmdb"] != right || stored["imdb"] != rightIMDb {
		t.Errorf("%s provider ids = %#v, want tmdb=%s imdb=%s", to, stored, right, rightIMDb)
	}
}
