package metadata

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// localPipelineItemRepo adds the stored-credit and Identify-choice reads that
// the real item repository has to the in-memory fake. Unlike the fake, the
// real GetByID never loads credits, so tests that care seed them here.
type localPipelineItemRepo struct {
	*fakeItemRepo
	mu     sync.Mutex
	people map[string][]models.ItemPerson
	chosen map[string]bool
}

func newLocalPipelineHarness() (*testHarness, *localPipelineItemRepo) {
	h := newTestHarness()
	repo := &localPipelineItemRepo{fakeItemRepo: h.itemRepo, people: map[string][]models.ItemPerson{}, chosen: map[string]bool{}}
	h.service.itemRepo = repo
	return h, repo
}

func (r *localPipelineItemRepo) GetPeople(_ context.Context, contentID string) ([]models.ItemPerson, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]models.ItemPerson(nil), r.people[contentID]...), nil
}

func (r *localPipelineItemRepo) MarkIdentityChosen(_ context.Context, contentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chosen[contentID] = true
	return nil
}

func (r *localPipelineItemRepo) IdentityChosen(_ context.Context, contentID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.chosen[contentID], nil
}

func lockFields(fields ...MetadataField) []int {
	out := make([]int, len(fields))
	for i, f := range fields {
		out[i] = int(f)
	}
	return out
}

// A manual refresh replaces the stored lists with the ones the NFO supplies
// instead of mixing in the remote provider's.
func TestManualRefresh_NFOListsReplaceRemoteLists(t *testing.T) {
	h, _ := newLocalPipelineHarness()
	ctx := context.Background()
	seedMovieItem(t, h, "movie:tmdb:27205", "Inception", 2010)
	stored := h.itemRepo.items["movie:tmdb:27205"]
	stored.TmdbID = "27205"
	stored.Genres = []string{"Action"}
	stored.Studios = []string{"Warner Bros."}

	nfo := &localHintStubProvider{
		hints: map[string]string{"tmdb": "27205"},
		metadata: &MetadataResult{
			HasMetadata: true,
			Title:       "Inception (Local)",
			Genres:      []string{"Local Genre"},
			Studios:     []string{"Local Studio"},
			People:      []models.ItemPerson{actor("Leonardo DiCaprio", "Local Role")},
		},
	}
	remote := &remoteStubProvider{
		slug: "tmdb",
		metadata: &MetadataResult{
			HasMetadata: true, Title: "Inception", Year: 2010, ProviderIDs: map[string]string{"tmdb": "27205"},
			Genres:  []string{"Action", "Science Fiction"},
			Studios: []string{"Warner Bros.", "Legendary"},
			People:  []models.ItemPerson{actor("Leonardo DiCaprio", "Cobb"), actor("Elliot Page", "Ariadne")},
		},
	}
	if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{ContentID: "movie:tmdb:27205", Language: "en", Mode: ModeManualRefresh}, []Provider{nfo, remote}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	item, err := h.itemRepo.GetByID(ctx, "movie:tmdb:27205")
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.Title != "Inception (Local)" {
		t.Errorf("Title = %q, want the NFO title", item.Title)
	}
	if !reflect.DeepEqual(item.Genres, []string{"Local Genre"}) || !reflect.DeepEqual(item.Studios, []string{"Local Studio"}) {
		t.Errorf("genres=%v studios=%v, want only the NFO lists", item.Genres, item.Studios)
	}
	if got := personNames(item.People); !reflect.DeepEqual(got, []string{"Leonardo DiCaprio"}) {
		t.Errorf("people = %v, want only the NFO cast", got)
	}
}

// Field locks outrank the NFO in every mode that can write an existing item.
func TestLocksOutrankNFOInEveryMode(t *testing.T) {
	cases := []struct {
		name   string
		mode   RefreshMode
		status string
	}{
		{"initial match of a matched item", ModeInitialMatch, "matched"},
		{"initial match of a skeleton item", ModeInitialMatch, "unmatched"},
		{"scheduled refresh", ModeScheduledRefresh, "matched"},
		{"manual refresh", ModeManualRefresh, "matched"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newLocalPipelineHarness()
			ctx := context.Background()
			seedMovieItem(t, h, "movie:tmdb:100", "Admin Title", 2018)
			stored := h.itemRepo.items["movie:tmdb:100"]
			stored.TmdbID = "100"
			stored.Status = tc.status
			stored.Overview = "Admin overview."
			// An admin cleared the genres and locked them; fill-empty modes
			// must not refill a locked empty field either.
			stored.Genres = []string{}
			stored.LockedFields = lockFields(FieldName, FieldOverview, FieldGenres, FieldReleaseDates)

			nfo := &localHintStubProvider{
				hints: map[string]string{"tmdb": "100"},
				metadata: &MetadataResult{
					HasMetadata: true, Title: "NFO Title", Overview: "NFO overview.", Year: 1999,
					Genres: []string{"NFO Genre"}, Studios: []string{"NFO Studio"},
				},
			}
			remote := &remoteStubProvider{
				slug:          "tmdb",
				searchResults: []SearchResult{{Name: "Admin Title", Year: 2018, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "100"}}},
				metadata:      &MetadataResult{HasMetadata: true, Title: "Remote Title", Year: 2018, ProviderIDs: map[string]string{"tmdb": "100"}},
			}
			req := ProcessRequest{ContentID: "movie:tmdb:100", Language: "en", Mode: tc.mode}
			if tc.mode == ModeInitialMatch {
				req.Hints = &MatchHints{Title: "Admin Title", Year: 2018, Type: "movie", TmdbID: "100"}
			}
			if _, err := h.service.ProcessWithProviders(ctx, req, []Provider{nfo, remote}); err != nil {
				t.Fatalf("ProcessWithProviders: %v", err)
			}
			item, err := h.itemRepo.GetByID(ctx, "movie:tmdb:100")
			if err != nil {
				t.Fatalf("load item: %v", err)
			}
			if item.Title != "Admin Title" || item.Overview != "Admin overview." || item.Year != 2018 {
				t.Errorf("locked fields = title %q overview %q year %d, want the admin's values", item.Title, item.Overview, item.Year)
			}
			if len(item.Genres) != 0 {
				t.Errorf("Genres = %v, want the locked empty list kept", item.Genres)
			}
			if !reflect.DeepEqual(item.Studios, []string{"NFO Studio"}) {
				t.Errorf("Studios = %v, want the unlocked field filled from the NFO", item.Studios)
			}
		})
	}
}

// A Cast-only lock keeps the stored cast through a refresh while the crew
// still refreshes, and a Crew-only lock the reverse. The stored item row has
// no credits, so the merge must read them.
func TestRefresh_PartialPeopleLockKeepsTheLockedGroup(t *testing.T) {
	storedPeople := []models.ItemPerson{actor("Keanu Reeves", "Neo"), crewMember("Lana Wachowski", models.PersonKindDirector)}
	cases := []struct {
		name string
		lock MetadataField
		mode RefreshMode
		want []string
	}{
		{"cast lock, manual", FieldCast, ModeManualRefresh, []string{"Keanu Reeves", "NFO Director"}},
		{"cast lock, scheduled", FieldCast, ModeScheduledRefresh, []string{"Keanu Reeves", "Lana Wachowski", "NFO Director"}},
		{"crew lock, manual", FieldCrew, ModeManualRefresh, []string{"Lock Test Actor", "Lana Wachowski"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := newLocalPipelineHarness()
			ctx := context.Background()
			seedMovieItem(t, h, "movie:tmdb:603", "The Matrix", 1999)
			stored := h.itemRepo.items["movie:tmdb:603"]
			stored.TmdbID = "603"
			stored.LockedFields = lockFields(tc.lock)
			repo.people["movie:tmdb:603"] = storedPeople

			nfo := &localHintStubProvider{
				hints: map[string]string{"tmdb": "603"},
				metadata: &MetadataResult{HasMetadata: true, People: []models.ItemPerson{
					actor("Lock Test Actor", "Intruder"), crewMember("NFO Director", models.PersonKindDirector),
				}},
			}
			remote := &remoteStubProvider{slug: "tmdb", metadata: &MetadataResult{HasMetadata: true, Title: "The Matrix", ProviderIDs: map[string]string{"tmdb": "603"}}}
			if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{ContentID: "movie:tmdb:603", Language: "en", Mode: tc.mode}, []Provider{nfo, remote}); err != nil {
				t.Fatalf("ProcessWithProviders: %v", err)
			}
			item, err := h.itemRepo.GetByID(ctx, "movie:tmdb:603")
			if err != nil {
				t.Fatalf("load item: %v", err)
			}
			if got := personNames(item.People); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("people = %v, want %v", got, tc.want)
			}
		})
	}
}

// After an admin Identify, a manual refresh keeps the chosen identity over a
// conflicting NFO <uniqueid> and leaves the NFO's text out; without an
// Identify the NFO still re-matches (the recovery path for a fixed NFO).
func TestManualRefresh_IdentifyChoiceOutranksConflictingNFO(t *testing.T) {
	h, repo := newLocalPipelineHarness()
	ctx := context.Background()
	seedMovieItem(t, h, "movie:tmdb:949", "Heat", 1995)
	h.itemRepo.items["movie:tmdb:949"].TmdbID = "949"

	nfo := &localHintStubProvider{
		hints:         map[string]string{"tmdb": "949"},
		searchResults: []SearchResult{{Name: "Heat", Year: 1995, Provider: "nfo", ProviderIDs: map[string]string{"tmdb": "949"}}},
		metadata:      &MetadataResult{HasMetadata: true, Title: "Heat (Local NFO Title)"},
	}
	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Collateral", Year: 2004, ProviderIDs: map[string]string{"tmdb": "1538"}},
	}

	identified, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "movie:tmdb:949", ProviderIDs: map[string]string{"tmdb": "1538"}, Language: "en", Mode: ModeIdentify,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	contentID := identified.ContentID
	if !repo.chosen[contentID] {
		t.Fatalf("Identify did not record the admin's choice on %q (chosen=%v)", contentID, repo.chosen)
	}

	nfo.mu.Lock()
	nfo.searchCalls, nfo.metadataCalls = 0, 0
	nfo.mu.Unlock()
	refreshed, err := h.service.ProcessWithProviders(ctx, ProcessRequest{ContentID: contentID, Language: "en", Mode: ModeManualRefresh}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("manual refresh: %v", err)
	}
	if got := remote.lastMetadataIDs()["tmdb"]; got != "1538" {
		t.Errorf("refresh fetched tmdb %q, want 1538 (the admin's Identify choice)", got)
	}
	item, err := h.itemRepo.GetByID(ctx, refreshed.ContentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.TmdbID != "1538" || item.Title != "Collateral" {
		t.Errorf("item = tmdb %q title %q, want Collateral (1538)", item.TmdbID, item.Title)
	}
	if nfo.searchCalls != 0 || nfo.metadataCalls != 0 {
		t.Errorf("conflicting NFO consulted: search=%d metadata=%d, want 0", nfo.searchCalls, nfo.metadataCalls)
	}
}

// A scheduled refresh of an identified item must not let a conflicting NFO
// add an identity key the item lacks either.
func TestScheduledRefresh_IdentifyChoiceBlocksConflictingNFOIDs(t *testing.T) {
	h, repo := newLocalPipelineHarness()
	ctx := context.Background()
	seedMovieItem(t, h, "movie:tmdb:1538", "Collateral", 2004)
	h.itemRepo.items["movie:tmdb:1538"].TmdbID = "1538"
	repo.chosen["movie:tmdb:1538"] = true

	nfo := &localHintStubProvider{hints: map[string]string{"tmdb": "949", "imdb": "tt0113277"}}
	remote := &remoteStubProvider{slug: "tmdb", metadata: &MetadataResult{HasMetadata: true, Title: "Collateral", ProviderIDs: map[string]string{"tmdb": "1538"}}}
	if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{ContentID: "movie:tmdb:1538", Language: "en", Mode: ModeScheduledRefresh}, []Provider{nfo, remote}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if ids := remote.lastMetadataIDs(); ids["tmdb"] != "1538" || ids["imdb"] != "" {
		t.Errorf("refresh fetched %v, want tmdb 1538 and no NFO imdb", ids)
	}
}

// An NFO that agrees with the chosen identity is not a conflict: its text
// applies as usual.
func TestManualRefresh_IdentifiedItemStillReadsAgreeingNFO(t *testing.T) {
	h, repo := newLocalPipelineHarness()
	ctx := context.Background()
	seedMovieItem(t, h, "movie:tmdb:1538", "Collateral", 2004)
	h.itemRepo.items["movie:tmdb:1538"].TmdbID = "1538"
	repo.chosen["movie:tmdb:1538"] = true

	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "1538"},
		metadata: &MetadataResult{HasMetadata: true, Title: "Collateral (Director's Cut)"},
	}
	remote := &remoteStubProvider{slug: "tmdb", metadata: &MetadataResult{HasMetadata: true, Title: "Collateral", ProviderIDs: map[string]string{"tmdb": "1538"}}}
	if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{ContentID: "movie:tmdb:1538", Language: "en", Mode: ModeManualRefresh}, []Provider{nfo, remote}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	item, err := h.itemRepo.GetByID(ctx, "movie:tmdb:1538")
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.Title != "Collateral (Director's Cut)" {
		t.Errorf("Title = %q, want the agreeing NFO's title", item.Title)
	}
}

// ChooseIdentity marks a seeded item as matched to ids by an admin's
// Identify, for the external NFO integration tests.
func (x *NFOSeriesHarness) ChooseIdentity(contentID string, ids map[string]string) {
	repo, ok := x.h.service.itemRepo.(*localPipelineItemRepo)
	if !ok {
		repo = &localPipelineItemRepo{fakeItemRepo: x.h.itemRepo, people: map[string][]models.ItemPerson{}, chosen: map[string]bool{}}
		x.h.service.itemRepo = repo
	}
	repo.chosen[contentID] = true
	item := x.h.itemRepo.items[contentID]
	item.Status = "matched"
	item.TmdbID, item.TvdbID, item.ImdbID = ids["tmdb"], ids["tvdb"], ids["imdb"]
}

// A scheduled refresh keeps the stored match and leaves out an NFO that names
// another title, rather than filling the item with that title's text.
func TestScheduledRefresh_SkipsNFOForAnotherTitle(t *testing.T) {
	h, _ := newLocalPipelineHarness()
	ctx := context.Background()
	seedMovieItem(t, h, "movie:tmdb:100", "Stable Movie", 2018)
	h.itemRepo.items["movie:tmdb:100"].TmdbID = "100"

	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "999"},
		metadata: &MetadataResult{HasMetadata: true, Title: "Other Movie", Overview: "Other overview.", Studios: []string{"Other Studio"}},
	}
	remote := &remoteStubProvider{slug: "tmdb", metadata: &MetadataResult{HasMetadata: true, Title: "Stable Movie", ProviderIDs: map[string]string{"tmdb": "100"}}}
	if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{ContentID: "movie:tmdb:100", Language: "en", Mode: ModeScheduledRefresh}, []Provider{nfo, remote}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	item, err := h.itemRepo.GetByID(ctx, "movie:tmdb:100")
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.TmdbID != "100" || item.Overview != "" || len(item.Studios) != 0 {
		t.Errorf("item = tmdb %q overview %q studios %v, want the stored match without the other title's NFO", item.TmdbID, item.Overview, item.Studios)
	}
	if nfo.metadataCalls != 0 {
		t.Errorf("NFO metadata read %d times, want 0", nfo.metadataCalls)
	}
}

// A fill-empty write lets an NFO list fill an empty stored list but does not
// add its entries to a stored one; a manual refresh replaces it.
func TestScheduledRefresh_NFOListsFillButDoNotJoinStoredLists(t *testing.T) {
	h, _ := newLocalPipelineHarness()
	ctx := context.Background()
	seedMovieItem(t, h, "movie:tmdb:100", "Stable Movie", 2018)
	stored := h.itemRepo.items["movie:tmdb:100"]
	stored.TmdbID = "100"
	stored.Studios = []string{"Warner Bros."}

	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "100"},
		metadata: &MetadataResult{HasMetadata: true, Studios: []string{"Local Studio"}, Countries: []string{"GB"}},
	}
	remote := &remoteStubProvider{slug: "tmdb", metadata: &MetadataResult{HasMetadata: true, Title: "Stable Movie", ProviderIDs: map[string]string{"tmdb": "100"}}}
	run := func(mode RefreshMode) *models.MediaItem {
		t.Helper()
		if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{ContentID: "movie:tmdb:100", Language: "en", Mode: mode}, []Provider{nfo, remote}); err != nil {
			t.Fatalf("ProcessWithProviders: %v", err)
		}
		item, err := h.itemRepo.GetByID(ctx, "movie:tmdb:100")
		if err != nil {
			t.Fatalf("load item: %v", err)
		}
		return item
	}

	item := run(ModeScheduledRefresh)
	if !reflect.DeepEqual(item.Studios, []string{"Warner Bros."}) || !reflect.DeepEqual(item.Countries, []string{"GB"}) {
		t.Errorf("after scheduled refresh studios=%v countries=%v, want the stored studios kept and the empty countries filled", item.Studios, item.Countries)
	}
	item = run(ModeManualRefresh)
	if !reflect.DeepEqual(item.Studios, []string{"Local Studio"}) {
		t.Errorf("after manual refresh studios=%v, want the NFO list", item.Studios)
	}
}
