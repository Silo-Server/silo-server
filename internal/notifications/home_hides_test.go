package notifications

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestHomeHidesContinueWatching(t *testing.T) {
	stamp := "2026-09-01T10:00:00Z"
	inProgress := []userstore.WatchProgress{{MediaItemID: "ep-2", PositionSeconds: 30, UpdatedAt: stamp}}
	dismissed := func(progressUpdatedAt string) catalog.HomeDismissalIndex {
		return catalog.NewHomeDismissalIndex([]userstore.HomeItemDismissal{{
			Surface: userstore.HomeSurfaceContinueWatching, MediaItemID: "ep-2", ProgressUpdatedAt: &progressUpdatedAt,
		}})
	}

	for _, tc := range []struct {
		name       string
		hides      homeHides
		inProgress []userstore.WatchProgress
		want       bool
	}{
		{name: "nothing in progress", want: false},
		{name: "in progress, not removed", inProgress: inProgress, want: true},
		{name: "removed at the current progress", hides: homeHides{continueWatching: dismissed(stamp)}, inProgress: inProgress, want: false},
		{name: "resumed after removal", hides: homeHides{continueWatching: dismissed("2026-08-01T10:00:00Z")}, inProgress: inProgress, want: true},
		{name: "series dropped", hides: homeHides{dropped: true}, inProgress: inProgress, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.hides.continueWatchingVisible(tc.inProgress); got != tc.want {
				t.Fatalf("continueWatchingVisible = %v, want %v", got, tc.want)
			}
		})
	}
}

type fakeDroppedSeries []catalog.DroppedSeries

func (f fakeDroppedSeries) ListDropped(_ context.Context, _ int, _ string, seriesIDs []string) ([]catalog.DroppedSeries, error) {
	var out []catalog.DroppedSeries
	for _, drop := range f {
		for _, id := range seriesIDs {
			if drop.SeriesID == id {
				out = append(out, drop)
			}
		}
	}
	return out, nil
}

func newSQLiteUserStore(t *testing.T) userstore.UserStore {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	return store
}

// countingDismissalStore counts dismissal list reads.
type countingDismissalStore struct {
	userstore.UserStore
	reads map[string]int
}

func (s *countingDismissalStore) ListHomeDismissals(ctx context.Context, profileID, surface string) ([]userstore.HomeItemDismissal, error) {
	s.reads[surface]++
	return s.UserStore.ListHomeDismissals(ctx, profileID, surface)
}

// itemReaderStore adds the targeted dismissal read, as the Postgres store has.
type itemReaderStore struct {
	*countingDismissalStore
	itemReads int
}

func (s *itemReaderStore) ListHomeDismissalsForItems(ctx context.Context, profileID, surface string, ids []string) ([]userstore.HomeItemDismissal, error) {
	s.itemReads++
	all, err := s.UserStore.ListHomeDismissals(ctx, profileID, surface)
	if err != nil {
		return nil, err
	}
	var out []userstore.HomeItemDismissal
	for _, dismissal := range all {
		if slices.Contains(ids, dismissal.MediaItemID) {
			out = append(out, dismissal)
		}
	}
	return out, nil
}

func TestLoadHomeHides(t *testing.T) {
	ctx := context.Background()
	store := &countingDismissalStore{UserStore: newSQLiteUserStore(t), reads: map[string]int{}}
	seriesID := "series-a"
	otherSeries := "series-b"
	stamp := "2026-09-01T10:00:00Z"
	for _, dismissal := range []userstore.HomeItemDismissal{
		{ProfileID: "p1", Surface: userstore.HomeSurfaceNextUp, MediaItemID: "a-e3", SeriesID: &seriesID, DismissedAt: stamp},
		{ProfileID: "p1", Surface: userstore.HomeSurfaceNextUp, MediaItemID: "b-e2", SeriesID: &otherSeries, DismissedAt: stamp},
		{ProfileID: "p1", Surface: userstore.HomeSurfaceContinueWatching, MediaItemID: "a-e2", ProgressUpdatedAt: &stamp, DismissedAt: stamp},
	} {
		if err := store.UpsertHomeDismissal(ctx, dismissal); err != nil {
			t.Fatalf("UpsertHomeDismissal: %v", err)
		}
	}
	seriesA := []string{"a-e1", "a-e2", "a-e3"}

	updater := &InterestUpdater{drops: fakeDroppedSeries{{SeriesID: seriesID, Active: false}}}
	hides, err := updater.loadHomeHides(ctx, store, 1, "p1", seriesID, []string{"a-e2"}, seriesA)
	if err != nil {
		t.Fatalf("loadHomeHides: %v", err)
	}
	if hides.dropped {
		t.Fatal("an inactive drop must not hide the series")
	}
	// Without a targeted reader the full list is read and cut to the series.
	if _, ok := hides.nextUp["a-e3"]; len(hides.nextUp) != 1 || !ok {
		t.Fatalf("nextUp = %v, want only a-e3", hides.nextUp)
	}
	if hides.continueWatchingVisible([]userstore.WatchProgress{{MediaItemID: "a-e2", PositionSeconds: 30, UpdatedAt: stamp}}) {
		t.Fatal("the Continue Watching dismissal did not load")
	}

	// A surface with no items to check is not read.
	store.reads = map[string]int{}
	if _, err := updater.loadHomeHides(ctx, store, 1, "p1", seriesID, nil, nil); err != nil {
		t.Fatalf("loadHomeHides: %v", err)
	}
	if len(store.reads) != 0 {
		t.Fatalf("read dismissals %v with no items to check", store.reads)
	}

	// A store with the targeted reader never lists a whole surface.
	store.reads = map[string]int{}
	targeted := &itemReaderStore{countingDismissalStore: store}
	hides, err = updater.loadHomeHides(ctx, targeted, 1, "p1", seriesID, []string{"a-e2"}, seriesA)
	if err != nil {
		t.Fatalf("loadHomeHides: %v", err)
	}
	if targeted.itemReads != 2 || len(store.reads) != 0 {
		t.Fatalf("targeted reads = %d, full reads = %v; want 2 and none", targeted.itemReads, store.reads)
	}
	if _, ok := hides.nextUp["a-e3"]; len(hides.nextUp) != 1 || !ok {
		t.Fatalf("nextUp = %v, want only a-e3", hides.nextUp)
	}

	updater.drops = fakeDroppedSeries{{SeriesID: seriesID, Active: true}}
	hides, err = updater.loadHomeHides(ctx, store, 1, "p1", seriesID, []string{"a-e2"}, seriesA)
	if err != nil {
		t.Fatalf("loadHomeHides: %v", err)
	}
	if !hides.dropped {
		t.Fatal("an active drop must hide the series")
	}
}

// Wiring without a notification system passes a nil updater; queuing must
// stay a no-op rather than dereference it.
func TestQueueingWithoutNotificationsIsANoOp(t *testing.T) {
	var updater *InterestUpdater
	updater.queueHomeChange(1, "p1", "series-a")
	updater.QueueItemMutation(1, "p1", "series-a")
	if tracker := TrackDroppedSeries(nil, nil); tracker.updater != nil {
		t.Fatal("a tracker without a notification system has an updater")
	}
}

func TestInterestTrackingStoreQueuesHomeDismissals(t *testing.T) {
	ctx := context.Background()
	updater := &InterestUpdater{pending: map[interestMutation]int{}}
	store := &interestTrackingStore{UserStore: newSQLiteUserStore(t), userID: 1, system: &System{}, updater: updater}
	progressStamp := "2026-09-01T10:00:00Z"

	if err := store.UpsertHomeDismissal(ctx, userstore.HomeItemDismissal{
		ProfileID: "p1", Surface: userstore.HomeSurfaceContinueWatching, MediaItemID: "ep-1",
		ProgressUpdatedAt: &progressStamp, DismissedAt: progressStamp,
	}); err != nil {
		t.Fatalf("UpsertHomeDismissal: %v", err)
	}
	if err := store.DeleteHomeDismissal(ctx, "p1", userstore.HomeSurfaceNextUp, "ep-2"); err != nil {
		t.Fatalf("DeleteHomeDismissal: %v", err)
	}

	updater.mu.Lock()
	defer updater.mu.Unlock()
	for _, itemID := range []string{"ep-1", "ep-2"} {
		if _, ok := updater.pending[interestMutation{userID: 1, profileID: "p1", itemID: itemID}]; !ok {
			t.Errorf("no interest mutation queued for %s; a Home removal would not reach notifications", itemID)
		}
	}
	// Removals and restores are rechecked after the session gap.
	for _, itemID := range []string{"ep-1", "ep-2"} {
		if _, ok := updater.deferred[interestMutation{userID: 1, profileID: "p1", itemID: itemID}]; !ok {
			t.Errorf("no second recompute deferred for %s", itemID)
		}
	}
}

func TestPromoteDeferredMovesOnlyDueMutations(t *testing.T) {
	now := time.Now()
	due := interestMutation{userID: 1, profileID: "p1", itemID: "due"}
	later := interestMutation{userID: 1, profileID: "p1", itemID: "later"}
	updater := &InterestUpdater{
		pending:  map[interestMutation]int{},
		deferred: map[interestMutation]time.Time{due: now.Add(-time.Second), later: now.Add(time.Minute)},
	}
	updater.promoteDeferred(now)
	if _, ok := updater.pending[due]; !ok {
		t.Error("a due deferred mutation was not queued")
	}
	if _, ok := updater.pending[later]; ok {
		t.Error("a deferred mutation was queued before it was due")
	}
	if _, ok := updater.deferred[later]; !ok || len(updater.deferred) != 1 {
		t.Errorf("deferred = %v, want only the later mutation", updater.deferred)
	}
}

func TestInterestTrackingStoreQueuesNewWatchSessions(t *testing.T) {
	ctx := context.Background()
	updater := &InterestUpdater{pending: map[interestMutation]int{}}
	store := &interestTrackingStore{UserStore: newSQLiteUserStore(t), userID: 1, system: &System{}, updater: updater}
	thresholds := userstore.ProgressThresholds{}
	queued := func() bool {
		updater.mu.Lock()
		defer updater.mu.Unlock()
		_, ok := updater.pending[interestMutation{userID: 1, profileID: "p1", itemID: "ep-1"}]
		clear(updater.pending)
		return ok
	}

	lastWatched := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := store.SetProgressAt(ctx, "p1", "ep-1", 30, 100, false, lastWatched); err != nil {
		t.Fatalf("SetProgressAt: %v", err)
	}
	queued()

	// Resuming an hour later changes no state but starts a watch session,
	// which can lift a Home removal.
	if err := store.UpdateProgress(ctx, "p1", "ep-1", 40, 100, thresholds); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
	if !queued() {
		t.Fatal("resuming after a pause queued no interest recompute")
	}

	// The next tick of the same session is free.
	if err := store.UpdateProgress(ctx, "p1", "ep-1", 50, 100, thresholds); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
	if queued() {
		t.Fatal("a playback tick within one session queued an interest recompute")
	}
}

func TestInterestTrackingStoreQueuesLateImports(t *testing.T) {
	ctx := context.Background()
	updater := &InterestUpdater{pending: map[interestMutation]int{}}
	store := &interestTrackingStore{UserStore: newSQLiteUserStore(t), userID: 1, system: &System{}, updater: updater}
	mutation := interestMutation{userID: 1, profileID: "p1", itemID: "ep-1"}
	queued := func() bool {
		_, ok := updater.pending[mutation]
		clear(updater.pending)
		return ok
	}

	stamp := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := store.SetProgressAt(ctx, "p1", "ep-1", 30, 100, false, stamp); err != nil {
		t.Fatalf("SetProgressAt: %v", err)
	}
	queued()

	// A late import stamped a few minutes after the stored row keeps it in
	// progress but moves the stamp a Continue Watching dismissal may hold for.
	if applied, err := store.SetProgressIfNewer(ctx, "p1", "ep-1", 35, 100, false, stamp.Add(3*time.Minute)); err != nil || !applied {
		t.Fatalf("SetProgressIfNewer = %v, %v", applied, err)
	}
	if !queued() {
		t.Fatal("a late import queued no interest recompute")
	}

	// A client that stamps its live ticks stays free within a session.
	now := time.Now().UTC().Truncate(time.Second)
	if applied, err := store.SetProgressIfNewer(ctx, "p1", "ep-1", 40, 100, false, now.Add(-20*time.Second)); err != nil || !applied {
		t.Fatalf("SetProgressIfNewer = %v, %v", applied, err)
	}
	queued() // the first tick of this session may queue
	if applied, err := store.SetProgressIfNewer(ctx, "p1", "ep-1", 45, 100, false, now); err != nil || !applied {
		t.Fatalf("SetProgressIfNewer = %v, %v", applied, err)
	}
	if queued() {
		t.Fatal("a stamped playback tick within one session queued an interest recompute")
	}

	// An import older than the stored row is not applied and queues nothing.
	if applied, err := store.SetProgressIfNewer(ctx, "p1", "ep-1", 20, 100, false, stamp); err != nil || applied {
		t.Fatalf("SetProgressIfNewer (older) = %v, %v", applied, err)
	}
	if queued() {
		t.Fatal("an import that did not apply queued an interest recompute")
	}

	// Completion is sticky, so stamped rewatch ticks of a finished episode
	// change no state.
	if applied, err := store.SetProgressIfNewer(ctx, "p1", "ep-1", 100, 100, true, now.Add(time.Second)); err != nil || !applied {
		t.Fatalf("SetProgressIfNewer (complete) = %v, %v", applied, err)
	}
	queued()
	if applied, err := store.SetProgressIfNewer(ctx, "p1", "ep-1", 10, 100, false, now.Add(2*time.Second)); err != nil || !applied {
		t.Fatalf("SetProgressIfNewer (rewatch) = %v, %v", applied, err)
	}
	if queued() {
		t.Fatal("a stamped rewatch tick of a completed episode queued an interest recompute")
	}
}

func TestInterestTrackingStoreQueuesFirstWriteAfterHomeChange(t *testing.T) {
	ctx := context.Background()
	updater := &InterestUpdater{pending: map[interestMutation]int{}}
	store := &interestTrackingStore{UserStore: newSQLiteUserStore(t), userID: 1, system: &System{}, updater: updater}
	mutation := interestMutation{userID: 1, profileID: "p1", itemID: "ep-1"}
	queued := func() bool {
		updater.mu.Lock()
		defer updater.mu.Unlock()
		_, ok := updater.pending[mutation]
		clear(updater.pending)
		return ok
	}
	thresholds := userstore.ProgressThresholds{}

	// Playback a minute ago, then the card is dismissed.
	if err := store.SetProgressAt(ctx, "p1", "ep-1", 30, 100, false, time.Now().Add(-time.Minute).UTC().Truncate(time.Second)); err != nil {
		t.Fatalf("SetProgressAt: %v", err)
	}
	stamp := "unused"
	if err := store.UpsertHomeDismissal(ctx, userstore.HomeItemDismissal{
		ProfileID: "p1", Surface: userstore.HomeSurfaceContinueWatching, MediaItemID: "ep-2",
		ProgressUpdatedAt: &stamp, DismissedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("UpsertHomeDismissal: %v", err)
	}
	queued()

	// Resuming inside the session gap is the row's first write since the
	// dismissal, so it queues at once.
	if err := store.UpdateProgress(ctx, "p1", "ep-1", 40, 100, thresholds); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
	if !queued() {
		t.Fatal("resuming right after a Home change queued no interest recompute")
	}
	// The next tick is newer than the change and stays free. Progress stamps
	// are whole seconds, so date the change a second back to keep the next
	// tick's row stamp after it.
	updater.noteHomeChange(1, "p1", time.Now().Add(-2*time.Second))
	if err := store.UpdateProgress(ctx, "p1", "ep-1", 50, 100, thresholds); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
	if queued() {
		t.Fatal("a tick after the first post-change write queued an interest recompute")
	}
}

func TestSupersededInProgress(t *testing.T) {
	keys := map[string]int{"e2": EpisodeKey(1, 2), "e3": EpisodeKey(1, 3), "e5": EpisodeKey(1, 5)}
	at := func(minutes int) time.Time { return time.Date(2026, 9, 1, 10, minutes, 0, 0, time.UTC) }
	inProgress := []userstore.WatchProgress{
		{MediaItemID: "e2", PositionSeconds: 30, UpdatedAt: at(0).Format(time.RFC3339)},
		{MediaItemID: "e3", PositionSeconds: 30, UpdatedAt: at(20).Format(time.RFC3339)},
	}
	// E5 was completed after E2's progress but before E3's.
	completed := []completedEpisode{{key: EpisodeKey(1, 5), at: at(10)}}

	got := supersededInProgress(inProgress, completed, keys)
	if _, ok := got["e2"]; !ok || len(got) != 1 {
		t.Fatalf("superseded = %v, want only e2", got)
	}
}

type allLibrariesScope struct{}

func (allLibrariesScope) Resolve(_ context.Context, input access.ResolveInput) (access.Scope, error) {
	return access.Scope{UserID: input.UserID, ProfileID: input.ProfileID}, nil
}

// TestRecomputeSeriesFollowsHomeRemovalsPostgres walks one profile's interest
// in one series through each Home removal against the production stores:
// per-card Continue Watching and Next Up dismissals, then a series drop made
// through the tracker the dismissal handler uses.
func TestRecomputeSeriesFollowsHomeRemovalsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	nonce := time.Now().UnixNano()
	prefix := fmt.Sprintf("home-hides-%d", nonce)
	libraryID := 900000 + int(nonce%90000)
	seriesID := prefix + "-series"
	// E3 has no file, so Home's Next Up skips it.
	episodes := []string{prefix + "-e1", prefix + "-e2", prefix + "-e3", prefix + "-e4"}
	const profileID = "profile-home-hides"

	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`,
		prefix, prefix+"@example.test",
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		if _, err := pool.Exec(cleanup, `DELETE FROM profile_series_interest WHERE series_id = $1`, seriesID); err != nil {
			t.Errorf("clean up: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_files WHERE media_folder_id = $1`, libraryID); err != nil {
			t.Errorf("clean up: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, seriesID); err != nil {
			t.Errorf("clean up: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, libraryID); err != nil {
			t.Errorf("clean up: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Errorf("clean up: %v", err)
		}
	})
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_folders (id, type, name) VALUES ($1, 'series', $2)`, []any{libraryID, prefix}},
		{`INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'series', 'Series', '{}')`, []any{seriesID}},
		{`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, []any{seriesID, libraryID}},
		{`INSERT INTO seasons (content_id, series_id, season_number) VALUES ($1 || '-s1', $1, 1)`, []any{seriesID}},
		{`INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title)
			VALUES ($1, $5, $5 || '-s1', 1, 1, 'E1'), ($2, $5, $5 || '-s1', 1, 2, 'E2'),
			       ($3, $5, $5 || '-s1', 1, 3, 'E3'), ($4, $5, $5 || '-s1', 1, 4, 'E4')`,
			[]any{episodes[0], episodes[1], episodes[2], episodes[3], seriesID}},
		{`INSERT INTO media_files (id, media_folder_id, file_path, episode_id) VALUES
			($1, $2, $3 || '/e1.mkv', $4), ($1 + 1, $2, $3 || '/e2.mkv', $5), ($1 + 2, $2, $3 || '/e4.mkv', $6)`,
			[]any{nonce % 1_000_000_000_000, libraryID, "/" + prefix, episodes[0], episodes[1], episodes[3]}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed catalog: %v", err)
		}
	}

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Viewer"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	watchedAt := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	setProgress := func(episodeID string, completed bool, at time.Time) {
		t.Helper()
		position := 30.0
		if completed {
			position = 100
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
			VALUES ($1, $2, $3, $4, 100, $5, $6)
			ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE
			SET position_seconds = EXCLUDED.position_seconds, completed = EXCLUDED.completed, updated_at = EXCLUDED.updated_at`,
			userID, profileID, episodeID, position, completed, at); err != nil {
			t.Fatalf("seed progress: %v", err)
		}
	}
	setProgress(episodes[0], true, watchedAt)
	setProgress(episodes[1], false, watchedAt)
	if err := store.AddFavorite(ctx, profileID, seriesID); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}

	system := &System{}
	updater := NewInterestUpdater(pool, NewInterestRepository(pool), provider, allLibrariesScope{})
	system.Interest = updater

	type flags struct{ favorite, continueWatching, nextUp bool }
	recompute := func(stage string, want flags, wantNextExpected int) {
		t.Helper()
		if err := updater.RecomputeSeries(ctx, userID, profileID, seriesID); err != nil {
			t.Fatalf("%s: RecomputeSeries: %v", stage, err)
		}
		var got flags
		var nextExpected *int
		if err := pool.QueryRow(ctx, `
			SELECT favorite, continue_watching, next_up_candidate, next_expected_episode_key
			FROM profile_series_interest
			WHERE profile_id = $1 AND library_id = $2 AND series_id = $3`,
			profileID, libraryID, seriesID,
		).Scan(&got.favorite, &got.continueWatching, &got.nextUp, &nextExpected); err != nil {
			t.Fatalf("%s: read interest: %v", stage, err)
		}
		if got != want {
			t.Fatalf("%s: interest = %+v, want %+v", stage, got, want)
		}
		if nextExpected == nil || *nextExpected != wantNextExpected {
			t.Fatalf("%s: next_expected_episode_key = %v, want %d", stage, nextExpected, wantNextExpected)
		}
	}
	requireQueued := func(action string) {
		t.Helper()
		updater.mu.Lock()
		defer updater.mu.Unlock()
		if _, ok := updater.pending[interestMutation{userID: userID, profileID: profileID, itemID: seriesID}]; !ok {
			t.Fatalf("%s queued no interest recompute", action)
		}
		clear(updater.pending)
	}
	afterE1, afterE2, afterE4 := EpisodeKey(1, 2), EpisodeKey(1, 3), EpisodeKey(1, 5)

	recompute("before any removal", flags{favorite: true, continueWatching: true, nextUp: true}, afterE1)

	progress, err := store.ListProgressByMediaItems(ctx, profileID, []string{episodes[1]})
	if err != nil {
		t.Fatalf("ListProgressByMediaItems: %v", err)
	}
	progressStamp := progress[episodes[1]].UpdatedAt
	dismissedAt := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if err := store.UpsertHomeDismissal(ctx, userstore.HomeItemDismissal{
		ProfileID: profileID, Surface: userstore.HomeSurfaceContinueWatching, MediaItemID: episodes[1],
		ProgressUpdatedAt: &progressStamp, DismissedAt: dismissedAt,
	}); err != nil {
		t.Fatalf("dismiss from Continue Watching: %v", err)
	}
	recompute("removed from Continue Watching", flags{favorite: true, nextUp: true}, afterE1)

	// Home's Next Up card after E1 skips the started E2 and the file-less E3,
	// so it shows E4.
	if err := store.UpsertHomeDismissal(ctx, userstore.HomeItemDismissal{
		ProfileID: profileID, Surface: userstore.HomeSurfaceNextUp, MediaItemID: episodes[3],
		SeriesID: &seriesID, DismissedAt: dismissedAt,
	}); err != nil {
		t.Fatalf("dismiss from Next Up: %v", err)
	}
	recompute("removed from Next Up too", flags{favorite: true}, afterE1)

	// Resuming E2 brings its Continue Watching card back; the Next Up card is
	// still E4, so it stays dismissed.
	setProgress(episodes[1], false, time.Now().Add(-3*time.Minute).UTC().Truncate(time.Second))
	recompute("resumed E2", flags{favorite: true, continueWatching: true}, afterE1)

	// Finishing E2 leaves E4 as the Next Up card.
	setProgress(episodes[1], true, time.Now().Add(-2*time.Minute).UTC().Truncate(time.Second))
	recompute("finished E2", flags{favorite: true}, afterE2)

	// Finishing E4 moves Next Up past the dismissed card.
	setProgress(episodes[3], true, time.Now().Add(-time.Minute).UTC().Truncate(time.Second))
	recompute("finished E4", flags{favorite: true, nextUp: true}, afterE4)

	drops := TrackDroppedSeries(catalog.NewDroppedSeriesRepo(pool), system)
	if err := drops.Drop(ctx, userID, profileID, seriesID); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	requireQueued("dropping the series")
	updater.mu.Lock()
	_, deferred := updater.deferred[interestMutation{userID: userID, profileID: profileID, itemID: seriesID}]
	updater.mu.Unlock()
	if !deferred {
		t.Fatal("dropping the series deferred no second recompute")
	}
	recompute("series dropped", flags{favorite: true}, afterE4)

	if err := drops.Undrop(ctx, userID, profileID, seriesID); err != nil {
		t.Fatalf("Undrop: %v", err)
	}
	requireQueued("undropping the series")
	recompute("series undropped", flags{favorite: true, nextUp: true}, afterE4)

	// Drops imported from, and removed by, a watch provider.
	droppedAt := time.Now().UTC().Truncate(time.Microsecond)
	if applied, err := drops.ImportDrop(ctx, userID, profileID, seriesID, droppedAt, nil); err != nil || !applied {
		t.Fatalf("ImportDrop = %v, %v", applied, err)
	}
	requireQueued("importing a drop")
	recompute("drop imported", flags{favorite: true}, afterE4)

	if removed, err := drops.DeleteIfUnchanged(ctx, userID, profileID, seriesID, droppedAt); err != nil || !removed {
		t.Fatalf("DeleteIfUnchanged = %v, %v", removed, err)
	}
	requireQueued("removing an imported drop")
	recompute("imported drop removed", flags{favorite: true, nextUp: true}, afterE4)

	// Home anchors Next Up on the most recently completed episode. E3 gets a
	// file and the profile rewatches E1, so Home's card becomes E3 even though
	// E4 is the highest episode watched.
	if _, err := pool.Exec(ctx, `INSERT INTO media_files (id, media_folder_id, file_path, episode_id) VALUES ($1, $2, $3 || '/e3.mkv', $4)`,
		nonce%1_000_000_000_000+3, libraryID, "/"+prefix, episodes[2]); err != nil {
		t.Fatalf("add E3 file: %v", err)
	}
	setProgress(episodes[0], true, time.Now().UTC().Truncate(time.Second))
	recompute("Next Up moved to E3 despite the old E4 dismissal", flags{favorite: true, nextUp: true}, afterE4)
	if err := store.UpsertHomeDismissal(ctx, userstore.HomeItemDismissal{
		ProfileID: profileID, Surface: userstore.HomeSurfaceNextUp, MediaItemID: episodes[2],
		SeriesID: &seriesID, DismissedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("dismiss E3 from Next Up: %v", err)
	}
	recompute("E3 card dismissed after a rewatch", flags{favorite: true}, afterE4)

	// Home's Next Up counts any progress row as started, even one a history
	// hide covers. Starting E3 and hiding it leaves no card after E1.
	startedAt := time.Now().Add(-30 * time.Second).UTC().Truncate(time.Second)
	setProgress(episodes[2], false, startedAt)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before, updated_at)
		VALUES ($1, $2, $3, $4, $4)`, userID, profileID, episodes[2], startedAt.Add(time.Second)); err != nil {
		t.Fatalf("hide E3: %v", err)
	}
	recompute("E3 started and hidden", flags{favorite: true, nextUp: true}, afterE4)
}

// TestNextUpEpisodeSkipsStoreStartedEpisodes covers profiles whose progress
// lives outside Postgres: episodes their store reports as started are skipped
// as well.
func TestNextUpEpisodeSkipsStoreStartedEpisodes(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	nonce := time.Now().UnixNano()
	prefix := fmt.Sprintf("next-up-%d", nonce)
	libraryID := 910000 + int(nonce%80000)
	seriesID := prefix + "-series"
	e1, e2, e3 := prefix+"-e1", prefix+"-e2", prefix+"-e3"
	t.Cleanup(func() {
		cleanup := context.Background()
		if _, err := pool.Exec(cleanup, `DELETE FROM media_files WHERE media_folder_id = $1`, libraryID); err != nil {
			t.Errorf("clean up: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, seriesID); err != nil {
			t.Errorf("clean up: %v", err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, libraryID); err != nil {
			t.Errorf("clean up: %v", err)
		}
	})
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_folders (id, type, name) VALUES ($1, 'series', $2)`, []any{libraryID, prefix}},
		{`INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'series', 'Series', '{}')`, []any{seriesID}},
		{`INSERT INTO seasons (content_id, series_id, season_number) VALUES ($1 || '-s1', $1, 1)`, []any{seriesID}},
		{`INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title)
			VALUES ($1, $4, $4 || '-s1', 1, 1, 'E1'), ($2, $4, $4 || '-s1', 1, 2, 'E2'), ($3, $4, $4 || '-s1', 1, 3, 'E3')`,
			[]any{e1, e2, e3, seriesID}},
		{`INSERT INTO media_files (id, media_folder_id, file_path, episode_id) VALUES
			($1, $2, $3 || '/1.mkv', $4), ($1 + 1, $2, $3 || '/2.mkv', $5), ($1 + 2, $2, $3 || '/3.mkv', $6)`,
			[]any{nonce % 1_000_000_000_000, libraryID, "/" + prefix, e1, e2, e3}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	updater := &InterestUpdater{pool: pool}
	for _, tc := range []struct {
		started []string
		want    string
	}{
		{started: []string{}, want: e1},
		{started: []string{e1, e2}, want: e3},
		{started: []string{e1, e2, e3}, want: ""},
	} {
		got, err := updater.nextUpEpisode(ctx, 1, "no-postgres-progress", seriesID, EpisodeKey(1, 1), tc.started)
		if err != nil {
			t.Fatalf("nextUpEpisode: %v", err)
		}
		if got != tc.want {
			t.Errorf("nextUpEpisode(started=%v) = %q, want %q", tc.started, got, tc.want)
		}
	}
}
