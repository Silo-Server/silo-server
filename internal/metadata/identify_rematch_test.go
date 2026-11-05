package metadata

import (
	"context"
	"errors"
	"slices"
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
// (either may be empty) in the item columns and the durable provider-ID rows,
// and with home release dates the providers in these tests never send.
func seedMovieIdentity(t *testing.T, h *testHarness, contentID, tmdb, imdb string) *fakeProviderIDRepo {
	t.Helper()
	digital, physical := "2007-03-01", "2007-04-01"
	if err := h.itemRepo.Upsert(context.Background(), &models.MediaItem{
		ContentID: contentID, Type: "movie", Title: "Wrong Film", Year: 2006, Status: "matched",
		TmdbID: tmdb, ImdbID: imdb,
		DigitalReleaseDate: &digital, PhysicalReleaseDate: &physical,
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

// assertHomeReleaseDatesKept checks the seeded home release dates: a
// correction drops the previous match's dates when the new movie has none,
// and a confirming choice keeps them.
func assertHomeReleaseDatesKept(t *testing.T, h *testHarness, contentID string, want bool) {
	t.Helper()
	item, err := h.itemRepo.GetByID(context.Background(), contentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	kept := item.DigitalReleaseDate != nil && item.PhysicalReleaseDate != nil
	cleared := item.DigitalReleaseDate == nil && item.PhysicalReleaseDate == nil
	if (want && !kept) || (!want && !cleared) {
		t.Errorf("home release dates digital=%v physical=%v, want kept=%v", item.DigitalReleaseDate, item.PhysicalReleaseDate, want)
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
	assertHomeReleaseDatesKept(t, h, contentID, false)
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
	assertHomeReleaseDatesKept(t, h, contentID, true)
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
	assertHomeReleaseDatesKept(t, h, contentID, false)
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
	assertHomeReleaseDatesKept(t, h, contentID, false)
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

// runIdentify applies an Identify to tmdb, whose provider returns only a title
// and that ID.
func runIdentify(t *testing.T, h *testHarness, contentID, tmdb, language string) {
	t.Helper()
	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "A Film", ProviderIDs: map[string]string{"tmdb": tmdb},
	}}
	if _, err := h.service.ProcessWithProviders(t.Context(), ProcessRequest{
		ContentID: contentID, ProviderIDs: map[string]string{"tmdb": tmdb}, Language: language, Mode: ModeIdentify,
	}, []Provider{provider}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
}

// seedDescribedWrongMatch gives the wrong-match movie a value in every field a
// provider supplies, each of which the providers in these tests never send.
func seedDescribedWrongMatch(t *testing.T, h *testHarness, contentID string, locked ...MetadataField) {
	t.Helper()
	seedWrongMatch(t, h, contentID)
	item := h.itemRepo.items[contentID]
	imdbRating, tmdbRating, rtRating := 7.5, 7.1, 80
	advisoryAge, seasonCount := 16, 3
	date, airTime, airTimezone := "2006-01-01", "20:00", "UTC"
	item.SortTitle, item.OriginalTitle, item.Overview, item.Tagline = "Wrong Sort", "Wrong Original", "Wrong overview.", "Wrong tagline."
	item.Runtime, item.ContentRating = 90, "TV-MA"
	item.AdvisoryAge, item.AdvisorySource = &advisoryAge, AdvisorySourceCommonSense
	item.RatingIMDB, item.RatingTMDB, item.RatingRTCritic, item.RatingRTAudience = &imdbRating, &tmdbRating, &rtRating, &rtRating
	item.ReleaseDate, item.FirstAirDate, item.LastAirDate = &date, &date, &date
	item.OriginalLanguage, item.ShowStatus, item.SeasonCount = "fr", "ended", &seasonCount
	item.AirTime, item.AirTimezone = &airTime, &airTimezone
	item.Genres, item.Studios, item.Networks = []string{"Wrong Genre"}, []string{"Wrong Studio"}, []string{"Wrong Network"}
	item.Countries, item.Keywords = []string{"fr"}, []string{"wrong"}
	item.LockedFields = lockFields(locked...)
}

// describedFieldsKept names the seeded fields still holding a value.
func describedFieldsKept(item *models.MediaItem) []string {
	var kept []string
	for _, field := range []struct {
		name    string
		present bool
	}{
		{"sort title", item.SortTitle == "Wrong Sort"},
		{"original title", item.OriginalTitle != ""},
		{"overview", item.Overview != ""},
		{"tagline", item.Tagline != ""},
		{"runtime", item.Runtime != 0},
		{"content rating", item.ContentRating != ""},
		{"advisory", item.AdvisoryAge != nil || item.AdvisorySource != ""},
		{"ratings", item.RatingIMDB != nil || item.RatingTMDB != nil || item.RatingRTCritic != nil || item.RatingRTAudience != nil},
		{"release date", item.ReleaseDate != nil},
		{"air dates", item.FirstAirDate != nil || item.LastAirDate != nil},
		{"original language", item.OriginalLanguage != ""},
		{"show status", item.ShowStatus != ""},
		{"season count", item.SeasonCount != nil},
		{"air schedule", item.AirTime != nil || item.AirTimezone != nil},
		{"genres", len(item.Genres) > 0},
		{"studios", len(item.Studios) > 0 || len(item.Networks) > 0},
		{"tags", len(item.Countries) > 0 || len(item.Keywords) > 0},
	} {
		if field.present {
			kept = append(kept, field.name)
		}
	}
	return kept
}

// A correction names a different title, so a value the new match lacks must not
// stay behind from the wrong one (#3363). Locks still hold, and a refresh or a
// confirmed match can't tell a missing value from an unsent one, so they keep
// what is stored.
func TestCorrectionClearsWhatTheNewMatchLacks(t *testing.T) {
	const contentID = "146000000000000100"
	for _, tc := range []struct {
		name     string
		locked   []MetadataField
		run      func(*testing.T, *testHarness)
		keepsAll bool
		wantKept []string
	}{
		{name: "identify correction", run: func(t *testing.T, h *testHarness) {
			t.Helper()
			runIdentify(t, h, contentID, "200", "en")
		}},
		{name: "corrected NFO on a manual refresh", run: func(t *testing.T, h *testHarness) {
			t.Helper()
			runNFOCorrection(t, h, contentID)
		}},
		{
			name:   "locks hold through a correction",
			locked: []MetadataField{FieldOverview, FieldContentRating, FieldGenres},
			run: func(t *testing.T, h *testHarness) {
				t.Helper()
				runIdentify(t, h, contentID, "200", "en")
			},
			wantKept: []string{"overview", "tagline", "content rating", "genres"},
		},
		{name: "a confirmed match keeps stored values", keepsAll: true, run: func(t *testing.T, h *testHarness) {
			t.Helper()
			runIdentify(t, h, contentID, "100", "en")
		}},
		{name: "a manual refresh keeps stored values", keepsAll: true, run: func(t *testing.T, h *testHarness) {
			t.Helper()
			provider := &capturingMetadataProvider{response: &MetadataResult{
				HasMetadata: true, Title: "Wrong Film", ProviderIDs: map[string]string{"tmdb": "100"},
			}}
			if _, err := h.service.ProcessWithProviders(t.Context(), ProcessRequest{
				ContentID: contentID, Language: "en", Mode: ModeManualRefresh,
			}, []Provider{provider}); err != nil {
				t.Fatalf("ProcessWithProviders: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			seedDescribedWrongMatch(t, h, contentID, tc.locked...)
			want := tc.wantKept
			if tc.keepsAll {
				want = describedFieldsKept(h.itemRepo.items[contentID])
			}
			tc.run(t, h)
			item, err := h.itemRepo.GetByID(t.Context(), contentID)
			if err != nil {
				t.Fatalf("load item: %v", err)
			}
			if got := describedFieldsKept(item); !slices.Equal(got, want) {
				t.Errorf("fields kept = %v, want %v", got, want)
			}
		})
	}
}

// A correction to a title with no listed credits drops the wrong title's, except
// a group a lock holds. A confirmed match leaves them alone.
func TestCorrectionClearsCreditsTheNewMatchLacks(t *testing.T) {
	const contentID = "146000000000000100"
	cast, crew := actor("Wrong Actor", "Lead"), crewMember("Wrong Director", models.PersonKindDirector)
	stored := []models.ItemPerson{cast, crew}
	for _, tc := range []struct {
		name         string
		tmdb         string
		locked       []MetadataField
		stored       []models.ItemPerson
		wantReplaced bool
		wantPeople   []string
	}{
		{name: "no lock", tmdb: "200", stored: stored, wantReplaced: true},
		{name: "cast lock", tmdb: "200", locked: []MetadataField{FieldCast}, stored: stored, wantReplaced: true, wantPeople: []string{"Wrong Actor"}},
		{name: "crew lock", tmdb: "200", locked: []MetadataField{FieldCrew}, stored: stored, wantReplaced: true, wantPeople: []string{"Wrong Director"}},
		{name: "a lock on an empty group", tmdb: "200", locked: []MetadataField{FieldCrew}, stored: []models.ItemPerson{cast}, wantReplaced: true},
		{name: "both locked", tmdb: "200", locked: []MetadataField{FieldCast, FieldCrew}, stored: stored},
		{name: "confirmed match", tmdb: "100", stored: stored},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := newLocalPipelineHarness()
			h.service.personRepo = stubPersonRepo{ids: []int64{1, 2}}
			seedDescribedWrongMatch(t, h, contentID, tc.locked...)
			repo.people[contentID] = tc.stored
			runIdentify(t, h, contentID, tc.tmdb, "en")
			people, replaced := h.itemRepo.replacedPeople[contentID]
			if replaced != tc.wantReplaced {
				t.Fatalf("ReplacePeople called = %v, want %v", replaced, tc.wantReplaced)
			}
			if got := personNames(people); len(got)+len(tc.wantPeople) > 0 && !slices.Equal(got, tc.wantPeople) {
				t.Errorf("people = %v, want %v", got, tc.wantPeople)
			}
		})
	}
}

// Localization rows hold the wrong title's text too, and the serving layer
// prefers them over the base row. A correction empties them in every language,
// whichever one the write fetched, so each falls back or takes the new match's
// text. A lock keeps its field, and a confirmed match keeps the rows.
func TestCorrectionClearsLocalizedTextTheNewMatchLacks(t *testing.T) {
	const contentID = "146000000000000100"
	for _, tc := range []struct {
		name        string
		language    string
		tmdb        string
		locked      []MetadataField
		wantTitle   string
		wantTagline string
	}{
		{name: "another language's row", language: "en", tmdb: "200"},
		{name: "the fetched language's row", language: "fr", tmdb: "200", wantTitle: "A Film"},
		{name: "locks keep their fields", language: "en", tmdb: "200", locked: []MetadataField{FieldName, FieldOverview},
			wantTitle: "Mauvais Film", wantTagline: "Mauvaise accroche."},
		{name: "a confirmed match keeps the row", language: "en", tmdb: "100",
			wantTitle: "Mauvais Film", wantTagline: "Mauvaise accroche."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			seedDescribedWrongMatch(t, h, contentID, tc.locked...)
			h.itemRepo.items[contentID].DefaultMetadataLanguage = "en"
			locs := newFakeItemLocalizationRepo()
			h.service.itemLocalizationRepo = locs
			locs.rows[contentID+"/fr"] = &models.MediaItemLocalization{
				ContentID: contentID, Language: "fr", Title: "Mauvais Film", Overview: "Mauvais résumé.", Tagline: "Mauvaise accroche.",
			}
			runIdentify(t, h, contentID, tc.tmdb, tc.language)
			row := locs.rows[contentID+"/fr"]
			if row.Title != tc.wantTitle || row.Tagline != tc.wantTagline {
				t.Errorf("fr row title=%q tagline=%q, want %q and %q", row.Title, row.Tagline, tc.wantTitle, tc.wantTagline)
			}
		})
	}
}

// A corrected NFO names a different title just as Identify does, so a rating
// source only the previous match reported goes too.
func TestManualRefresh_CorrectedNFOReplacesRatingSources(t *testing.T) {
	const contentID = "146000000000000100"
	h := newTestHarness()
	seedWrongMatch(t, h, contentID)
	repo := &fakeRatingSourceRepo{}
	h.service.ratingSourceRepo = repo

	runNFOCorrection(t, h, contentID)

	if len(repo.upserts) != 1 || !repo.upserts[0].wholeSet || len(repo.upserts[0].sources) != 0 {
		t.Fatalf("writes = %+v, want one empty Replace", repo.upserts)
	}
}

// A refresh prefers stored text to text in a language nobody asked for, but
// after a correction the stored text belongs to another title, so the new
// match's original-language text stands when no language has better.
func TestCorrectedNFOKeepsTheNewMatchsOriginalLanguageText(t *testing.T) {
	const contentID = "146000000000000100"
	h := newTestHarness()
	seedDescribedWrongMatch(t, h, contentID)
	h.itemRepo.items[contentID].DefaultMetadataLanguage = "sv"
	h.service.SetFallbackLanguage("en")
	nfo := &localHintStubProvider{hints: map[string]string{"tmdb": "200"}, metadata: &MetadataResult{}}
	remote := &languageTextProvider{slug: "tmdb", types: []string{"movie"}, items: map[string]*MetadataResult{"sv": {
		Title: "正しい映画", TitleLanguage: "ja", TitleIsFallback: true,
		Overview: "正しいあらすじ", OverviewLanguage: "ja", ProviderIDs: map[string]string{"tmdb": "200"},
	}}}

	if _, err := h.service.ProcessWithProviders(t.Context(), ProcessRequest{
		ContentID: contentID, Language: "sv", Mode: ModeManualRefresh,
	}, []Provider{nfo, remote}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	item, err := h.itemRepo.GetByID(t.Context(), contentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.Title != "正しい映画" || item.Overview != "正しいあらすじ" {
		t.Errorf("item = %q / %q, want the new match's original-language text", item.Title, item.Overview)
	}
}

// A correction that can't clear what the new match lacks fails before it stores
// the new identity, so retrying is still a correction and clears it.
func TestCorrectionFailingToClearCanBeRetried(t *testing.T) {
	const contentID = "146000000000000100"
	cleared := errors.New("connection lost")
	for _, tc := range []struct {
		name string
		fail func(*testHarness, *fakeItemLocalizationRepo, error)
	}{
		{"localized text", func(_ *testHarness, locs *fakeItemLocalizationRepo, err error) { locs.clearErr = err }},
		{"credits", func(h *testHarness, _ *fakeItemLocalizationRepo, err error) { h.itemRepo.replacePeopleErr = err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			seedDescribedWrongMatch(t, h, contentID)
			locs := newFakeItemLocalizationRepo()
			h.service.itemLocalizationRepo = locs
			locs.rows[contentID+"/fr"] = &models.MediaItemLocalization{ContentID: contentID, Language: "fr", Tagline: "Mauvaise accroche."}
			provider := &capturingMetadataProvider{response: &MetadataResult{
				HasMetadata: true, Title: "A Film", ProviderIDs: map[string]string{"tmdb": "200"},
			}}
			request := ProcessRequest{ContentID: contentID, ProviderIDs: map[string]string{"tmdb": "200"}, Language: "en", Mode: ModeIdentify}

			tc.fail(h, locs, cleared)
			if _, err := h.service.ProcessWithProviders(t.Context(), request, []Provider{provider}); !errors.Is(err, cleared) {
				t.Fatalf("ProcessWithProviders = %v, want the clear's error", err)
			}
			if got := h.itemRepo.items[contentID].TmdbID; got != "100" {
				t.Fatalf("stored tmdb id = %q, want the old match until the clear succeeds", got)
			}

			tc.fail(h, locs, nil)
			if _, err := h.service.ProcessWithProviders(t.Context(), request, []Provider{provider}); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if got := locs.rows[contentID+"/fr"].Tagline; got != "" {
				t.Errorf("fr tagline after the retry = %q, want it cleared", got)
			}
			if _, replaced := h.itemRepo.replacedPeople[contentID]; !replaced {
				t.Error("retry did not clear the credits")
			}
		})
	}
}
