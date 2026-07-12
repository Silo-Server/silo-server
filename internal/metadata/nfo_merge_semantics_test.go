package metadata

import (
	"context"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Phase-2 accumulation runs NFO (priority 1) first, then remote providers,
// always with MergeFillEmpty: the NFO wins every field it populates and the
// remote result backfills the rest.
func TestMerge_NFOFirstFillEmpty_NFOWinsRemoteBackfills(t *testing.T) {
	t.Parallel()

	nfoResult := &MetadataResult{
		HasMetadata: true,
		Title:       "Curated Title",
		Overview:    "Curated overview.",
		Genres:      []string{"Documentary"},
		Ratings:     Ratings{IMDB: 8.1},
	}
	remoteResult := &MetadataResult{
		HasMetadata:   true,
		Title:         "Remote Title",
		Overview:      "Remote overview.",
		Tagline:       "Remote tagline",
		Runtime:       117,
		Genres:        []string{"Action", "Thriller"},
		Studios:       []string{"Remote Studio"},
		Ratings:       Ratings{IMDB: 7.0, TMDB: 7.5},
		People:        []models.ItemPerson{{Person: models.Person{Name: "Remote Actor"}, Kind: models.PersonKindActor}},
		ContentRating: "R",
	}

	accumulator := &MetadataResult{}
	MergeMetadata(nfoResult, accumulator, nil, MergeFillEmpty)
	MergeMetadata(remoteResult, accumulator, nil, MergeFillEmpty)

	// NFO wins the fields it populated.
	if accumulator.Title != "Curated Title" {
		t.Errorf("Title = %q, want Curated Title", accumulator.Title)
	}
	if accumulator.Overview != "Curated overview." {
		t.Errorf("Overview = %q, want the NFO overview", accumulator.Overview)
	}
	if accumulator.Ratings.IMDB != 8.1 {
		t.Errorf("Ratings.IMDB = %v, want 8.1 (NFO wins)", accumulator.Ratings.IMDB)
	}
	// Genres are first-provider-wins as a whole list: no remote union.
	if want := []string{"Documentary"}; !reflect.DeepEqual(accumulator.Genres, want) {
		t.Errorf("Genres = %#v, want %#v (whole-list first-provider-wins)", accumulator.Genres, want)
	}
	// Remote backfills what the NFO left empty.
	if accumulator.Tagline != "Remote tagline" || accumulator.Runtime != 117 || accumulator.ContentRating != "R" {
		t.Errorf("backfill = tagline %q runtime %d rating %q", accumulator.Tagline, accumulator.Runtime, accumulator.ContentRating)
	}
	if accumulator.Ratings.TMDB != 7.5 {
		t.Errorf("Ratings.TMDB = %v, want 7.5 (remote backfills)", accumulator.Ratings.TMDB)
	}
	if len(accumulator.Studios) != 1 || len(accumulator.People) != 1 {
		t.Errorf("Studios = %#v People = %#v, want remote backfill", accumulator.Studios, accumulator.People)
	}
}

// An NFO that declares no <genre> must leave Genres untouched so the remote
// provider's list applies (the provider emits nil, never an empty slice).
func TestMerge_NFOWithoutGenres_RemoteGenresApply(t *testing.T) {
	t.Parallel()

	nfoResult := &MetadataResult{HasMetadata: true, Title: "Curated Title"}
	remoteResult := &MetadataResult{HasMetadata: true, Genres: []string{"Action", "Thriller"}}

	accumulator := &MetadataResult{}
	MergeMetadata(nfoResult, accumulator, nil, MergeFillEmpty)
	MergeMetadata(remoteResult, accumulator, nil, MergeFillEmpty)

	if want := []string{"Action", "Thriller"}; !reflect.DeepEqual(accumulator.Genres, want) {
		t.Errorf("Genres = %#v, want %#v", accumulator.Genres, want)
	}
}

// FieldReleaseDates locks Year and the release/air dates against refreshes.
func TestMerge_ReleaseDatesLockBlocksReplace(t *testing.T) {
	t.Parallel()

	source := &MetadataResult{
		HasMetadata:  true,
		Year:         1982,
		ReleaseDate:  "1982-06-25",
		FirstAirDate: "1982-06-25",
		LastAirDate:  "1983-01-01",
	}
	target := &MetadataResult{
		Year:         1990,
		ReleaseDate:  "1990-01-01",
		FirstAirDate: "1990-01-01",
		LastAirDate:  "1991-01-01",
	}

	MergeMetadata(source, target, []MetadataField{FieldReleaseDates}, MergeReplaceUnlocked)

	if target.Year != 1990 || target.ReleaseDate != "1990-01-01" ||
		target.FirstAirDate != "1990-01-01" || target.LastAirDate != "1991-01-01" {
		t.Errorf("locked dates overwritten: %+v", target)
	}

	// Unlocked, replace mode applies the source values.
	MergeMetadata(source, target, nil, MergeReplaceUnlocked)
	if target.Year != 1982 || target.ReleaseDate != "1982-06-25" {
		t.Errorf("unlocked replace did not apply: %+v", target)
	}
}

// Refresh contract, scheduled side: MergeFillEmpty seeds the accumulator from
// the existing item, so an edited NFO title does NOT propagate on the 6h cycle.
func TestScheduledRefresh_DoesNotPropagateNFOEdits(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	seedMovieItem(t, h, "movie:tmdb:100", "Original Title", 2018)
	h.itemRepo.items["movie:tmdb:100"].TmdbID = "100"
	h.itemRepo.items["movie:tmdb:100"].Overview = "Original overview."

	nfo := &localHintStubProvider{
		metadata: &MetadataResult{HasMetadata: true, Title: "Edited via NFO", Overview: "Edited overview."},
	}
	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Original Title", Year: 2018, ProviderIDs: map[string]string{"tmdb": "100"}},
	}

	_, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "movie:tmdb:100",
		Language:  "en",
		Mode:      ModeScheduledRefresh,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	item, err := h.itemRepo.GetByID(ctx, "movie:tmdb:100")
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.Title != "Original Title" {
		t.Errorf("scheduled refresh title = %q, want Original Title (NFO edits must not propagate)", item.Title)
	}
	if item.Overview != "Original overview." {
		t.Errorf("scheduled refresh overview = %q, want the original", item.Overview)
	}
}

// Refresh contract, manual side: MergeReplaceUnlocked propagates NFO edits
// ("edit the NFO, then Refresh metadata").
func TestManualRefresh_PropagatesNFOEdits(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	seedMovieItem(t, h, "movie:tmdb:100", "Original Title", 2018)
	h.itemRepo.items["movie:tmdb:100"].TmdbID = "100"
	h.itemRepo.items["movie:tmdb:100"].Overview = "Original overview."

	nfo := &localHintStubProvider{
		metadata: &MetadataResult{HasMetadata: true, Title: "Edited via NFO", Overview: "Edited overview."},
	}
	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Original Title", Year: 2018, ProviderIDs: map[string]string{"tmdb": "100"}},
	}

	_, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "movie:tmdb:100",
		Language:  "en",
		Mode:      ModeManualRefresh,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	item, err := h.itemRepo.GetByID(ctx, "movie:tmdb:100")
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.Title != "Edited via NFO" {
		t.Errorf("manual refresh title = %q, want Edited via NFO", item.Title)
	}
	if item.Overview != "Edited overview." {
		t.Errorf("manual refresh overview = %q, want the NFO edit", item.Overview)
	}
}
