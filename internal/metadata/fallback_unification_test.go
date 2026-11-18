package metadata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakePersonRefreshRepo struct {
	persons           map[int64]models.Person
	refreshAttempts   []int64
	refreshAttemptErr error
	updateErr         error
	// storedIdentity, when set, is the identity UpdateRefreshed reports
	// storing, as resolving an id conflict would.
	storedIdentity *catalog.PersonIdentity
	outcomes       []catalog.PersonRefreshOutcome
	identities     []catalog.PersonIdentity
}

func newFakePersonRefreshRepo(persons ...models.Person) *fakePersonRefreshRepo {
	repo := &fakePersonRefreshRepo{persons: make(map[int64]models.Person, len(persons))}
	for _, person := range persons {
		repo.persons[person.ID] = person
	}
	return repo
}

func (r *fakePersonRefreshRepo) Get(_ context.Context, id int64) (*models.Person, error) {
	person, ok := r.persons[id]
	if !ok {
		return nil, ErrPersonNotFound
	}
	cp := person
	return &cp, nil
}

func (r *fakePersonRefreshRepo) UpdateRefreshed(_ context.Context, person models.Person, lookedUp catalog.PersonIdentity) (catalog.PersonIdentity, error) {
	if r.updateErr != nil {
		return catalog.PersonIdentity{}, r.updateErr
	}
	if catalog.PersonIdentityOf(r.persons[person.ID]) != lookedUp {
		return catalog.PersonIdentity{}, catalog.ErrPersonIdentityChanged
	}
	r.persons[person.ID] = person
	if r.storedIdentity != nil {
		return *r.storedIdentity, nil
	}
	return catalog.PersonIdentityOf(person), nil
}

func (r *fakePersonRefreshRepo) ClaimRefreshCandidates(_ context.Context, _ int) ([]int64, error) {
	return nil, nil
}

func (r *fakePersonRefreshRepo) MarkRefreshAttempt(_ context.Context, id int64) error {
	r.refreshAttempts = append(r.refreshAttempts, id)
	return r.refreshAttemptErr
}

// StartRefreshAttemptUnlessStartedSince skips when the stored person's
// attempt is later than since.
func (r *fakePersonRefreshRepo) StartRefreshAttemptUnlessStartedSince(_ context.Context, id int64, since time.Time) (bool, error) {
	if attempted := r.persons[id].MetadataRefreshAttemptedAt; attempted != nil && attempted.After(since) {
		return false, nil
	}
	if r.refreshAttemptErr != nil {
		return false, r.refreshAttemptErr
	}
	r.refreshAttempts = append(r.refreshAttempts, id)
	return true, nil
}

func (r *fakePersonRefreshRepo) RecordRefreshOutcome(ctx context.Context, _ int64, identity catalog.PersonIdentity, outcome catalog.PersonRefreshOutcome) error {
	// The service must record with a context that outlives the lookup's.
	if err := ctx.Err(); err != nil {
		return err
	}
	r.outcomes = append(r.outcomes, outcome)
	r.identities = append(r.identities, identity)
	return nil
}

type stubPersonProvider struct {
	slug   string
	detail *PersonDetailResult
}

func (p stubPersonProvider) Slug() string       { return p.slug }
func (p stubPersonProvider) Name() string       { return p.slug }
func (p stubPersonProvider) ForTypes() []string { return []string{"person"} }

func (p stubPersonProvider) GetPersonDetail(context.Context, PersonDetailRequest) (*PersonDetailResult, error) {
	return p.detail, nil
}

func newSeasonEpisodeServiceForTest(seriesID string) (*MetadataService, *fakeItemRepo, *fakeSeasonRepo, *fakeEpisodeRepo) {
	itemRepo := newFakeItemRepo()
	itemRepo.items[seriesID] = &models.MediaItem{
		ContentID: seriesID,
		Type:      "series",
		Title:     "Test Series",
	}

	seasonRepo := newFakeSeasonRepo()
	episodeRepo := newFakeEpisodeRepo()

	service := &MetadataService{
		itemRepo:    itemRepo,
		seasonRepo:  seasonRepo,
		episodeRepo: episodeRepo,
	}
	return service, itemRepo, seasonRepo, episodeRepo
}

func TestPersistSeasonsAndEpisodes_ImageLockPreservesSelectedSeasonPoster(t *testing.T) {
	const seriesID = "series-locked-season-artwork"

	service, _, seasonRepo, _ := newSeasonEpisodeServiceForTest(seriesID)
	ctx := context.Background()
	seasonRepo.seasons[seasonKey(seriesID, 1)] = &models.Season{
		ContentID:        "season-1",
		SeriesID:         seriesID,
		SeasonNumber:     1,
		Title:            "Old Season 1",
		PosterPath:       "artwork/series/manual-season-1/revision/original.jpg",
		PosterSourcePath: "tmdb://manual-season-1.jpg",
		PosterThumbhash:  "manual-thumb",
	}
	seasonRepo.seasons[seasonKey(seriesID, 2)] = &models.Season{
		ContentID:    "season-2",
		SeriesID:     seriesID,
		SeasonNumber: 2,
		Title:        "Old Season 2",
	}

	service.persistSeasonsAndEpisodes(
		ctx,
		&models.MediaItem{ContentID: seriesID, Type: "series", LockedFields: []int{int(FieldImages)}},
		map[string]string{"tmdb": "123"},
		"en",
		"en",
		[]SeasonResult{
			{SeasonNumber: 1, Title: "New Season 1", PosterPath: "tmdb://automatic-season-1.jpg"},
			{SeasonNumber: 2, Title: "New Season 2", PosterPath: "tmdb://automatic-season-2.jpg"},
		},
		nil,
		MergeReplaceUnlocked,
	)

	selected := seasonRepo.seasons[seasonKey(seriesID, 1)]
	if selected.PosterPath != "artwork/series/manual-season-1/revision/original.jpg" ||
		selected.PosterSourcePath != "tmdb://manual-season-1.jpg" || selected.PosterThumbhash != "manual-thumb" {
		t.Fatalf("selected poster was overwritten: %#v", selected)
	}
	if selected.Title != "New Season 1" {
		t.Fatalf("season title = %q, want metadata refresh to update non-image fields", selected.Title)
	}

	missing := seasonRepo.seasons[seasonKey(seriesID, 2)]
	if missing.PosterPath != "tmdb://automatic-season-2.jpg" || missing.PosterSourcePath != "tmdb://automatic-season-2.jpg" {
		t.Fatalf("missing poster was not backfilled while images were locked: %#v", missing)
	}
}

func TestPersistSeasonsAndEpisodes_LocalizationBatchFailuresUsePointFallbacks(t *testing.T) {
	const seriesID = "series-localization-bulk-fallback"

	service, _, _, _ := newSeasonEpisodeServiceForTest(seriesID)
	seasonLocalizations := newFakeSeasonLocalizationRepo()
	episodeLocalizations := newFakeEpisodeLocalizationRepo()
	seasonLocalizations.bulkGetErr = errors.New("season localization batch read unavailable")
	seasonLocalizations.bulkUpsertErr = errors.New("season localization batch write unavailable")
	episodeLocalizations.bulkGetErr = errors.New("episode localization batch read unavailable")
	episodeLocalizations.bulkUpsertErr = errors.New("episode localization batch write unavailable")
	service.seasonLocalizationRepo = seasonLocalizations
	service.episodeLocalizationRepo = episodeLocalizations

	service.persistSeasonsAndEpisodes(
		context.Background(),
		&models.MediaItem{ContentID: seriesID, Type: "series"},
		nil,
		"en",
		"fr",
		[]SeasonResult{
			{SeasonNumber: 1, Title: "Saison 1"},
			{SeasonNumber: 2, Title: "Saison 2"},
		},
		[]EpisodeResult{
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Episode 1"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Episode 2"},
		},
		MergeFillEmpty,
	)

	if seasonLocalizations.getCalls != 2 || seasonLocalizations.upsertCalls != 2 {
		t.Fatalf("season localization fallback calls = get:%d upsert:%d, want 2/2",
			seasonLocalizations.getCalls, seasonLocalizations.upsertCalls)
	}
	if episodeLocalizations.getCalls != 2 || episodeLocalizations.upsertCalls != 2 {
		t.Fatalf("episode localization fallback calls = get:%d upsert:%d, want 2/2",
			episodeLocalizations.getCalls, episodeLocalizations.upsertCalls)
	}
	if got := len(seasonLocalizations.localizations); got != 2 {
		t.Fatalf("season localization fallback persisted %d rows, want 2", got)
	}
	if got := len(episodeLocalizations.localizations); got != 2 {
		t.Fatalf("episode localization fallback persisted %d rows, want 2", got)
	}
}

func TestPersistSeasonsAndEpisodes_BulkFailurePreservesPartialProgress(t *testing.T) {
	const seriesID = "series-bulk-fallback"

	service, _, seasonRepo, episodeRepo := newSeasonEpisodeServiceForTest(seriesID)
	enqueuer := &recordingImageCacheJobEnqueuer{}
	service.SetAutoCacheImages(true)
	service.SetImageCacheJobEnqueuer(enqueuer)
	seasonRepo.bulkUpsertErr = errors.New("season bulk unavailable")
	seasonRepo.upsertErrors[seasonKey(seriesID, 1)] = errors.New("season 1 rejected")
	episodeRepo.bulkUpsertErr = errors.New("episode bulk unavailable")
	episodeRepo.upsertErrors[episodeKey(seriesID, 2, 1)] = errors.New("episode 1 rejected")

	service.persistSeasonsAndEpisodes(
		context.Background(),
		&models.MediaItem{ContentID: seriesID, Type: "series"},
		map[string]string{"tvdb": "series-123"},
		"en",
		"en",
		[]SeasonResult{
			{SeasonNumber: 1, Title: "Season 1", PosterPath: "tvdb://season-1.jpg"},
			{SeasonNumber: 2, Title: "Season 2", PosterPath: "tvdb://season-2.jpg"},
			{SeasonNumber: 3, Title: "Season 3", PosterPath: "tvdb://season-3.jpg"},
		},
		[]EpisodeResult{
			{SeasonNumber: 2, EpisodeNumber: 1, Title: "Episode 1", StillPath: "tvdb://episode-1.jpg"},
			{SeasonNumber: 2, EpisodeNumber: 2, Title: "Episode 2", StillPath: "tvdb://episode-2.jpg"},
		},
		MergeFillEmpty,
	)

	if got := seasonRepo.BulkUpsertCalls(); got != 1 {
		t.Fatalf("season bulk upserts = %d, want 1", got)
	}
	if got := seasonRepo.UpsertCalls(); got != 3 {
		t.Fatalf("season fallback upserts = %d, want 3", got)
	}
	if got := episodeRepo.BulkUpsertCalls(); got != 1 {
		t.Fatalf("episode bulk upserts = %d, want 1", got)
	}
	if got := episodeRepo.UpsertCalls(); got != 2 {
		t.Fatalf("episode fallback upserts = %d, want 2", got)
	}
	if seasonRepo.seasons[seasonKey(seriesID, 1)] != nil {
		t.Fatal("failed season was persisted")
	}
	if seasonRepo.seasons[seasonKey(seriesID, 2)] == nil || seasonRepo.seasons[seasonKey(seriesID, 3)] == nil {
		t.Fatal("successful seasons did not persist after bulk fallback")
	}
	if episodeRepo.episodes[episodeKey(seriesID, 2, 1)] != nil {
		t.Fatal("failed episode was persisted")
	}
	if episodeRepo.episodes[episodeKey(seriesID, 2, 2)] == nil {
		t.Fatal("successful episode did not persist after bulk fallback")
	}

	queuedSources := make(map[string]bool, len(enqueuer.inputs))
	for _, input := range enqueuer.inputs {
		queuedSources[input.SourcePath] = true
	}
	for _, source := range []string{"tvdb://season-2.jpg", "tvdb://season-3.jpg", "tvdb://episode-2.jpg"} {
		if !queuedSources[source] {
			t.Errorf("successful source %q was not queued", source)
		}
	}
	for _, source := range []string{"tvdb://season-1.jpg", "tvdb://episode-1.jpg"} {
		if queuedSources[source] {
			t.Errorf("failed source %q was queued", source)
		}
	}
	if got := len(enqueuer.inputs); got != 3 {
		t.Fatalf("queued image jobs = %d, want 3", got)
	}
}

// uniqueProviderIDEpisodeRepo adds the provider ID rules of EpisodeRepository
// that the plain fake skips: the stale-ID clears that run before a write, the
// unique provider ID indexes, which reject a bulk write holding one ID twice,
// and the upsert keeping a stored ID when the write carries none.
type uniqueProviderIDEpisodeRepo struct {
	*fakeEpisodeRepo
}

var errDuplicateEpisodeProviderID = errors.New("duplicate key value violates unique constraint on an episode provider ID")

func episodeIDColumns(ep *models.Episode) []*string {
	return []*string{&ep.ImdbID, &ep.TmdbID, &ep.TvdbID}
}

// keepStoredEpisodeIDsLocked mirrors COALESCE(NULLIF(EXCLUDED.id, ”), stored).
func (r *uniqueProviderIDEpisodeRepo) keepStoredEpisodeIDsLocked(ep *models.Episode) *models.Episode {
	cp := *ep
	if stored, ok := r.episodes[episodeKey(ep.SeriesID, ep.SeasonNumber, ep.EpisodeNumber)]; ok {
		storedIDs := episodeIDColumns(stored)
		for column, id := range episodeIDColumns(&cp) {
			if *id == "" {
				*id = *storedIDs[column]
			}
		}
	}
	return &cp
}

func (r *uniqueProviderIDEpisodeRepo) BulkUpsert(ctx context.Context, seriesID string, episodes []*models.Episode) error {
	r.mu.Lock()
	owners := make([]map[string]string, 3)
	for column := range owners {
		owners[column] = make(map[string]string)
	}
	for _, ep := range episodes {
		key := episodeKey(seriesID, ep.SeasonNumber, ep.EpisodeNumber)
		for column, id := range episodeIDColumns(ep) {
			if *id == "" {
				continue
			}
			if _, taken := owners[column][*id]; taken {
				r.bulkUpserts++
				r.mu.Unlock()
				return errDuplicateEpisodeProviderID
			}
			owners[column][*id] = key
		}
	}
	for key, stored := range r.episodes {
		if stored.SeriesID != seriesID {
			continue
		}
		for column, id := range episodeIDColumns(stored) {
			if owner, ok := owners[column][*id]; ok && *id != "" && owner != key {
				*id = ""
			}
		}
	}
	writes := make([]*models.Episode, len(episodes))
	for i, ep := range episodes {
		writes[i] = r.keepStoredEpisodeIDsLocked(ep)
	}
	r.mu.Unlock()
	return r.fakeEpisodeRepo.BulkUpsert(ctx, seriesID, writes)
}

func (r *uniqueProviderIDEpisodeRepo) Upsert(ctx context.Context, ep *models.Episode) error {
	r.mu.Lock()
	key := episodeKey(ep.SeriesID, ep.SeasonNumber, ep.EpisodeNumber)
	incoming := episodeIDColumns(ep)
	for storedKey, stored := range r.episodes {
		if stored.SeriesID != ep.SeriesID || storedKey == key {
			continue
		}
		for column, id := range episodeIDColumns(stored) {
			if *id != "" && *id == *incoming[column] {
				*id = ""
			}
		}
	}
	write := r.keepStoredEpisodeIDsLocked(ep)
	r.mu.Unlock()
	return r.fakeEpisodeRepo.Upsert(ctx, write)
}

// TestPersistSeasonsAndEpisodes_ProviderIDsStayUniqueWithinOneBulkWrite covers
// a provider that moved an episode's ID to another episode number. The stored
// row still holds the ID, and the canonical merge keeps stored IDs, so the
// write batch put it on two episodes: the bulk write failed, and the
// single-row fallback that followed left the ID on whichever row it wrote last.
func TestPersistSeasonsAndEpisodes_ProviderIDsStayUniqueWithinOneBulkWrite(t *testing.T) {
	type ids = map[string]string
	for _, tc := range []struct {
		name     string
		stored   map[int]ids
		provided map[int]ids
		want     map[int]ids
	}{
		{
			name:     "moved to an episode without one",
			stored:   map[int]ids{2: {}, 6: {"tmdb": "tmdb-special"}},
			provided: map[int]ids{2: {"tmdb": "tmdb-special"}, 6: {"tvdb": "tvdb-reunion"}},
			want:     map[int]ids{2: {"tmdb": "tmdb-special"}, 6: {"tvdb": "tvdb-reunion"}},
		},
		{
			name:     "moved off an episode that has a new one",
			stored:   map[int]ids{2: {"tmdb": "tmdb-special"}, 6: {}},
			provided: map[int]ids{2: {"tmdb": "tmdb-premiere"}, 6: {"tmdb": "tmdb-special"}},
			want:     map[int]ids{2: {"tmdb": "tmdb-premiere"}, 6: {"tmdb": "tmdb-special"}},
		},
		{
			name:     "one ID for a two-part episode",
			stored:   map[int]ids{2: {}, 6: {}},
			provided: map[int]ids{2: {"imdb": "tt0000002"}, 6: {"imdb": "tt0000002"}},
			want:     map[int]ids{2: {"imdb": "tt0000002"}, 6: {}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const seriesID = "series-renumbered-special"

			service, _, _, plainEpisodes := newSeasonEpisodeServiceForTest(seriesID)
			episodeRepo := &uniqueProviderIDEpisodeRepo{fakeEpisodeRepo: plainEpisodes}
			service.episodeRepo = episodeRepo
			titles := map[int]string{2: "Behind the Scenes", 6: "Reunion"}
			for number, stored := range tc.stored {
				plainEpisodes.episodes[episodeKey(seriesID, 0, number)] = &models.Episode{
					ContentID:      "special-" + titles[number],
					SeriesID:       seriesID,
					SeasonNumber:   0,
					EpisodeNumber:  number,
					Title:          titles[number],
					ImdbID:         stored["imdb"],
					TmdbID:         stored["tmdb"],
					TvdbID:         stored["tvdb"],
					MetadataSource: "provider",
				}
			}
			results := make([]EpisodeResult, 0, len(tc.provided))
			for _, number := range []int{2, 6} {
				results = append(results, EpisodeResult{SeasonNumber: 0, EpisodeNumber: number, Title: titles[number], ProviderIDs: tc.provided[number]})
			}

			service.persistSeasonsAndEpisodes(
				context.Background(),
				&models.MediaItem{ContentID: seriesID, Type: "series"},
				map[string]string{"tvdb": "series-123"},
				"en",
				"en",
				[]SeasonResult{{SeasonNumber: 0, Title: "Specials"}},
				results,
				MergeFillEmpty,
			)

			if got := episodeRepo.BulkUpsertCalls(); got != 1 {
				t.Fatalf("episode bulk upserts = %d, want 1", got)
			}
			if got := episodeRepo.UpsertCalls(); got != 0 {
				t.Fatalf("episode single-row fallback upserts = %d, want 0", got)
			}
			for number, want := range tc.want {
				got := plainEpisodes.episodes[episodeKey(seriesID, 0, number)]
				if got == nil || got.ImdbID != want["imdb"] || got.TmdbID != want["tmdb"] || got.TvdbID != want["tvdb"] {
					t.Fatalf("S00E%02d = %+v, want IDs %v", number, got, want)
				}
			}
		})
	}
}

func TestPersistSeasonsAndEpisodes_PrefetchFailureUsesPointReadFallback(t *testing.T) {
	const seriesID = "series-prefetch-fallback"

	service, _, seasonRepo, episodeRepo := newSeasonEpisodeServiceForTest(seriesID)
	seasonRepo.listErr = errors.New("season list unavailable")
	episodeRepo.listByNumbersErr = errors.New("episode list unavailable")
	seasonRepo.seasons[seasonKey(seriesID, 1)] = &models.Season{
		ContentID:       "existing-season",
		SeriesID:        seriesID,
		SeasonNumber:    1,
		Title:           "Existing Season",
		Overview:        "Existing season overview",
		MetadataSource:  "provider",
		PosterThumbhash: "existing-season-thumb",
	}
	episodeRepo.episodes[episodeKey(seriesID, 1, 1)] = &models.Episode{
		ContentID:      "existing-episode",
		SeriesID:       seriesID,
		SeasonID:       "existing-season",
		SeasonNumber:   1,
		EpisodeNumber:  1,
		Title:          "Existing Episode",
		Overview:       "Existing episode overview",
		MetadataSource: "provider",
	}

	service.persistSeasonsAndEpisodes(
		context.Background(),
		&models.MediaItem{ContentID: seriesID, Type: "series"},
		nil,
		"en",
		"en",
		[]SeasonResult{{SeasonNumber: 1, Title: "Provider Season"}},
		[]EpisodeResult{{SeasonNumber: 1, EpisodeNumber: 1, Title: "Provider Episode"}},
		MergeFillEmpty,
	)

	if got := seasonRepo.GetByNumberCalls(); got != 1 {
		t.Fatalf("season point-read fallbacks = %d, want 1", got)
	}
	if got := episodeRepo.GetByNumberCalls(); got != 1 {
		t.Fatalf("episode point-read fallbacks = %d, want 1", got)
	}
	if got := seasonRepo.seasons[seasonKey(seriesID, 1)].Title; got != "Existing Season" {
		t.Fatalf("season title = %q, want existing title preserved", got)
	}
	if got := episodeRepo.episodes[episodeKey(seriesID, 1, 1)].Title; got != "Existing Episode" {
		t.Fatalf("episode title = %q, want existing title preserved", got)
	}
}

func TestPersistSeasonsAndEpisodes_DuplicateNaturalKeysKeepSequentialSemantics(t *testing.T) {
	const seriesID = "series-duplicate-persist"

	service, _, seasonRepo, episodeRepo := newSeasonEpisodeServiceForTest(seriesID)
	service.persistSeasonsAndEpisodes(
		context.Background(),
		&models.MediaItem{ContentID: seriesID, Type: "series"},
		nil,
		"en",
		"en",
		[]SeasonResult{
			{SeasonNumber: 1, Title: "First season title"},
			{SeasonNumber: 1, Title: "Second season title"},
		},
		[]EpisodeResult{
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "First episode title"},
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Second episode title"},
		},
		MergeReplaceUnlocked,
	)

	if got := seasonRepo.BulkUpsertCalls(); got != 0 {
		t.Fatalf("season bulk upserts = %d, want 0 for duplicate keys", got)
	}
	if got := episodeRepo.BulkUpsertCalls(); got != 0 {
		t.Fatalf("episode bulk upserts = %d, want 0 for duplicate keys", got)
	}
	if got := seasonRepo.GetByNumberCalls(); got != 2 {
		t.Fatalf("season sequential reads = %d, want 2", got)
	}
	if got := episodeRepo.GetByNumberCalls(); got != 2 {
		t.Fatalf("episode sequential reads = %d, want 2", got)
	}
	if got := seasonRepo.seasons[seasonKey(seriesID, 1)].Title; got != "Second season title" {
		t.Fatalf("season title = %q, want last sequential write", got)
	}
	if got := episodeRepo.episodes[episodeKey(seriesID, 1, 1)].Title; got != "Second episode title" {
		t.Fatalf("episode title = %q, want last sequential write", got)
	}
}

func TestPersonRefreshWithProviders_RecordsFailedAttempt(t *testing.T) {
	repo := newFakePersonRefreshRepo(models.Person{
		ID:     3,
		Name:   "Missing Provider Person",
		TmdbID: "missing-tmdb-id",
	})
	service := &PersonRefreshService{repo: repo}

	_, err := service.refreshPersonWithProviders(context.Background(), 3, []Provider{
		stubPersonProvider{slug: "tmdb"},
	})
	if !errors.Is(err, ErrPersonMetadataNotFound) {
		t.Fatalf("refreshPersonWithProviders() error = %v, want %v", err, ErrPersonMetadataNotFound)
	}
	if len(repo.refreshAttempts) != 1 || repo.refreshAttempts[0] != 3 {
		t.Fatalf("refresh attempts = %v, want [3]", repo.refreshAttempts)
	}
}

// A failed attempt write is bookkeeping loss, not a reason to skip the refresh
// the caller asked for.
func TestPersonRefreshWithProviders_RefreshesWhenAttemptWriteFails(t *testing.T) {
	repo := newFakePersonRefreshRepo(models.Person{
		ID:     4,
		Name:   "Attempt Write Fails",
		TmdbID: "tmdb-4",
	})
	repo.refreshAttemptErr = errors.New("timeout: context deadline exceeded")
	service := &PersonRefreshService{repo: repo}

	person, err := service.refreshPersonWithProviders(context.Background(), 4, []Provider{
		stubPersonProvider{slug: "tmdb", detail: &PersonDetailResult{
			Name: "Attempt Write Fails",
			Bio:  "Refreshed anyway",
		}},
	})
	if err != nil {
		t.Fatalf("refreshPersonWithProviders() error = %v, want nil", err)
	}
	if person.Bio != "Refreshed anyway" {
		t.Fatalf("bio = %q, want %q", person.Bio, "Refreshed anyway")
	}
}
