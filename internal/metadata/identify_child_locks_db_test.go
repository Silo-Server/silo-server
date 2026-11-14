package metadata

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	identifyEditTitle    = "Edited title"
	identifyEditOverview = "Edited overview"
	identifyEditAirDate  = "2001-02-03"
	identifyEditRuntime  = 77
	identifyEditTagline  = "Edited tagline"
)

var (
	identifySeasonLocks  = []int{int(FieldName), int(FieldOverview), int(FieldReleaseDates)}
	identifyEpisodeLocks = []int{int(FieldName), int(FieldOverview), int(FieldReleaseDates), int(FieldRuntime)}
)

type identifyLockFixture struct {
	service     *MetadataService
	items       *catalog.ItemRepository
	providerIDs *catalog.ProviderIDRepository
	seasons     *catalog.SeasonRepository
	episodes    *catalog.EpisodeRepository
}

// seed stores a matched series with one season and one episode, both edited
// and locked, and returns the series id.
func (f *identifyLockFixture) seed(t *testing.T, tvdb string) string {
	t.Helper()
	return f.seedAt(t, "series-tvdb-"+tvdb, tvdb)
}

// seedAt is seed for a series stored under seriesID.
func (f *identifyLockFixture) seedAt(t *testing.T, seriesID, tvdb string) string {
	t.Helper()
	ctx := t.Context()
	if err := f.items.Upsert(ctx, &models.MediaItem{
		ContentID: seriesID, Type: "series", Title: "Show", Status: "matched", TvdbID: tvdb,
		DefaultMetadataLanguage: "en",
		Studios:                 []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.providerIDs.ReplaceByContentID(ctx, seriesID, map[string]string{"tvdb": tvdb}); err != nil {
		t.Fatal(err)
	}
	seasonID, ok := contentid.ForSeason(seriesID, 1)
	if !ok {
		seasonID = seriesID + "-s1"
	}
	episodeID, ok := contentid.ForEpisode(seriesID, 1, 1)
	if !ok {
		episodeID = seriesID + "-s1e1"
	}
	if err := f.seasons.Upsert(ctx, &models.Season{ContentID: seasonID, SeriesID: seriesID, SeasonNumber: 1, Title: "Season 1", DefaultMetadataLanguage: "en", MetadataSource: "provider"}); err != nil {
		t.Fatal(err)
	}
	if err := f.episodes.Upsert(ctx, &models.Episode{ContentID: episodeID, SeriesID: seriesID, SeasonID: seasonID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Episode 1", DefaultMetadataLanguage: "en", MetadataSource: "provider"}); err != nil {
		t.Fatal(err)
	}
	// The title details need no lock: no provider writes them.
	title, overview, airDate, tagline := identifyEditTitle, identifyEditOverview, identifyEditAirDate, identifyEditTagline
	if err := f.seasons.UpdateMetadata(ctx, seasonID, &catalog.MetadataUpdate{Title: &title, SortTitle: &title, OriginalTitle: &title, Overview: &overview, Tagline: &tagline, AirDate: &airDate, TvdbID: &tvdb, LockedFields: &identifySeasonLocks}); err != nil {
		t.Fatal(err)
	}
	runtime := identifyEditRuntime
	if err := f.episodes.UpdateMetadata(ctx, episodeID, &catalog.MetadataUpdate{Title: &title, SortTitle: &title, OriginalTitle: &title, Overview: &overview, Tagline: &tagline, AirDate: &airDate, Runtime: &runtime, LockedFields: &identifyEpisodeLocks}); err != nil {
		t.Fatal(err)
	}
	return seriesID
}

func (f *identifyLockFixture) identify(t *testing.T, seriesID, tvdb string) string {
	t.Helper()
	ids := map[string]string{"tvdb": tvdb}
	return f.identifyChoosing(t, seriesID, ids, ids)
}

// identifyChoosing runs an Identify on the IDs the admin chose, with the
// provider returning the IDs it knows for that show.
func (f *identifyLockFixture) identifyChoosing(t *testing.T, seriesID string, chosen, returned map[string]string) string {
	t.Helper()
	remote := &remoteStubProvider{
		slug:     "tvdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Named show", ProviderIDs: returned},
	}
	result, err := f.service.ProcessWithProviders(t.Context(), ProcessRequest{
		ContentID: seriesID, ProviderIDs: chosen, Language: "en", Mode: ModeIdentify,
	}, []Provider{remote})
	if err != nil {
		t.Fatal(err)
	}
	return result.ContentID
}

func (f *identifyLockFixture) read(t *testing.T, seriesID string) (*models.Season, *models.Episode) {
	t.Helper()
	season, err := f.seasons.GetBySeriesAndNumber(t.Context(), seriesID, 1)
	if err != nil {
		t.Fatal(err)
	}
	episode, err := f.episodes.GetBySeriesAndNumber(t.Context(), seriesID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return season, episode
}

func newIdentifyLockFixture(pool *pgxpool.Pool) *identifyLockFixture {
	items := catalog.NewItemRepository(pool)
	providerIDs := catalog.NewProviderIDRepository(pool)
	episodes := catalog.NewEpisodeRepository(pool)
	seasons := catalog.NewSeasonRepository(pool)
	return &identifyLockFixture{
		service: NewMetadataService(nil, nil, nil, items, providerIDs, episodes, seasons,
			catalog.NewLibraryItemRepository(pool), catalog.NewFolderRepository(pool),
			nil, nil, nil, nil, nil),
		items: items, providerIDs: providerIDs, seasons: seasons, episodes: episodes,
	}
}

func (f *identifyLockFixture) storeOnDeadAnchor(t *testing.T, pool *pgxpool.Pool, seriesID, dead string, ids map[string]string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE media_items SET tvdb_id = $2, tmdb_id = $3 WHERE content_id = $1`, seriesID, ids["tvdb"], ids["tmdb"]); err != nil {
		t.Fatal(err)
	}
	if err := f.providerIDs.ReplaceByContentID(t.Context(), seriesID, ids); err != nil {
		t.Fatal(err)
	}
	upsertStaleProviderIDValues(t.Context(), NewStaleMediaIDRepository(pool), seriesID, providerIDValueSet{"tvdb": {dead: {}}})
	f.service.staleIDRepo = NewStaleMediaIDRepository(pool)
	t.Cleanup(func() { f.service.staleIDRepo = nil })
}

func (f *identifyLockFixture) assertLocksKept(t *testing.T, seriesID string) {
	t.Helper()
	season, episode := f.read(t, seriesID)
	if !slices.Equal(season.LockedFields, identifySeasonLocks) || !slices.Equal(episode.LockedFields, identifyEpisodeLocks) {
		t.Errorf("locks = %v, %v, want them kept", season.LockedFields, episode.LockedFields)
	}
}

// Season and episode locks describe one show. An Identify that replaces the show
// clears them along with the values they held, so nothing an admin wrote about
// the wrong show stays, even where the new show's provider has no value. An
// Identify that confirms the same show changes nothing about them.
func TestIdentify_SeasonEpisodeLocksFollowTheShow(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	f := newIdentifyLockFixture(pool)
	providerIDs := f.providerIDs
	nonce := time.Now().UnixNano() % 100_000_000
	cleanup := func(t *testing.T, ids ...string) {
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
		})
	}

	t.Run("a different show clears the edits", func(t *testing.T) {
		wrong, right := strconv.FormatInt(610_000_000+nonce, 10), strconv.FormatInt(710_000_000+nonce, 10)
		to := "series-tvdb-" + right
		from := f.seed(t, wrong)
		cleanup(t, from, to)
		if got := f.identify(t, from, right); got != to {
			t.Fatalf("identify moved to %q, want %q", got, to)
		}
		season, episode := f.read(t, to)
		if len(season.LockedFields) != 0 || len(episode.LockedFields) != 0 {
			t.Errorf("locks = %v, %v, want none", season.LockedFields, episode.LockedFields)
		}
		assertEditsCleared(t, pool, to, season, episode)
	})

	t.Run("a dead anchor replaced by a different show clears the edits", func(t *testing.T) {
		dead, right := strconv.FormatInt(620_000_000+nonce, 10), strconv.FormatInt(720_000_000+nonce, 10)
		seriesID := f.seed(t, dead)
		cleanup(t, seriesID, "series-tvdb-"+right)
		upsertStaleProviderIDValues(t.Context(), NewStaleMediaIDRepository(pool), seriesID, providerIDValueSet{"tvdb": {dead: {}}})
		f.service.staleIDRepo = NewStaleMediaIDRepository(pool)
		t.Cleanup(func() { f.service.staleIDRepo = nil })
		if got := f.identify(t, seriesID, right); got != seriesID {
			t.Fatalf("identify moved to %q, want the id kept at %q", got, seriesID)
		}
		season, episode := f.read(t, seriesID)
		assertEditsCleared(t, pool, seriesID, season, episode)
	})

	// An earlier correction left each series on a dead anchor and stored another
	// show's IDs, so correcting it again doesn't move the content ID.
	for _, tc := range []struct {
		name string
		tmdb string
	}{
		{"a second correction on a kept dead anchor clears the edits", ""},
		{"a shared lower-priority id doesn't hide a different show", "4242"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nonce := time.Now().UnixNano() % 100_000_000
			dead, previous, right := strconv.FormatInt(630_000_000+nonce, 10), strconv.FormatInt(730_000_000+nonce, 10), strconv.FormatInt(830_000_000+nonce, 10)
			seriesID := f.seed(t, dead)
			cleanup(t, seriesID, "series-tvdb-"+right)
			f.storeOnDeadAnchor(t, pool, seriesID, dead, map[string]string{"tvdb": previous, "tmdb": tc.tmdb})
			chosen := map[string]string{"tvdb": right, "tmdb": tc.tmdb}
			if got := f.identifyChoosing(t, seriesID, chosen, chosen); got != seriesID {
				t.Fatalf("identify moved to %q, want the id kept at %q", got, seriesID)
			}
			season, episode := f.read(t, seriesID)
			assertEditsCleared(t, pool, seriesID, season, episode)
		})
	}

	t.Run("the same show chosen by another provider's id keeps the edits", func(t *testing.T) {
		tvdb, tmdb := strconv.FormatInt(840_000_000+nonce, 10), strconv.FormatInt(850_000_000+nonce, 10)
		seriesID := f.seed(t, tvdb)
		cleanup(t, seriesID)
		f.identifyChoosing(t, seriesID, map[string]string{"tmdb": tmdb}, map[string]string{"tmdb": tmdb, "tvdb": tvdb})
		f.assertLocksKept(t, seriesID)
	})

	t.Run("correcting another provider's id on the same show keeps the edits", func(t *testing.T) {
		tvdb := strconv.FormatInt(860_000_000+nonce, 10)
		seriesID := f.seed(t, tvdb)
		cleanup(t, seriesID)
		wrongIMDB, rightIMDB := "tt"+strconv.FormatInt(70_000_000+nonce, 10), "tt"+strconv.FormatInt(80_000_000+nonce, 10)
		if err := providerIDs.ReplaceByContentID(t.Context(), seriesID, map[string]string{"tvdb": tvdb, "imdb": wrongIMDB}); err != nil {
			t.Fatal(err)
		}
		chosen := map[string]string{"tvdb": tvdb, "imdb": rightIMDB}
		f.identifyChoosing(t, seriesID, chosen, chosen)
		f.assertLocksKept(t, seriesID)
	})

	t.Run("the same show keeps the edits", func(t *testing.T) {
		tvdb := strconv.FormatInt(810_000_000+nonce, 10)
		seriesID := f.seed(t, tvdb)
		cleanup(t, seriesID)
		if got := f.identify(t, seriesID, tvdb); got != seriesID {
			t.Fatalf("identify moved to %q, want %q", got, seriesID)
		}
		season, episode := f.read(t, seriesID)
		if !slices.Equal(season.LockedFields, identifySeasonLocks) || season.Title != identifyEditTitle || season.Tagline != identifyEditTagline || season.TvdbID != tvdb {
			t.Errorf("season = %q, %q, %q with locks %v, want the edits kept", season.Title, season.Tagline, season.TvdbID, season.LockedFields)
		}
		if !slices.Equal(episode.LockedFields, identifyEpisodeLocks) || episode.Title != identifyEditTitle || episode.Tagline != identifyEditTagline {
			t.Errorf("episode = %q, %q with locks %v, want the edits kept", episode.Title, episode.Tagline, episode.LockedFields)
		}
	})
}

// These corrections reach the new show without an Identify that renames the
// series or keeps its id: a corrected NFO that moves the id off a dead anchor,
// a matched local series's promotion, and a write retried after a provisional
// item gave up the chosen id. Each still clears the old show's edits.
func TestIdentify_SeasonEpisodeLocksClearOnEveryCorrectionPath(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	f := newIdentifyLockFixture(pool)
	providerIDs := f.providerIDs
	nonce := time.Now().UnixNano() % 100_000_000
	cleanup := func(t *testing.T, ids ...string) {
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
		})
	}

	t.Run("a corrected NFO over a dead anchor clears the edits", func(t *testing.T) {
		dead, right := strconv.FormatInt(640_000_000+nonce, 10), strconv.FormatInt(740_000_000+nonce, 10)
		seriesID, to := f.seed(t, dead), "series-tvdb-"+right
		cleanup(t, seriesID, to)
		f.storeOnDeadAnchor(t, pool, seriesID, dead, map[string]string{"tvdb": dead})
		nfo := &localHintStubProvider{hints: map[string]string{"tvdb": right}, metadata: &MetadataResult{HasMetadata: true, Title: "Named show"}}
		remote := &remoteStubProvider{slug: "tvdb", metadata: &MetadataResult{HasMetadata: true, Title: "Named show", ProviderIDs: map[string]string{"tvdb": right}}}
		result, err := f.service.ProcessWithProviders(t.Context(), ProcessRequest{ContentID: seriesID, Language: "en", Mode: ModeManualRefresh}, []Provider{nfo, remote})
		if err != nil {
			t.Fatal(err)
		}
		if result.ContentID != to {
			t.Fatalf("refresh moved to %q, want %q", result.ContentID, to)
		}
		season, episode := f.read(t, to)
		assertEditsCleared(t, pool, to, season, episode)
	})

	t.Run("a matched local series corrected to a different show clears the edits", func(t *testing.T) {
		dead, right := strconv.FormatInt(650_000_000+nonce, 10), strconv.FormatInt(750_000_000+nonce, 10)
		seriesID, to := f.seedAt(t, contentid.ForLocal("/identify-locks/"+dead), dead), "series-tvdb-"+right
		cleanup(t, seriesID, to)
		f.storeOnDeadAnchor(t, pool, seriesID, dead, map[string]string{"tvdb": dead})
		if got := f.identify(t, seriesID, right); got != to {
			t.Fatalf("identify moved to %q, want %q", got, to)
		}
		season, episode := f.read(t, to)
		assertEditsCleared(t, pool, to, season, episode)
	})

	t.Run("a write retried after a provisional item gives up the id clears the edits", func(t *testing.T) {
		dead, right := strconv.FormatInt(660_000_000+nonce, 10), strconv.FormatInt(760_000_000+nonce, 10)
		seriesID := f.seed(t, dead)
		holder := contentid.ForLocal("/identify-locks/holder-" + right)
		cleanup(t, seriesID, holder, "series-tvdb-"+right)
		f.storeOnDeadAnchor(t, pool, seriesID, dead, map[string]string{"tvdb": dead})
		if err := f.items.Upsert(t.Context(), &models.MediaItem{
			ContentID: holder, Type: "series", Title: "Unmatched", Status: "unmatched", DefaultMetadataLanguage: "en",
			Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
		}); err != nil {
			t.Fatal(err)
		}
		if err := providerIDs.ReplaceByContentID(t.Context(), holder, map[string]string{"tvdb": right}); err != nil {
			t.Fatal(err)
		}
		if got := f.identify(t, seriesID, right); got != seriesID {
			t.Fatalf("identify moved to %q, want the id kept at %q", got, seriesID)
		}
		season, episode := f.read(t, seriesID)
		assertEditsCleared(t, pool, seriesID, season, episode)
	})
}

// assertEditsCleared checks nothing an admin wrote remains, and that rows the
// new show's provider didn't return fall back to their default titles.
func assertEditsCleared(t *testing.T, pool *pgxpool.Pool, seriesID string, season *models.Season, episode *models.Episode) {
	t.Helper()
	if len(season.LockedFields) != 0 || len(episode.LockedFields) != 0 {
		t.Errorf("locks = %v, %v, want none", season.LockedFields, episode.LockedFields)
	}
	if season.Title != "Season 1" || season.Overview != "" || season.AirDate != nil {
		t.Errorf("season = %q, %q, %v, want the default title and nothing else", season.Title, season.Overview, season.AirDate)
	}
	if details := []string{season.SortTitle, season.OriginalTitle, season.Tagline, season.TvdbID}; slices.ContainsFunc(details, func(v string) bool { return v != "" }) {
		t.Errorf("season title details = %q, want them cleared", details)
	}
	if episode.Title != "Episode 1" || episode.Overview != "" || episode.AirDate != nil || episode.Runtime != 0 {
		t.Errorf("episode = %q, %q, %v, %d, want the default title and nothing else", episode.Title, episode.Overview, episode.AirDate, episode.Runtime)
	}
	if details := []string{episode.SortTitle, episode.OriginalTitle, episode.Tagline}; slices.ContainsFunc(details, func(v string) bool { return v != "" }) {
		t.Errorf("episode title details = %q, want them cleared", details)
	}
	var lastAired *time.Time
	if err := pool.QueryRow(t.Context(), `SELECT last_air_date_at FROM media_items WHERE content_id = $1`, seriesID).Scan(&lastAired); err != nil {
		t.Fatal(err)
	}
	if lastAired != nil {
		t.Errorf("series last aired %v, want no date once the edited one is gone", lastAired)
	}
}
