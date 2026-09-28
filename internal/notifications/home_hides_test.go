package notifications

import (
	"context"
	"database/sql"
	"fmt"
	"os"
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

func TestHomeHidesNextUp(t *testing.T) {
	dismissedE3 := map[string]struct{}{"e3": {}}
	for _, tc := range []struct {
		name  string
		hides homeHides
		next  string
		want  bool
	}{
		{name: "not removed", next: "e3", want: true},
		{name: "dismissed episode still next", hides: homeHides{nextUp: dismissedE3}, next: "e3", want: false},
		{name: "Next Up moved on", hides: homeHides{nextUp: dismissedE3}, next: "e4", want: true},
		{name: "caught up", hides: homeHides{nextUp: dismissedE3}, next: "", want: true},
		{name: "series dropped", hides: homeHides{dropped: true}, next: "e3", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.hides.nextUpVisible(tc.next); got != tc.want {
				t.Fatalf("nextUpVisible = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNextEpisodeAt(t *testing.T) {
	keys := map[string]int{"s1e1": EpisodeKey(1, 1), "s1e2": EpisodeKey(1, 2), "s2e1": EpisodeKey(2, 1)}
	key := func(season, episode int) *int {
		k := EpisodeKey(season, episode)
		return &k
	}
	for _, tc := range []struct {
		name string
		next *int
		want string
	}{
		{name: "no cursor", next: nil, want: ""},
		{name: "within a season", next: key(1, 2), want: "s1e2"},
		{name: "across a season break", next: key(1, 3), want: "s2e1"},
		{name: "caught up", next: key(2, 2), want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextEpisodeAt(keys, tc.next); got != tc.want {
				t.Fatalf("nextEpisodeAt = %q, want %q", got, tc.want)
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

func TestLoadHomeHidesScopesDismissalsToTheSeries(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteUserStore(t)
	seriesID := "series-a"
	otherSeries := "series-b"
	dismissals := []userstore.HomeItemDismissal{
		{ProfileID: "p1", Surface: userstore.HomeSurfaceNextUp, MediaItemID: "a-e3", SeriesID: &seriesID, DismissedAt: "2026-09-01T10:00:00Z"},
		{ProfileID: "p1", Surface: userstore.HomeSurfaceNextUp, MediaItemID: "b-e2", SeriesID: &otherSeries, DismissedAt: "2026-09-02T10:00:00Z"},
		// Next Up cards are episodes; a dismissal naming the series matches no card.
		{ProfileID: "p1", Surface: userstore.HomeSurfaceNextUp, MediaItemID: seriesID, SeriesID: &seriesID, DismissedAt: "2026-09-03T10:00:00Z"},
	}
	for _, dismissal := range dismissals {
		if err := store.UpsertHomeDismissal(ctx, dismissal); err != nil {
			t.Fatalf("UpsertHomeDismissal: %v", err)
		}
	}
	episodeKeys := map[string]int{"a-e1": EpisodeKey(1, 1), "a-e2": EpisodeKey(1, 2), "a-e3": EpisodeKey(1, 3)}

	updater := &InterestUpdater{drops: fakeDroppedSeries{{SeriesID: seriesID, Active: false}}}
	hides, err := updater.loadHomeHides(ctx, store, 1, "p1", seriesID, episodeKeys, true, true)
	if err != nil {
		t.Fatalf("loadHomeHides: %v", err)
	}
	if hides.dropped {
		t.Fatal("an inactive drop must not hide the series")
	}
	if _, ok := hides.nextUp["a-e3"]; len(hides.nextUp) != 1 || !ok {
		t.Fatalf("nextUp = %v, want only a-e3", hides.nextUp)
	}

	hides, err = updater.loadHomeHides(ctx, store, 1, "p1", seriesID, episodeKeys, true, false)
	if err != nil {
		t.Fatalf("loadHomeHides: %v", err)
	}
	if len(hides.nextUp) != 0 {
		t.Fatalf("nextUp = %v, want none when there is no progression cursor", hides.nextUp)
	}

	updater.drops = fakeDroppedSeries{{SeriesID: seriesID, Active: true}}
	hides, err = updater.loadHomeHides(ctx, store, 1, "p1", seriesID, episodeKeys, true, true)
	if err != nil {
		t.Fatalf("loadHomeHides: %v", err)
	}
	if !hides.dropped {
		t.Fatal("an active drop must hide the series")
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
	// A removal is rechecked after the session gap, in case playback resumes
	// sooner; restoring a card needs no recheck.
	if _, ok := updater.deferred[interestMutation{userID: 1, profileID: "p1", itemID: "ep-1"}]; !ok {
		t.Error("dismissing a card deferred no second recompute")
	}
	if _, ok := updater.deferred[interestMutation{userID: 1, profileID: "p1", itemID: "ep-2"}]; ok {
		t.Error("restoring a card deferred a recompute")
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
	episodes := []string{prefix + "-e1", prefix + "-e2", prefix + "-e3"}
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
		_, _ = pool.Exec(cleanup, `DELETE FROM profile_series_interest WHERE series_id = $1`, seriesID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, libraryID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
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
			VALUES ($1, $4, $4 || '-s1', 1, 1, 'E1'), ($2, $4, $4 || '-s1', 1, 2, 'E2'), ($3, $4, $4 || '-s1', 1, 3, 'E3')`,
			[]any{episodes[0], episodes[1], episodes[2], seriesID}},
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
	afterE1, afterE2 := EpisodeKey(1, 2), EpisodeKey(1, 3)

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

	if err := store.UpsertHomeDismissal(ctx, userstore.HomeItemDismissal{
		ProfileID: profileID, Surface: userstore.HomeSurfaceNextUp, MediaItemID: episodes[1],
		SeriesID: &seriesID, DismissedAt: dismissedAt,
	}); err != nil {
		t.Fatalf("dismiss from Next Up: %v", err)
	}
	recompute("removed from Next Up too", flags{favorite: true}, afterE1)

	// Resuming E2 brings its Continue Watching card back; E2 is still next,
	// so the Next Up card stays dismissed, as on Home.
	setProgress(episodes[1], false, time.Now().Add(-2*time.Minute).UTC().Truncate(time.Second))
	recompute("resumed E2", flags{favorite: true, continueWatching: true}, afterE1)

	// Finishing E2 moves Next Up on to E3.
	setProgress(episodes[1], true, time.Now().Add(-time.Minute).UTC().Truncate(time.Second))
	recompute("finished E2", flags{favorite: true, nextUp: true}, afterE2)

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
	recompute("series dropped", flags{favorite: true}, afterE2)

	if err := drops.Undrop(ctx, userID, profileID, seriesID); err != nil {
		t.Fatalf("Undrop: %v", err)
	}
	requireQueued("undropping the series")
	recompute("series undropped", flags{favorite: true, nextUp: true}, afterE2)

	// Drops imported from, and removed by, a watch provider.
	droppedAt := time.Now().UTC().Truncate(time.Microsecond)
	if applied, err := drops.ImportDrop(ctx, userID, profileID, seriesID, droppedAt, nil); err != nil || !applied {
		t.Fatalf("ImportDrop = %v, %v", applied, err)
	}
	requireQueued("importing a drop")
	recompute("drop imported", flags{favorite: true}, afterE2)

	if removed, err := drops.DeleteIfUnchanged(ctx, userID, profileID, seriesID, droppedAt); err != nil || !removed {
		t.Fatalf("DeleteIfUnchanged = %v, %v", removed, err)
	}
	requireQueued("removing an imported drop")
	recompute("imported drop removed", flags{favorite: true, nextUp: true}, afterE2)
}
