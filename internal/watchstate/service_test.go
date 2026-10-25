package watchstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

	entry := onlyHistoryEntry(t, store)
	if entry.Identity.StableType != "movie" {
		t.Fatalf("Identity.StableType = %q, want movie", entry.Identity.StableType)
	}
	if got := entry.Identity.ProviderIDs["tmdb"]; got != "603" {
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

	entry := onlyHistoryEntry(t, store)
	if entry.Identity.StableType != "episode" {
		t.Fatalf("Identity.StableType = %q, want episode", entry.Identity.StableType)
	}
	if len(entry.Identity.ProviderIDs) != 0 {
		t.Fatalf("Identity.ProviderIDs = %#v, want empty", entry.Identity.ProviderIDs)
	}
	if got := entry.Identity.SeriesProviderIDs["tvdb"]; got != "765" {
		t.Fatalf("Identity.SeriesProviderIDs[tvdb] = %q, want 765", got)
	}
	if entry.Identity.Season == nil || *entry.Identity.Season != 2 {
		t.Fatalf("Identity.Season = %v, want 2", entry.Identity.Season)
	}
	if entry.Identity.Episode == nil || *entry.Identity.Episode != 7 {
		t.Fatalf("Identity.Episode = %v, want 7", entry.Identity.Episode)
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

	entry := onlyHistoryEntry(t, store)
	if entry.WatchedAt != "2026-04-25T12:00:00Z" {
		t.Fatalf("history watched_at = %q, want caller watchedAt", entry.WatchedAt)
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

	entry := onlyHistoryEntry(t, store)
	if entry.Identity.StableType != "" ||
		len(entry.Identity.ProviderIDs) != 0 ||
		len(entry.Identity.SeriesProviderIDs) != 0 {
		t.Fatalf("identity = %+v, want empty", entry.Identity)
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
	if entry := onlyHistoryEntry(t, store); entry.WatchedAt <= hiddenBefore {
		t.Fatalf("history = %+v, want visible history after hidden watermark %q", entry, hiddenBefore)
	}
}

// An imported completed watch projects onto the resume point only when it is
// newer than that resume point; either way the history row is recorded.
func TestImportedWatchIfNewerAgainstExistingResume(t *testing.T) {
	for _, tc := range []struct {
		name          string
		resumeAt      time.Time
		watchedAt     time.Time
		source        userstore.WatchHistorySource
		wantCompleted bool
		wantPosition  float64
	}{
		{
			name:         "an older import keeps the newer resume",
			resumeAt:     time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
			watchedAt:    time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
			source:       userstore.WatchHistorySourceTrakt,
			wantPosition: 1200,
		},
		{
			name:          "a newer import completes the older resume",
			resumeAt:      time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
			watchedAt:     time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
			source:        userstore.WatchHistorySourceSimkl,
			wantCompleted: true,
			wantPosition:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db := newTestUserStore(t)
			defer func() { _ = db.Close() }()
			createWatchstateProfile(t, store)
			if err := store.SetProgressAt(context.Background(), "profile-1", "movie-1", 1200, 7200, false, tc.resumeAt); err != nil {
				t.Fatalf("SetProgressAt: %v", err)
			}

			watchedAt := tc.watchedAt
			service := NewService(testStoreProvider{store: store})
			created, err := service.RecordImportedWatchIfNewerWithSource(
				context.Background(), 1, "profile-1", "movie-1", 7200, 0, true, watchedAt, &watchedAt, tc.source,
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
			if progress == nil || progress.Completed != tc.wantCompleted || progress.PositionSeconds != tc.wantPosition {
				t.Fatalf("progress after import = %+v, want completed %v at %v", progress, tc.wantCompleted, tc.wantPosition)
			}
		})
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

// TestClearResumePositionKeepsHistoryAndWatchedState pins issue #2134: a
// resume position reset leaves every earlier watch in history (unlike mark
// unwatched, which hides it) and keeps a rewatched item watched.
func TestClearResumePositionKeepsHistoryAndWatchedState(t *testing.T) {
	ctx := context.Background()
	store, db := newTestUserStore(t)
	t.Cleanup(func() { _ = db.Close() })
	createWatchstateProfile(t, store)
	service := NewService(testStoreProvider{store: store})

	// movie-1 was watched to the end two days ago and is now partly rewatched;
	// movie-2 was only ever skimmed to the middle.
	if err := service.RecordManualMarkWatched(ctx, 1, "profile-1", []LeafWatchTarget{{MediaItemID: "movie-1", DurationSeconds: 7200}}, time.Now().UTC().Add(-48*time.Hour)); err != nil {
		t.Fatalf("RecordManualMarkWatched: %v", err)
	}
	for _, play := range []struct {
		id       string
		position float64
	}{{"movie-1", 1800}, {"movie-2", 2700}} {
		if _, err := service.RecordPlaybackStop(ctx, 1, "profile-1", play.id, 7200, play.position, time.Now().UTC().Add(-time.Hour), userstore.VersionHints{}, userstore.ProgressThresholds{}); err != nil {
			t.Fatalf("RecordPlaybackStop(%s): %v", play.id, err)
		}
	}
	before, err := store.ListHistory(ctx, "profile-1", 10, 0)
	if err != nil || len(before) != 3 {
		t.Fatalf("history before = %+v, %v; want the mark and two partial plays", before, err)
	}

	for _, id := range []string{"movie-1", "movie-2"} {
		cleared, err := service.ClearResumePosition(ctx, 1, "profile-1", id)
		if err != nil || !cleared {
			t.Fatalf("ClearResumePosition(%s) = %v, %v; want cleared", id, cleared, err)
		}
	}

	for _, want := range []struct {
		id        string
		completed bool
	}{{"movie-1", true}, {"movie-2", false}} {
		progress, err := store.GetProgress(ctx, "profile-1", want.id)
		if err != nil || progress == nil {
			t.Fatalf("GetProgress(%s) = %+v, %v", want.id, progress, err)
		}
		if progress.PositionSeconds != 0 || progress.Completed != want.completed || progress.DurationSeconds != 7200 {
			t.Fatalf("%s progress = %+v, want position 0, completed %v, duration kept", want.id, progress, want.completed)
		}
	}
	inProgress, err := store.ListProgress(ctx, "profile-1", "in_progress", 10, 0)
	if err != nil || len(inProgress) != 0 {
		t.Fatalf("in-progress rows = %+v, %v; want none", inProgress, err)
	}
	after, err := store.ListHistory(ctx, "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("history after = %+v, want all %d earlier watches kept", after, len(before))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Completed != before[i].Completed {
			t.Fatalf("history row %d = %+v, want %+v", i, after[i], before[i])
		}
	}

	// Nothing left to clear: a no-op that reports so.
	if cleared, err := service.ClearResumePosition(ctx, 1, "profile-1", "movie-2"); err != nil || cleared {
		t.Fatalf("second ClearResumePosition = %v, %v; want a no-op", cleared, err)
	}
}

// A live write under a hidden-history watermark that lies ahead of the clock
// (a mark unwatched after a future-dated imported watch) is stamped just past
// the watermark and stays visible. Clearing that resume position must still
// write, not be skipped as hidden while reporting success.
func TestClearResumePositionUnderFutureHiddenWatermark(t *testing.T) {
	ctx := context.Background()
	store, db := newTestUserStore(t)
	t.Cleanup(func() { _ = db.Close() })
	createWatchstateProfile(t, store)

	hiddenBefore := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO hidden_history_items (profile_id, media_item_id, hidden_before, updated_at)
		VALUES (?, ?, ?, ?)`, "profile-1", "movie-1", hiddenBefore, hiddenBefore); err != nil {
		t.Fatalf("seed hidden watermark: %v", err)
	}
	if err := store.UpdateProgress(ctx, "profile-1", "movie-1", 1800, 7200, userstore.ProgressThresholds{}); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
	if progress, err := store.GetProgress(ctx, "profile-1", "movie-1"); err != nil || progress == nil || progress.PositionSeconds != 1800 {
		t.Fatalf("progress before = %+v, %v; want a visible resume point", progress, err)
	}

	cleared, err := NewService(testStoreProvider{store: store}).ClearResumePosition(ctx, 1, "profile-1", "movie-1")
	if err != nil || !cleared {
		t.Fatalf("ClearResumePosition = %v, %v; want cleared", cleared, err)
	}
	progress, err := store.GetProgress(ctx, "profile-1", "movie-1")
	if err != nil || progress == nil || progress.PositionSeconds != 0 {
		t.Fatalf("progress after = %+v, %v; want visible with position 0", progress, err)
	}
}

func TestClearResumePositionWithoutProgressWritesNothing(t *testing.T) {
	ctx := context.Background()
	store, db := newTestUserStore(t)
	t.Cleanup(func() { _ = db.Close() })
	createWatchstateProfile(t, store)

	cleared, err := NewService(testStoreProvider{store: store}).ClearResumePosition(ctx, 1, "profile-1", "movie-1")
	if err != nil || cleared {
		t.Fatalf("ClearResumePosition = %v, %v; want a no-op", cleared, err)
	}
	if progress, err := store.GetProgress(ctx, "profile-1", "movie-1"); err != nil || progress != nil {
		t.Fatalf("progress = %+v, %v; want no row", progress, err)
	}
}

// onlyHistoryEntry returns profile-1's single history row, failing unless
// there is exactly one.
func onlyHistoryEntry(t *testing.T, store userstore.UserStore) userstore.WatchHistoryEntry {
	t.Helper()
	history, err := store.ListHistory(context.Background(), "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	return history[0]
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

// batchTestEpisodeRepo is testEpisodeRepo plus the GetByIDs batch capability,
// so the bulk identity path is what gets exercised. It counts calls to prove
// a series mark no longer issues one lookup per episode.
type batchTestEpisodeRepo struct {
	testEpisodeRepo
	batchCalls int
}

func (r *batchTestEpisodeRepo) GetByIDs(_ context.Context, contentIDs []string) ([]*models.Episode, error) {
	r.batchCalls++
	found := make([]*models.Episode, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		if episode, ok := r.episodes[contentID]; ok {
			found = append(found, episode)
		}
	}
	return found, nil
}

// batchTestProviderIDRepo adds the GetByContentIDs batch capability and counts
// calls, pinning that a whole series resolves in one provider-ID query.
type batchTestProviderIDRepo struct {
	testProviderIDRepo
	batchCalls int
}

func (r *batchTestProviderIDRepo) GetByContentIDs(_ context.Context, contentIDs []string) (map[string][]*models.MediaItemProviderID, error) {
	r.batchCalls++
	out := make(map[string][]*models.MediaItemProviderID, len(contentIDs))
	for _, contentID := range contentIDs {
		if rows, ok := r.ids[contentID]; ok {
			out[contentID] = rows
		}
	}
	return out, nil
}

// TestManualMarkWatchedSeriesResolvesIdentitiesInBulk marks a whole series and
// pins both halves of the fix: every episode gets its own history row with a
// resolved identity, and the catalog lookups are batched rather than repeated
// per episode.
func TestManualMarkWatchedSeriesResolvesIdentitiesInBulk(t *testing.T) {
	store, db := newTestUserStore(t)
	t.Cleanup(func() { _ = db.Close() })

	const episodeCount = 25
	episodes := make(map[string]*models.Episode, episodeCount)
	targets := make([]LeafWatchTarget, 0, episodeCount)
	for i := 1; i <= episodeCount; i++ {
		contentID := fmt.Sprintf("episode-%d", i)
		episodes[contentID] = &models.Episode{
			ContentID:     contentID,
			SeriesID:      "series-1",
			SeasonNumber:  1,
			EpisodeNumber: i,
		}
		targets = append(targets, LeafWatchTarget{MediaItemID: contentID, DurationSeconds: 1800})
	}

	episodeRepo := &batchTestEpisodeRepo{testEpisodeRepo: testEpisodeRepo{episodes: episodes}}
	providerRepo := &batchTestProviderIDRepo{testProviderIDRepo: testProviderIDRepo{
		ids: map[string][]*models.MediaItemProviderID{
			"series-1": {{ContentID: "series-1", ItemType: "series", Provider: "tvdb", ProviderID: "765"}},
		},
	}}
	service := NewService(testStoreProvider{store: store}).WithStableIdentityResolver(
		NewStableIdentityResolver(testItemRepo{}, episodeRepo, providerRepo),
	)

	result, err := service.RecordManualMarkWatchedWithResult(
		context.Background(), 1, "profile-1", targets,
		time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("RecordManualMarkWatchedWithResult: %v", err)
	}
	if len(result.Entries) != episodeCount {
		t.Fatalf("result entries = %d, want %d; watch-provider dispatch reads these",
			len(result.Entries), episodeCount)
	}

	history, err := store.ListHistory(context.Background(), "profile-1", episodeCount*2, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != episodeCount {
		t.Fatalf("history len = %d, want %d", len(history), episodeCount)
	}
	for _, entry := range history {
		if entry.Identity.StableType != "episode" {
			t.Fatalf("%s identity = %q, want episode", entry.MediaItemID, entry.Identity.StableType)
		}
		if got := entry.Identity.SeriesProviderIDs["tvdb"]; got != "765" {
			t.Fatalf("%s SeriesProviderIDs[tvdb] = %q, want 765", entry.MediaItemID, got)
		}
	}
	for _, target := range targets {
		progress, err := store.GetProgress(context.Background(), "profile-1", target.MediaItemID)
		if err != nil || progress == nil || !progress.Completed {
			t.Fatalf("GetProgress(%s) = %+v (%v), want completed", target.MediaItemID, progress, err)
		}
	}

	// The whole point of the bulk path: constant lookups, not O(episodes).
	if episodeRepo.batchCalls != 1 {
		t.Fatalf("episode batch lookups = %d, want 1", episodeRepo.batchCalls)
	}
	if providerRepo.batchCalls != 1 {
		t.Fatalf("provider-ID batch lookups = %d, want 1", providerRepo.batchCalls)
	}
}

// TestManualMarkWatchedSeriesIsAtomicOnCancel is the regression test for the
// reported bug: marking a large series while the client disconnects used to
// leave the series partially watched, because each episode committed on its
// own. The mark must now be all-or-nothing.
func TestManualMarkWatchedSeriesIsAtomicOnCancel(t *testing.T) {
	store, db := newTestUserStore(t)
	t.Cleanup(func() { _ = db.Close() })

	targets := make([]LeafWatchTarget, 0, 40)
	for i := 1; i <= 40; i++ {
		targets = append(targets, LeafWatchTarget{
			MediaItemID:     fmt.Sprintf("episode-%d", i),
			DurationSeconds: 1800,
		})
	}

	service := NewService(testStoreProvider{store: store})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the tab closed before the write could run

	if _, err := service.RecordManualMarkWatchedWithResult(
		ctx, 1, "profile-1", targets, time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	); err == nil {
		t.Fatal("RecordManualMarkWatchedWithResult on a canceled context returned nil error")
	}

	history, err := store.ListHistory(context.Background(), "profile-1", 100, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("history len = %d, want 0 — a canceled series mark must roll back, not leave a partial mark", len(history))
	}
	for _, target := range targets {
		progress, err := store.GetProgress(context.Background(), "profile-1", target.MediaItemID)
		if err != nil {
			t.Fatalf("GetProgress(%s): %v", target.MediaItemID, err)
		}
		if progress != nil && progress.Completed {
			t.Fatalf("%s is completed after a canceled mark; the write must be all-or-nothing", target.MediaItemID)
		}
	}
}

func TestManualMarkWatchedSkipsAlreadyCompletedTargets(t *testing.T) {
	store, db := newTestUserStore(t)
	t.Cleanup(func() { _ = db.Close() })
	createWatchstateProfile(t, store)
	service := NewService(testStoreProvider{store: store})
	targets := []LeafWatchTarget{{MediaItemID: "movie-1", DurationSeconds: 7200}, {MediaItemID: "movie-2", DurationSeconds: 5400}}

	first, err := service.RecordManualMarkWatchedWithResult(context.Background(), 1, "profile-1", targets[:1], time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("first mark: %v", err)
	}
	if len(first.Entries) != 1 {
		t.Fatalf("first mark entries = %+v, want one", first.Entries)
	}
	// A series-style retry over both: only the unwatched leaf records.
	second, err := service.RecordManualMarkWatchedWithResult(context.Background(), 1, "profile-1", targets, time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second mark: %v", err)
	}
	if len(second.Entries) != 1 || second.Entries[0].MediaItemID != "movie-2" {
		t.Fatalf("second mark entries = %+v, want only movie-2", second.Entries)
	}
	history, err := store.ListHistory(context.Background(), "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history = %+v, want one row per leaf", history)
	}
}

// A completed item with a resume position is a rewatch in progress. A mark
// that records rewatches records another play and clears the position; a
// repeat, with no new position in between, records nothing. Any other mark
// leaves it alone.
func TestManualMarkWatchedRecordsRewatchInProgress(t *testing.T) {
	ctx := context.Background()
	store, db := newTestUserStore(t)
	t.Cleanup(func() { _ = db.Close() })
	createWatchstateProfile(t, store)
	service := NewService(testStoreProvider{store: store})
	target := []LeafWatchTarget{{MediaItemID: "movie-1", DurationSeconds: 7200}}
	rewatch := []LeafWatchTarget{{MediaItemID: "movie-1", DurationSeconds: 7200, RecordRewatch: true}}

	if err := service.RecordManualMarkWatched(ctx, 1, "profile-1", target, time.Now().UTC().Add(-48*time.Hour)); err != nil {
		t.Fatalf("first mark: %v", err)
	}
	if _, err := service.RecordPlaybackStop(ctx, 1, "profile-1", "movie-1", 7200, 1800, time.Now().UTC().Add(-time.Hour), userstore.VersionHints{}, userstore.ProgressThresholds{}); err != nil {
		t.Fatalf("RecordPlaybackStop: %v", err)
	}
	if progress, err := store.GetProgress(ctx, "profile-1", "movie-1"); err != nil || progress == nil || !progress.Completed || progress.PositionSeconds != 1800 {
		t.Fatalf("progress before = %+v, %v; want a watched item with a resume position", progress, err)
	}

	plain, err := service.RecordManualMarkWatchedWithResult(ctx, 1, "profile-1", target, time.Now().UTC())
	if err != nil || len(plain.Entries) != 0 {
		t.Fatalf("plain mark entries = %+v, %v; want none", plain.Entries, err)
	}
	recorded, err := service.RecordManualMarkWatchedWithResult(ctx, 1, "profile-1", rewatch, time.Now().UTC())
	if err != nil {
		t.Fatalf("rewatch mark: %v", err)
	}
	if len(recorded.Entries) != 1 || !recorded.Entries[0].Completed {
		t.Fatalf("rewatch mark entries = %+v, want one completed play", recorded.Entries)
	}
	progress, err := store.GetProgress(ctx, "profile-1", "movie-1")
	if err != nil || progress == nil || !progress.Completed || progress.PositionSeconds != 0 {
		t.Fatalf("progress after = %+v, %v; want watched with no resume position", progress, err)
	}

	repeat, err := service.RecordManualMarkWatchedWithResult(ctx, 1, "profile-1", rewatch, time.Now().UTC())
	if err != nil {
		t.Fatalf("repeat mark: %v", err)
	}
	if len(repeat.Entries) != 0 {
		t.Fatalf("repeat mark entries = %+v, want none", repeat.Entries)
	}
	history, err := store.ListHistory(ctx, "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	// The first mark, the partial rewatch play and the rewatch mark.
	if len(history) != 3 {
		t.Fatalf("history = %+v, want three rows", history)
	}
}
