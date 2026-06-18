package watchstate

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type testStoreProvider struct {
	store userstore.UserStore
}

func (p testStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p testStoreProvider) Close() error {
	return nil
}

type testItemRepo struct {
	items map[string]*models.MediaItem
}

func (r testItemRepo) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	item, ok := r.items[contentID]
	if !ok {
		return nil, catalog.ErrItemNotFound
	}
	return item, nil
}

type testEpisodeRepo struct {
	episodes map[string]*models.Episode
	byKey    map[string]*models.Episode
}

func (r testEpisodeRepo) GetByID(_ context.Context, contentID string) (*models.Episode, error) {
	episode, ok := r.episodes[contentID]
	if !ok {
		return nil, catalog.ErrEpisodeNotFound
	}
	return episode, nil
}

func (r testEpisodeRepo) GetBySeriesAndNumber(_ context.Context, seriesID string, season, episode int) (*models.Episode, error) {
	got, ok := r.byKey[seriesID]
	if !ok || got.SeasonNumber != season || got.EpisodeNumber != episode {
		return nil, catalog.ErrEpisodeNotFound
	}
	return got, nil
}

type testProviderIDRepo struct {
	ids map[string][]*models.MediaItemProviderID
	err error
}

func (r testProviderIDRepo) GetByContentID(_ context.Context, contentID string) ([]*models.MediaItemProviderID, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.ids[contentID], nil
}

func (r testProviderIDRepo) FindContentIDByProviderIDs(_ context.Context, providerIDs map[string]string, itemType, _ string) (string, error) {
	for contentID, rows := range r.ids {
		for _, row := range rows {
			if row.ItemType != itemType {
				continue
			}
			if providerIDs[row.Provider] == row.ProviderID {
				return contentID, nil
			}
		}
	}
	return "", nil
}

func TestRecordPlaybackStopAddsMovieIdentity(t *testing.T) {
	store, db := newTestUserStore(t)
	defer db.Close()

	service := NewService(testStoreProvider{store: store}).WithStableIdentityResolver(NewStableIdentityResolver(
		testItemRepo{items: map[string]*models.MediaItem{
			"movie-1": {ContentID: "movie-1", Type: "movie"},
		}},
		testEpisodeRepo{},
		testProviderIDRepo{ids: map[string][]*models.MediaItemProviderID{
			"movie-1": {
				{ContentID: "movie-1", ItemType: "movie", Provider: "tmdb", ProviderID: "603"},
			},
		}},
	))

	result, err := service.RecordPlaybackStop(
		context.Background(),
		1,
		"profile-1",
		"movie-1",
		7200,
		7200,
		time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		userstore.VersionHints{},
		userstore.ProgressThresholds{},
	)
	if err != nil {
		t.Fatalf("RecordPlaybackStop: %v", err)
	}
	if !result.Completed || result.HistoryID == "" {
		t.Fatalf("RecordPlaybackStop result = %+v", result)
	}

	history, err := store.ListHistory(context.Background(), "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if history[0].Identity.StableType != "movie" {
		t.Fatalf("Identity.StableType = %q, want movie", history[0].Identity.StableType)
	}
	if got := history[0].Identity.ProviderIDs["tmdb"]; got != "603" {
		t.Fatalf("Identity.ProviderIDs[tmdb] = %q, want 603", got)
	}
}

func TestManualMarkWatchedAddsEpisodeIdentity(t *testing.T) {
	store, db := newTestUserStore(t)
	defer db.Close()

	episode := &models.Episode{
		ContentID:     "episode-1",
		SeriesID:      "series-1",
		SeasonNumber:  2,
		EpisodeNumber: 7,
	}
	service := NewService(testStoreProvider{store: store}).WithStableIdentityResolver(NewStableIdentityResolver(
		testItemRepo{},
		testEpisodeRepo{episodes: map[string]*models.Episode{"episode-1": episode}},
		testProviderIDRepo{ids: map[string][]*models.MediaItemProviderID{
			"series-1": {
				{ContentID: "series-1", ItemType: "series", Provider: "tvdb", ProviderID: "765"},
			},
		}},
	))

	err := service.RecordManualMarkWatched(
		context.Background(),
		1,
		"profile-1",
		[]LeafWatchTarget{{MediaItemID: "episode-1", DurationSeconds: 1800}},
		time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("RecordManualMarkWatched: %v", err)
	}

	history, err := store.ListHistory(context.Background(), "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if history[0].Identity.StableType != "episode" {
		t.Fatalf("Identity.StableType = %q, want episode", history[0].Identity.StableType)
	}
	if len(history[0].Identity.ProviderIDs) != 0 {
		t.Fatalf("Identity.ProviderIDs = %#v, want empty", history[0].Identity.ProviderIDs)
	}
	if got := history[0].Identity.SeriesProviderIDs["tvdb"]; got != "765" {
		t.Fatalf("Identity.SeriesProviderIDs[tvdb] = %q, want 765", got)
	}
	if history[0].Identity.Season == nil || *history[0].Identity.Season != 2 {
		t.Fatalf("Identity.Season = %v, want 2", history[0].Identity.Season)
	}
	if history[0].Identity.Episode == nil || *history[0].Identity.Episode != 7 {
		t.Fatalf("Identity.Episode = %v, want 7", history[0].Identity.Episode)
	}
}

func TestManualMarkWatchedPreservesVisibleWatchedAt(t *testing.T) {
	store, db := newTestUserStore(t)
	defer db.Close()

	watchedAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	service := NewService(testStoreProvider{store: store})
	err := service.RecordManualMarkWatched(
		context.Background(),
		1,
		"profile-1",
		[]LeafWatchTarget{{MediaItemID: "movie-1", DurationSeconds: 7200}},
		watchedAt,
	)
	if err != nil {
		t.Fatalf("RecordManualMarkWatched: %v", err)
	}

	history, err := store.ListHistory(context.Background(), "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if history[0].WatchedAt != "2026-04-25T12:00:00Z" {
		t.Fatalf("history watched_at = %q, want caller watchedAt", history[0].WatchedAt)
	}
}

func TestIdentityLookupFailureDoesNotBlockHistory(t *testing.T) {
	store, db := newTestUserStore(t)
	defer db.Close()

	service := NewService(testStoreProvider{store: store}).WithStableIdentityResolver(NewStableIdentityResolver(
		testItemRepo{items: map[string]*models.MediaItem{
			"movie-1": {ContentID: "movie-1", Type: "movie"},
		}},
		testEpisodeRepo{},
		testProviderIDRepo{err: errors.New("catalog unavailable")},
	))

	err := service.RecordManualMarkWatched(
		context.Background(),
		1,
		"profile-1",
		[]LeafWatchTarget{{MediaItemID: "movie-1", DurationSeconds: 7200}},
		time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("RecordManualMarkWatched: %v", err)
	}

	history, err := store.ListHistory(context.Background(), "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if history[0].Identity.StableType != "" ||
		len(history[0].Identity.ProviderIDs) != 0 ||
		len(history[0].Identity.SeriesProviderIDs) != 0 {
		t.Fatalf("identity = %+v, want empty", history[0].Identity)
	}
}

func TestStableIdentityResolverWithoutItemRepoDoesNotAssumeMovie(t *testing.T) {
	resolver := NewStableIdentityResolver(
		nil,
		testEpisodeRepo{},
		testProviderIDRepo{ids: map[string][]*models.MediaItemProviderID{
			"unknown-1": {
				{ContentID: "unknown-1", ItemType: "movie", Provider: "tmdb", ProviderID: "603"},
			},
		}},
	)

	identity := resolver.ResolveHistoryIdentity(context.Background(), "unknown-1")
	if identity.StableType != "" ||
		len(identity.ProviderIDs) != 0 ||
		len(identity.SeriesProviderIDs) != 0 {
		t.Fatalf("identity = %+v, want empty", identity)
	}
}

func TestStableIdentityResolverResolvesEpisodeContentID(t *testing.T) {
	episode := &models.Episode{
		ContentID:     "episode-1",
		SeriesID:      "series-1",
		SeasonNumber:  2,
		EpisodeNumber: 7,
	}
	resolver := NewStableIdentityResolver(
		testItemRepo{},
		testEpisodeRepo{byKey: map[string]*models.Episode{"series-1": episode}},
		testProviderIDRepo{ids: map[string][]*models.MediaItemProviderID{
			"series-1": {
				{ContentID: "series-1", ItemType: "series", Provider: "tmdb", ProviderID: "123"},
			},
		}},
	)

	contentID, err := resolver.ResolveEpisodeContentID(context.Background(), map[string]string{"tmdb": "123"}, 2, 7)
	if err != nil {
		t.Fatalf("ResolveEpisodeContentID: %v", err)
	}
	if contentID != "episode-1" {
		t.Fatalf("contentID = %q, want episode-1", contentID)
	}
}

func TestStableIdentityResolverResolvesSeasonZeroSpecial(t *testing.T) {
	episode := &models.Episode{
		ContentID:     "special-1",
		SeriesID:      "series-1",
		SeasonNumber:  0,
		EpisodeNumber: 1,
	}
	resolver := NewStableIdentityResolver(
		testItemRepo{},
		testEpisodeRepo{byKey: map[string]*models.Episode{"series-1": episode}},
		testProviderIDRepo{ids: map[string][]*models.MediaItemProviderID{
			"series-1": {
				{ContentID: "series-1", ItemType: "series", Provider: "tmdb", ProviderID: "123"},
			},
		}},
	)

	contentID, err := resolver.ResolveEpisodeContentID(context.Background(), map[string]string{"tmdb": "123"}, 0, 1)
	if err != nil {
		t.Fatalf("ResolveEpisodeContentID: %v", err)
	}
	if contentID != "special-1" {
		t.Fatalf("contentID = %q, want special-1", contentID)
	}
}

func TestManualMarkUnwatchedSuppressesImportedHistoryButReturnsManualHistory(t *testing.T) {
	store, db := newTestUserStore(t)
	defer db.Close()

	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Profile"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if err := store.SetProgressAt(
		context.Background(),
		"profile-1",
		"movie-1",
		0,
		7200,
		true,
		time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("SetProgressAt: %v", err)
	}
	if err := store.AddHistory(context.Background(), userstore.WatchHistoryEntry{
		ID:              "trakt-history-1",
		ProfileID:       "profile-1",
		MediaItemID:     "movie-1",
		WatchedAt:       "2026-05-04T12:00:00Z",
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourceTrakt,
		Identity: userstore.WatchIdentity{
			StableType:  "movie",
			ProviderIDs: map[string]string{"tmdb": "603"},
		},
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}
	if err := store.AddHistory(context.Background(), userstore.WatchHistoryEntry{
		ID:              "simkl-history-1",
		ProfileID:       "profile-1",
		MediaItemID:     "movie-1",
		WatchedAt:       "2026-05-04T13:00:00Z",
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourceSimkl,
		Identity: userstore.WatchIdentity{
			StableType:  "movie",
			ProviderIDs: map[string]string{"tmdb": "603"},
		},
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}
	if err := store.AddHistory(context.Background(), userstore.WatchHistoryEntry{
		ID:              "manual-history-1",
		ProfileID:       "profile-1",
		MediaItemID:     "movie-1",
		WatchedAt:       "2026-05-04T14:00:00Z",
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourceManual,
		Identity: userstore.WatchIdentity{
			StableType:  "movie",
			ProviderIDs: map[string]string{"tmdb": "603"},
		},
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}

	service := NewService(testStoreProvider{store: store})
	result, err := service.RecordManualMarkUnwatchedWithResult(context.Background(), 1, "profile-1", []string{"movie-1"})
	if err != nil {
		t.Fatalf("RecordManualMarkUnwatchedWithResult: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Source != userstore.WatchHistorySourceManual {
		t.Fatalf("unwatch result entries = %+v, want only manual history for outbound sync", result.Entries)
	}

	progress, err := store.GetProgress(context.Background(), "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if progress != nil {
		t.Fatalf("progress after unwatch = %+v, want nil", progress)
	}
	completedItems, err := store.ListCompletedHistoryItems(context.Background(), userstore.CompletedHistoryItemQuery{
		ProfileID:    "profile-1",
		MediaItemIDs: []string{"movie-1"},
	})
	if err != nil {
		t.Fatalf("ListCompletedHistoryItems: %v", err)
	}
	if len(completedItems) != 0 {
		t.Fatalf("completed items after unwatch = %v, want empty", completedItems)
	}
}

func TestManualMarkUnwatchedReturnsOneOutboundEntryPerTarget(t *testing.T) {
	store, db := newTestUserStore(t)
	defer db.Close()
	createWatchstateProfile(t, store)

	for _, entry := range []userstore.WatchHistoryEntry{
		{
			ID:              "manual-history-older",
			ProfileID:       "profile-1",
			MediaItemID:     "movie-1",
			WatchedAt:       "2026-05-04T12:00:00Z",
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourceManual,
			Identity: userstore.WatchIdentity{
				StableType:  "movie",
				ProviderIDs: map[string]string{"tmdb": "603"},
			},
		},
		{
			ID:              "manual-history-newer",
			ProfileID:       "profile-1",
			MediaItemID:     "movie-1",
			WatchedAt:       "2026-05-04T13:00:00Z",
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourceManual,
			Identity: userstore.WatchIdentity{
				StableType:  "movie",
				ProviderIDs: map[string]string{"tmdb": "603"},
			},
		},
	} {
		if err := store.AddHistory(context.Background(), entry); err != nil {
			t.Fatalf("AddHistory(%s): %v", entry.ID, err)
		}
	}

	service := NewService(testStoreProvider{store: store})
	result, err := service.RecordManualMarkUnwatchedWithResult(context.Background(), 1, "profile-1", []string{"movie-1"})
	if err != nil {
		t.Fatalf("RecordManualMarkUnwatchedWithResult: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("unwatch result entries = %+v, want one representative entry", result.Entries)
	}
	if result.Entries[0].ID != "manual-history-newer" {
		t.Fatalf("representative history id = %q, want newest manual history", result.Entries[0].ID)
	}
}

func TestManualMarkWatchedAfterHiddenWatermarkIsVisible(t *testing.T) {
	store, db := newTestUserStore(t)
	defer db.Close()
	createWatchstateProfile(t, store)

	hiddenBefore := time.Now().UTC().Add(time.Second).Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO hidden_history_items (profile_id, media_item_id, hidden_before, updated_at)
		VALUES (?, ?, ?, ?)`,
		"profile-1",
		"movie-1",
		hiddenBefore,
		hiddenBefore,
	); err != nil {
		t.Fatalf("seed hidden watermark: %v", err)
	}

	service := NewService(testStoreProvider{store: store})
	result, err := service.RecordManualMarkWatchedWithResult(
		context.Background(),
		1,
		"profile-1",
		[]LeafWatchTarget{{MediaItemID: "movie-1", DurationSeconds: 7200}},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("RecordManualMarkWatchedWithResult: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("result entries = %+v, want one history entry", result.Entries)
	}
	if result.Entries[0].WatchedAt <= hiddenBefore {
		t.Fatalf("history watched_at = %q, want after hidden_before %q", result.Entries[0].WatchedAt, hiddenBefore)
	}

	progress, err := store.GetProgress(context.Background(), "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if progress == nil || !progress.Completed {
		t.Fatalf("progress = %+v, want visible completed progress", progress)
	}
	history, err := store.ListHistory(context.Background(), "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 || history[0].WatchedAt <= hiddenBefore {
		t.Fatalf("history = %+v, want visible history after hidden watermark %q", history, hiddenBefore)
	}
}

func TestImportedWatchIfNewerDoesNotOverwriteNewerResume(t *testing.T) {
	store, db := newTestUserStore(t)
	defer db.Close()
	createWatchstateProfile(t, store)
	if err := store.SetProgressAt(
		context.Background(),
		"profile-1",
		"movie-1",
		1200,
		7200,
		false,
		time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("SetProgressAt: %v", err)
	}

	watchedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	service := NewService(testStoreProvider{store: store})
	created, err := service.RecordImportedWatchIfNewerWithSource(
		context.Background(),
		1,
		"profile-1",
		"movie-1",
		7200,
		0,
		true,
		watchedAt,
		&watchedAt,
		userstore.WatchHistorySourceTrakt,
	)
	if err != nil {
		t.Fatalf("RecordImportedWatchIfNewerWithSource: %v", err)
	}
	if !created {
		t.Fatal("created = false, want imported history row recorded")
	}
	progress, err := store.GetProgress(context.Background(), "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if progress == nil || progress.Completed || progress.PositionSeconds != 1200 {
		t.Fatalf("progress after older import = %+v, want newer resume preserved", progress)
	}
}

func TestImportedWatchIfNewerCompletesOlderResume(t *testing.T) {
	store, db := newTestUserStore(t)
	defer db.Close()
	createWatchstateProfile(t, store)
	if err := store.SetProgressAt(
		context.Background(),
		"profile-1",
		"movie-1",
		1200,
		7200,
		false,
		time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("SetProgressAt: %v", err)
	}

	watchedAt := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	service := NewService(testStoreProvider{store: store})
	created, err := service.RecordImportedWatchIfNewerWithSource(
		context.Background(),
		1,
		"profile-1",
		"movie-1",
		7200,
		0,
		true,
		watchedAt,
		&watchedAt,
		userstore.WatchHistorySourceSimkl,
	)
	if err != nil {
		t.Fatalf("RecordImportedWatchIfNewerWithSource: %v", err)
	}
	if !created {
		t.Fatal("created = false, want imported history row recorded")
	}
	progress, err := store.GetProgress(context.Background(), "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if progress == nil || !progress.Completed || progress.PositionSeconds != 0 {
		t.Fatalf("progress after newer import = %+v, want completed projection", progress)
	}
}

func TestImportedWatchIfNewerSuppressesHiddenOlderWatch(t *testing.T) {
	store, db := newTestUserStore(t)
	defer db.Close()
	createWatchstateProfile(t, store)

	hiddenBefore := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	if err := store.RemoveHistoryItems(context.Background(), "profile-1", []string{"movie-1"}, hiddenBefore); err != nil {
		t.Fatalf("RemoveHistoryItems: %v", err)
	}
	watchedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	service := NewService(testStoreProvider{store: store})
	created, err := service.RecordImportedWatchIfNewerWithSource(
		context.Background(),
		1,
		"profile-1",
		"movie-1",
		7200,
		0,
		true,
		watchedAt,
		&watchedAt,
		userstore.WatchHistorySourceTrakt,
	)
	if err != nil {
		t.Fatalf("RecordImportedWatchIfNewerWithSource: %v", err)
	}
	if created {
		t.Fatal("created = true, want hidden imported history skipped")
	}
	progress, err := store.GetProgress(context.Background(), "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if progress != nil {
		t.Fatalf("progress after hidden import = %+v, want nil", progress)
	}
	completedItems, err := store.ListCompletedHistoryItems(context.Background(), userstore.CompletedHistoryItemQuery{
		ProfileID:    "profile-1",
		MediaItemIDs: []string{"movie-1"},
	})
	if err != nil {
		t.Fatalf("ListCompletedHistoryItems: %v", err)
	}
	if len(completedItems) != 0 {
		t.Fatalf("completed items = %v, want hidden import skipped", completedItems)
	}
}

func newTestUserStore(t *testing.T) (userstore.UserStore, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := userdb.InitSchema(db); err != nil {
		db.Close()
		t.Fatalf("InitSchema: %v", err)
	}
	return userdb.NewSQLiteUserStore(db), db
}

func createWatchstateProfile(t *testing.T, store userstore.UserStore) {
	t.Helper()
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Profile"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
}
