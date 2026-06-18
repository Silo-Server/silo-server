package metadata

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type fakePersonRefreshRepo struct {
	persons map[int64]models.Person
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

func (r *fakePersonRefreshRepo) Update(_ context.Context, person models.Person) error {
	r.persons[person.ID] = person
	return nil
}

func (r *fakePersonRefreshRepo) FindRefreshCandidates(_ context.Context, _ time.Duration, _ int) ([]int64, error) {
	return nil, nil
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

func TestAccumulateSeasonResults_FillsMissingAndAddsSeasons(t *testing.T) {
	accumulator := make(map[int]*SeasonResult)

	accumulateSeasonResults(accumulator, []SeasonResult{
		{SeasonNumber: 22, Title: "Season 22"},
		{SeasonNumber: 23, Title: "Season 23"},
	})
	accumulateSeasonResults(accumulator, []SeasonResult{
		{SeasonNumber: 23, Title: "TMDB Season 23", PosterPath: "tmdb://season-23.jpg"},
		{SeasonNumber: 24, Title: "Season 24", PosterPath: "tmdb://season-24.jpg"},
	})

	seasons := flattenSeasonResults(accumulator)
	if len(seasons) != 3 {
		t.Fatalf("len(seasons) = %d, want 3", len(seasons))
	}
	if seasons[1].SeasonNumber != 23 {
		t.Fatalf("season[1].SeasonNumber = %d, want 23", seasons[1].SeasonNumber)
	}
	if seasons[1].Title != "Season 23" {
		t.Fatalf("season 23 title = %q, want %q", seasons[1].Title, "Season 23")
	}
	if seasons[1].PosterPath != "tmdb://season-23.jpg" {
		t.Fatalf("season 23 poster = %q, want tmdb://season-23.jpg", seasons[1].PosterPath)
	}
	if seasons[2].SeasonNumber != 24 || seasons[2].PosterPath != "tmdb://season-24.jpg" {
		t.Fatalf("season 24 = %#v, want poster from lower-priority provider", seasons[2])
	}
}

func TestAccumulateEpisodeResults_FillsMissingAndAddsEpisodes(t *testing.T) {
	accumulator := make(map[episodeResultKey]*EpisodeResult)

	accumulateEpisodeResults(accumulator, []EpisodeResult{
		{
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Title:         "Pilot",
		},
	})
	accumulateEpisodeResults(accumulator, []EpisodeResult{
		{
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Title:         "TMDB Pilot",
			Runtime:       60,
			StillPath:     "tmdb://still-1.jpg",
			ProviderIDs:   map[string]string{"tmdb": "ep-1"},
		},
		{
			SeasonNumber:  1,
			EpisodeNumber: 2,
			Title:         "Episode 2",
			ProviderIDs:   map[string]string{"tmdb": "ep-2"},
		},
	})

	episodes := flattenEpisodeResults(accumulator)
	if len(episodes) != 2 {
		t.Fatalf("len(episodes) = %d, want 2", len(episodes))
	}
	if episodes[0].Title != "Pilot" {
		t.Fatalf("episode 1 title = %q, want %q", episodes[0].Title, "Pilot")
	}
	if episodes[0].Runtime != 60 {
		t.Fatalf("episode 1 runtime = %d, want 60", episodes[0].Runtime)
	}
	if episodes[0].StillPath != "tmdb://still-1.jpg" {
		t.Fatalf("episode 1 still = %q, want tmdb://still-1.jpg", episodes[0].StillPath)
	}
	if episodes[0].ProviderIDs["tmdb"] != "ep-1" {
		t.Fatalf("episode 1 tmdb id = %q, want ep-1", episodes[0].ProviderIDs["tmdb"])
	}
	if episodes[1].EpisodeNumber != 2 {
		t.Fatalf("episode 2 number = %d, want 2", episodes[1].EpisodeNumber)
	}
}

func TestPersistSeasonsAndEpisodes_ScheduledRefreshPreservesExistingAndBackfillsMissing(t *testing.T) {
	const seriesID = "series-fallback"

	service, _, seasonRepo, episodeRepo := newSeasonEpisodeServiceForTest(seriesID)
	ctx := context.Background()

	seasonRepo.seasons[seasonKey(seriesID, 1)] = &models.Season{
		ContentID:       "season-1",
		SeriesID:        seriesID,
		SeasonNumber:    1,
		Title:           "Existing Season",
		Overview:        "",
		PosterPath:      "s3://season-1.jpg",
		PosterThumbhash: "season-thumb",
	}
	episodeRepo.episodes[episodeKey(seriesID, 1, 1)] = &models.Episode{
		ContentID:      "episode-1",
		SeriesID:       seriesID,
		SeasonID:       "season-1",
		SeasonNumber:   1,
		EpisodeNumber:  1,
		Title:          "Existing Episode",
		Overview:       "",
		Runtime:        0,
		StillPath:      "s3://episode-1.jpg",
		StillThumbhash: "episode-thumb",
		MetadataSource: "provider",
	}

	service.persistSeasonsAndEpisodes(ctx, &models.MediaItem{ContentID: seriesID, Type: "series"}, nil, "en", "en",
		[]SeasonResult{{
			SeasonNumber: 1,
			Title:        "Provider Season",
			Overview:     "Filled overview",
		}},
		[]EpisodeResult{{
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Title:         "Provider Episode",
			Overview:      "Episode overview",
			Runtime:       60,
			ProviderIDs:   map[string]string{"tmdb": "tmdb-ep-1"},
		}},
		MergeFillEmpty,
	)

	season := seasonRepo.seasons[seasonKey(seriesID, 1)]
	if season.Title != "Existing Season" {
		t.Fatalf("season title = %q, want %q", season.Title, "Existing Season")
	}
	if season.Overview != "Filled overview" {
		t.Fatalf("season overview = %q, want %q", season.Overview, "Filled overview")
	}
	if season.PosterPath != "s3://season-1.jpg" || season.PosterThumbhash != "season-thumb" {
		t.Fatalf("season poster fields = (%q, %q), want existing poster preserved", season.PosterPath, season.PosterThumbhash)
	}

	episode := episodeRepo.episodes[episodeKey(seriesID, 1, 1)]
	if episode.Title != "Existing Episode" {
		t.Fatalf("episode title = %q, want %q", episode.Title, "Existing Episode")
	}
	if episode.Overview != "Episode overview" {
		t.Fatalf("episode overview = %q, want %q", episode.Overview, "Episode overview")
	}
	if episode.Runtime != 60 {
		t.Fatalf("episode runtime = %d, want 60", episode.Runtime)
	}
	if episode.StillPath != "s3://episode-1.jpg" || episode.StillThumbhash != "episode-thumb" {
		t.Fatalf("episode still fields = (%q, %q), want existing still preserved", episode.StillPath, episode.StillThumbhash)
	}
	if episode.TmdbID != "tmdb-ep-1" {
		t.Fatalf("episode tmdb id = %q, want tmdb-ep-1", episode.TmdbID)
	}
}

func TestPersistSeasonsAndEpisodes_ManualRefreshReplacesNonEmptyButPreservesBlanks(t *testing.T) {
	const seriesID = "series-manual"

	service, _, seasonRepo, episodeRepo := newSeasonEpisodeServiceForTest(seriesID)
	ctx := context.Background()

	seasonRepo.seasons[seasonKey(seriesID, 1)] = &models.Season{
		ContentID:       "season-1",
		SeriesID:        seriesID,
		SeasonNumber:    1,
		Title:           "Old Season",
		Overview:        "Old season overview",
		PosterPath:      "s3://season-old.jpg",
		PosterThumbhash: "season-thumb",
	}
	episodeRepo.episodes[episodeKey(seriesID, 1, 1)] = &models.Episode{
		ContentID:      "episode-1",
		SeriesID:       seriesID,
		SeasonID:       "season-1",
		SeasonNumber:   1,
		EpisodeNumber:  1,
		Title:          "Old Episode",
		Overview:       "Old episode overview",
		Runtime:        45,
		StillPath:      "s3://episode-old.jpg",
		StillThumbhash: "episode-thumb",
		MetadataSource: "provider",
	}

	service.persistSeasonsAndEpisodes(ctx, &models.MediaItem{ContentID: seriesID, Type: "series"}, nil, "en", "en",
		[]SeasonResult{{
			SeasonNumber: 1,
			Title:        "New Season",
			PosterPath:   "",
		}},
		[]EpisodeResult{{
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Title:         "New Episode",
			Runtime:       50,
			StillPath:     "",
		}},
		MergeReplaceUnlocked,
	)

	season := seasonRepo.seasons[seasonKey(seriesID, 1)]
	if season.Title != "New Season" {
		t.Fatalf("season title = %q, want %q", season.Title, "New Season")
	}
	if season.Overview != "Old season overview" {
		t.Fatalf("season overview = %q, want old overview preserved", season.Overview)
	}
	if season.PosterPath != "s3://season-old.jpg" || season.PosterThumbhash != "season-thumb" {
		t.Fatalf("season poster fields = (%q, %q), want existing poster preserved", season.PosterPath, season.PosterThumbhash)
	}

	episode := episodeRepo.episodes[episodeKey(seriesID, 1, 1)]
	if episode.Title != "New Episode" {
		t.Fatalf("episode title = %q, want %q", episode.Title, "New Episode")
	}
	if episode.Overview != "Old episode overview" {
		t.Fatalf("episode overview = %q, want old overview preserved", episode.Overview)
	}
	if episode.Runtime != 50 {
		t.Fatalf("episode runtime = %d, want 50", episode.Runtime)
	}
	if episode.StillPath != "s3://episode-old.jpg" || episode.StillThumbhash != "episode-thumb" {
		t.Fatalf("episode still fields = (%q, %q), want existing still preserved", episode.StillPath, episode.StillThumbhash)
	}
}

func TestBuildItemLocalizationRecord_PreservesExistingWhenRefreshIsBlank(t *testing.T) {
	existing := &models.MediaItemLocalization{
		ContentID:         "series-1",
		Language:          "fr",
		Title:             "Titre existant",
		SortTitle:         "Titre",
		Overview:          "Apercu existant",
		Tagline:           "Phrase existante",
		PosterPath:        "s3://poster.jpg",
		PosterThumbhash:   "poster-thumb",
		BackdropPath:      "s3://backdrop.jpg",
		BackdropThumbhash: "backdrop-thumb",
		LogoPath:          "s3://logo.png",
	}

	loc := buildItemLocalizationRecord(existing, "series-1", "fr", "series", &MetadataResult{}, nil, MergeReplaceUnlocked, "fr", false)

	if *loc != *existing {
		t.Fatalf("localization = %#v, want %#v", loc, existing)
	}
}

func TestBuildSeasonLocalizationRecord_PreservesExistingPosterOnBlankRefresh(t *testing.T) {
	existing := &models.SeasonLocalization{
		SeasonContentID: "season-1",
		Language:        "fr",
		Title:           "Saison 1",
		Overview:        "Apercu",
		PosterPath:      "s3://season.jpg",
		PosterThumbhash: "season-thumb",
	}

	loc := buildSeasonLocalizationRecord(existing, "season-1", "fr", SeasonResult{}, MergeReplaceUnlocked)

	if *loc != *existing {
		t.Fatalf("localization = %#v, want %#v", loc, existing)
	}
}

func TestBuildEpisodeLocalizationRecord_PreservesExistingTextOnBlankRefresh(t *testing.T) {
	existing := &models.EpisodeLocalization{
		EpisodeContentID: "episode-1",
		Language:         "fr",
		Title:            "Episode 1",
		Overview:         "Apercu",
	}

	loc := buildEpisodeLocalizationRecord(existing, "episode-1", "fr", EpisodeResult{}, MergeReplaceUnlocked)

	if *loc != *existing {
		t.Fatalf("localization = %#v, want %#v", loc, existing)
	}
}

func TestPersonRefreshWithProviders_PreservesExistingWhenProvidersOmitFields(t *testing.T) {
	repo := newFakePersonRefreshRepo(models.Person{
		ID:             1,
		Name:           "Existing Name",
		Bio:            "Existing bio",
		Homepage:       "https://existing.example",
		PhotoPath:      "s3://existing-photo.jpg",
		PhotoThumbhash: "existing-thumb",
		TmdbID:         "tmdb-1",
	})
	service := &PersonRefreshService{repo: repo}

	providers := []Provider{
		stubPersonProvider{
			slug: "tvdb",
			detail: &PersonDetailResult{
				ProviderIDs: map[string]string{"tvdb": "tvdb-1"},
			},
		},
	}

	person, err := service.refreshPersonWithProviders(context.Background(), 1, providers)
	if err != nil {
		t.Fatalf("refreshPersonWithProviders() error = %v", err)
	}
	if person.Name != "Existing Name" || person.Bio != "Existing bio" {
		t.Fatalf("person = %#v, want existing non-empty fields preserved", person)
	}
	if person.Homepage != "https://existing.example" {
		t.Fatalf("homepage = %q, want existing homepage preserved", person.Homepage)
	}
	if person.PhotoPath != "s3://existing-photo.jpg" || person.PhotoThumbhash != "existing-thumb" {
		t.Fatalf("photo fields = (%q, %q), want existing photo preserved", person.PhotoPath, person.PhotoThumbhash)
	}
	if person.TvdbID != "tvdb-1" {
		t.Fatalf("tvdb id = %q, want tvdb-1", person.TvdbID)
	}
}

func TestPersonRefreshWithProviders_FillsFallbackAcrossProviders(t *testing.T) {
	repo := newFakePersonRefreshRepo(models.Person{
		ID:     2,
		Name:   "Old Name",
		Bio:    "Existing bio",
		TmdbID: "tmdb-2",
	})
	service := &PersonRefreshService{repo: repo}

	providers := []Provider{
		stubPersonProvider{
			slug: "tmdb",
			detail: &PersonDetailResult{
				Name:        "New Name",
				ProviderIDs: map[string]string{"tmdb": "tmdb-2"},
			},
		},
		stubPersonProvider{
			slug: "tvdb",
			detail: &PersonDetailResult{
				Homepage:    "https://fallback.example",
				PhotoPath:   "https://fallback.example/photo.jpg",
				ProviderIDs: map[string]string{"tvdb": "tvdb-2"},
			},
		},
	}

	person, err := service.refreshPersonWithProviders(context.Background(), 2, providers)
	if err != nil {
		t.Fatalf("refreshPersonWithProviders() error = %v", err)
	}
	if person.Name != "New Name" {
		t.Fatalf("name = %q, want New Name", person.Name)
	}
	if person.Bio != "Existing bio" {
		t.Fatalf("bio = %q, want existing bio preserved", person.Bio)
	}
	if person.Homepage != "https://fallback.example" {
		t.Fatalf("homepage = %q, want fallback homepage", person.Homepage)
	}
	if person.PhotoPath != "https://fallback.example/photo.jpg" {
		t.Fatalf("photo path = %q, want fallback photo", person.PhotoPath)
	}
	if person.TmdbID != "tmdb-2" || person.TvdbID != "tvdb-2" {
		t.Fatalf("provider IDs = (%q, %q), want tmdb-2 and tvdb-2", person.TmdbID, person.TvdbID)
	}
}
