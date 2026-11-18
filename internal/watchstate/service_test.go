package watchstate

import (
	"context"
	"database/sql"
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

// TestJellycompatDatedMarkPlayedBoundsFutureDate covers the datePlayed path;
// jellycompat's TestUserDataDateBounds covers LastPlayedDate.
func TestJellycompatDatedMarkPlayedBoundsFutureDate(t *testing.T) {
	store, db := newTestUserStore(t)
	t.Cleanup(func() { _ = db.Close() })
	createWatchstateProfile(t, store)

	before := time.Now().UTC().Add(-time.Second)
	service := NewService(testStoreProvider{store: store})
	if err := service.RecordJellycompatMarkPlayedBatch(t.Context(), 1, "profile-1", []string{"movie-1"}, before.Add(24*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC().Add(time.Second)

	reader := store.(interface {
		ListJellycompatProgressDates(context.Context, string, []string) (map[string]string, error)
	})
	dates, err := reader.ListJellycompatProgressDates(t.Context(), "profile-1", []string{"movie-1"})
	if err != nil {
		t.Fatal(err)
	}
	eventAt, err := time.Parse(time.RFC3339Nano, dates["movie-1"])
	if err != nil {
		t.Fatal(err)
	}
	if eventAt.Before(before) || eventAt.After(after) {
		t.Fatalf("future event time was not bounded to server time: %s", eventAt)
	}
	history, err := store.ListHistory(t.Context(), "profile-1", 10, 0)
	if err != nil || len(history) != 1 || history[0].WatchedAt != eventAt.Truncate(time.Second).Format(time.RFC3339) {
		t.Fatalf("history and progress dates disagree: %+v %v", history, err)
	}
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
