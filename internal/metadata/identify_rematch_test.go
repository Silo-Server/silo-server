package metadata

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// seedWrongMatch stores a movie the way a wrong title-only match leaves it:
// the wrong film's TMDB and IMDb IDs in both the item columns and the durable
// provider-ID rows.
func seedWrongMatch(t *testing.T, h *testHarness, contentID string) *fakeProviderIDRepo {
	t.Helper()
	return seedMovieIdentity(t, h, contentID, "100", "tt0000100")
}

// seedMovieIdentity stores a matched movie with the given TMDB and IMDb IDs
// (either may be empty) in the item columns and the durable provider-ID rows.
func seedMovieIdentity(t *testing.T, h *testHarness, contentID, tmdb, imdb string) *fakeProviderIDRepo {
	t.Helper()
	if err := h.itemRepo.Upsert(context.Background(), &models.MediaItem{
		ContentID: contentID, Type: "movie", Title: "Wrong Film", Year: 2006, Status: "matched",
		TmdbID: tmdb, ImdbID: imdb,
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	var rows []*models.MediaItemProviderID
	for provider, id := range map[string]string{"tmdb": tmdb, "imdb": imdb} {
		if id != "" {
			rows = append(rows, &models.MediaItemProviderID{ContentID: contentID, ItemType: "movie", Provider: provider, ProviderID: id})
		}
	}
	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(contentID, rows...)
	h.service.providerIDRepo = providerRepo
	return providerRepo
}

func assertIdentityIDs(t *testing.T, h *testHarness, providerRepo *fakeProviderIDRepo, contentID, wantTMDB, wantIMDb string) {
	t.Helper()
	item, err := h.itemRepo.GetByID(context.Background(), contentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.TmdbID != wantTMDB || item.ImdbID != wantIMDb {
		t.Errorf("stored tmdb_id=%q imdb_id=%q, want %q and %q", item.TmdbID, item.ImdbID, wantTMDB, wantIMDb)
	}
	providerRepo.mu.Lock()
	persisted := providerRepo.lastReplace[contentID]
	providerRepo.mu.Unlock()
	if persisted["tmdb"] != wantTMDB || persisted["imdb"] != wantIMDb {
		t.Errorf("persisted provider IDs = %#v, want tmdb=%q imdb=%q", persisted, wantTMDB, wantIMDb)
	}
}

// Identify is how an admin corrects a wrong match (#1629). The chosen TMDB ID
// replaces the stored one, and the wrong film's IMDb ID is dropped when the
// corrected film has none, so it can't pull the wrong film's files in later.
// The item is on a legacy id, which never re-anchors; moving a corrected
// provider-anchored item is covered by
// TestIdentify_CorrectedSeriesMovesWithItsChildren.
func TestProcess_IdentifyReplacesWrongMatchIDs(t *testing.T) {
	const contentID = "146000000000000100"
	h := newTestHarness()
	providerRepo := seedWrongMatch(t, h, contentID)
	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Right Film", Year: 2015,
		ProviderIDs: map[string]string{"tmdb": "200"},
	}}

	result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID:   contentID,
		ProviderIDs: map[string]string{"tmdb": "200"},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.ContentID != contentID {
		t.Fatalf("result = %#v, want content id %s preserved", result, contentID)
	}
	if got := provider.lastRequest().ProviderIDs["imdb"]; got != "" {
		t.Errorf("identify fetched with the wrong film's imdb id %q", got)
	}
	assertIdentityIDs(t, h, providerRepo, contentID, "200", "")
}

// Picking the same film by a provider ID the item lacks reads as a correction
// (the choice shares no key with the stored IDs), but the chosen film's
// provider returns the item's anchor again, so the item keeps its id.
func TestProcess_IdentifyAddingProviderIDKeepsAnchoredID(t *testing.T) {
	const contentID = "movie-imdb-tt0000100"
	h := newTestHarness()
	providerRepo := seedMovieIdentity(t, h, contentID, "", "tt0000100")
	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Wrong Film", Year: 2006,
		ProviderIDs: map[string]string{"tmdb": "100", "imdb": "tt0000100"},
	}}

	result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID:   contentID,
		ProviderIDs: map[string]string{"tmdb": "100"},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.ContentID != contentID {
		t.Fatalf("result = %#v, want content id %s preserved", result, contentID)
	}
	assertIdentityIDs(t, h, providerRepo, contentID, "100", "tt0000100")
}

// Re-applying the match an item already has confirms it rather than
// correcting it, so IDs the provider doesn't repeat are kept.
func TestProcess_IdentifyConfirmingMatchKeepsStoredIDs(t *testing.T) {
	const contentID = "movie-tmdb-100"
	h := newTestHarness()
	providerRepo := seedWrongMatch(t, h, contentID)
	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Wrong Film", Year: 2006,
		ProviderIDs: map[string]string{"tmdb": "100"},
	}}

	if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID:   contentID,
		ProviderIDs: map[string]string{"tmdb": "100"},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{provider}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	assertIdentityIDs(t, h, providerRepo, contentID, "100", "tt0000100")
}

// A match stored only in the durable provider-ID rows still counts: choosing
// the TMDB ID those rows hold confirms it and keeps the IMDb ID.
func TestProcess_IdentifyConfirmingDurableOnlyMatchKeepsStoredIDs(t *testing.T) {
	const contentID = "movie-durable-only"
	h := newTestHarness()
	providerRepo := seedMovieIdentity(t, h, contentID, "100", "tt0000100")
	h.itemRepo.items[contentID].TmdbID = ""
	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Wrong Film", Year: 2006,
		ProviderIDs: map[string]string{"tmdb": "100"},
	}}

	if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID:   contentID,
		ProviderIDs: map[string]string{"tmdb": "100"},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{provider}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	assertIdentityIDs(t, h, providerRepo, contentID, "100", "tt0000100")
}

// An item that only has the wrong film's IMDb ID, identified by a TMDB ID,
// drops that IMDb ID: the admin named the item's identity.
func TestProcess_IdentifyByTMDBDropsStoredIMDbOnlyMatch(t *testing.T) {
	const contentID = "movie:imdb:tt0000100"
	h := newTestHarness()
	providerRepo := seedMovieIdentity(t, h, contentID, "", "tt0000100")
	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Right Film", Year: 2015,
		ProviderIDs: map[string]string{"tmdb": "200"},
	}}

	if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID:   contentID,
		ProviderIDs: map[string]string{"tmdb": "200"},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{provider}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if got := provider.lastRequest().ProviderIDs["imdb"]; got != "" {
		t.Errorf("identify fetched with the stored imdb id %q", got)
	}
	assertIdentityIDs(t, h, providerRepo, contentID, "200", "")
}

// A corrected NFO <uniqueid> plus a manual refresh is the documented recovery
// path. The stored IDs from the wrong match must not survive it, or the next
// refresh without the NFO would restore the wrong film.
func TestManualRefresh_CorrectedNFOReplacesStoredIDs(t *testing.T) {
	const contentID = "movie:tmdb:100"
	h := newTestHarness()
	providerRepo := seedWrongMatch(t, h, contentID)
	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "200"},
		metadata: &MetadataResult{HasMetadata: true, Title: "Right Film"},
	}
	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Right Film", Year: 2015, ProviderIDs: map[string]string{"tmdb": "200"}},
	}

	result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID: contentID,
		Language:  "en",
		Mode:      ModeManualRefresh,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.ContentID != contentID {
		t.Fatalf("result = %#v, want content id %s", result, contentID)
	}
	if got := remote.lastMetadataIDs()["imdb"]; got != "" {
		t.Errorf("manual refresh fetched with the wrong film's imdb id %q", got)
	}
	assertIdentityIDs(t, h, providerRepo, contentID, "200", "")
}

// Restating the stored TMDB ID while adding a TVDB ID extends the match, so
// the stored IMDb ID is kept.
func TestProcess_IdentifyExtendingMatchKeepsStoredIDs(t *testing.T) {
	const contentID = "movie-tmdb-100"
	h := newTestHarness()
	providerRepo := seedWrongMatch(t, h, contentID)
	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Wrong Film", Year: 2006,
		ProviderIDs: map[string]string{"tmdb": "100", "tvdb": "555"},
	}}

	if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID:   contentID,
		ProviderIDs: map[string]string{"tmdb": "100", "tvdb": "555"},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{provider}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	assertIdentityIDs(t, h, providerRepo, contentID, "100", "tt0000100")
}

// runNFOCorrection runs a manual refresh whose NFO names TMDB 200 and whose
// provider returns only that ID.
func runNFOCorrection(t *testing.T, h *testHarness, contentID string) *remoteStubProvider {
	t.Helper()
	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "200"},
		metadata: &MetadataResult{HasMetadata: true, Title: "Right Film"},
	}
	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Right Film", Year: 2015, ProviderIDs: map[string]string{"tmdb": "200"}},
	}
	if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID: contentID,
		Language:  "en",
		Mode:      ModeManualRefresh,
	}, []Provider{nfo, remote}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	return remote
}

// A TMDB ID stored only in the durable rows still counts as the item's match
// when a corrected NFO names a different one.
func TestManualRefresh_CorrectedNFOReplacesDurableOnlyIDs(t *testing.T) {
	const contentID = "movie:durable:100"
	h := newTestHarness()
	providerRepo := seedMovieIdentity(t, h, contentID, "100", "tt0000100")
	h.itemRepo.items[contentID].TmdbID = ""
	h.itemRepo.items[contentID].ImdbID = ""

	remote := runNFOCorrection(t, h, contentID)
	if got := remote.lastMetadataIDs()["imdb"]; got != "" {
		t.Errorf("manual refresh fetched with the wrong film's imdb id %q", got)
	}
	assertIdentityIDs(t, h, providerRepo, contentID, "200", "")
}

// An item that only has the wrong film's IMDb ID, given a TMDB ID by a
// corrected NFO, drops that IMDb ID.
func TestManualRefresh_CorrectedNFOReplacesIMDbOnlyMatch(t *testing.T) {
	const contentID = "movie:imdb:tt0000100"
	h := newTestHarness()
	providerRepo := seedMovieIdentity(t, h, contentID, "", "tt0000100")

	remote := runNFOCorrection(t, h, contentID)
	if got := remote.lastMetadataIDs()["imdb"]; got != "" {
		t.Errorf("manual refresh fetched with the stored imdb id %q", got)
	}
	assertIdentityIDs(t, h, providerRepo, contentID, "200", "")
}

func TestProcess_IdentifyReplacesDivergentStoredIDs(t *testing.T) {
	for _, chosenIMDb := range []string{"", "tt0000200"} {
		t.Run("chosen IMDb="+chosenIMDb, func(t *testing.T) {
			const contentID = "146000000000000100"
			h := newTestHarness()
			providerRepo := seedWrongMatch(t, h, contentID)
			providerRepo.set(contentID,
				&models.MediaItemProviderID{ContentID: contentID, ItemType: "movie", Provider: "tmdb", ProviderID: "100"},
				&models.MediaItemProviderID{ContentID: contentID, ItemType: "movie", Provider: "imdb", ProviderID: "tt0000200"})
			chosen := map[string]string{"tmdb": "200"}
			if chosenIMDb != "" {
				chosen["imdb"] = chosenIMDb
			}
			provider := &capturingMetadataProvider{response: &MetadataResult{
				HasMetadata: true, Title: "Right Film", ProviderIDs: map[string]string{"tmdb": "200"},
			}}
			if _, err := h.service.ProcessWithProviders(t.Context(), ProcessRequest{
				ContentID: contentID, ProviderIDs: chosen, Language: "en", Mode: ModeIdentify,
			}, []Provider{provider}); err != nil {
				t.Fatal(err)
			}
			if got := provider.lastRequest().ProviderIDs["imdb"]; got != chosenIMDb {
				t.Errorf("requested IMDb = %q, want %q", got, chosenIMDb)
			}
			assertIdentityIDs(t, h, providerRepo, contentID, "200", chosenIMDb)
		})
	}
}

// A malformed legacy ID from the wrong match is rejected as it is stored, so
// the merge can't restore it after a correction.
func TestRejectIdentityProviderIDsIncludesMalformedStoredValues(t *testing.T) {
	rejected := make(providerIDValueSet)
	rejectIdentityProviderIDs(rejected, map[string]string{"tmdb": "200"},
		map[string]string{"TMDB": "100", "imdb": "nm0000100"})

	stored := map[string]string{"tmdb": "100", "imdb": "nm0000100"}
	suppressProviderIDValues(stored, rejected)
	if len(stored) != 0 {
		t.Fatalf("stored IDs after suppression = %#v, want the malformed IMDb value rejected too", stored)
	}
}
