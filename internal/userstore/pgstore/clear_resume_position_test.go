package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

// TestPostgresClearResumePosition pins issue #2134 on PostgreSQL: clearing a
// resume position keeps every earlier watch in history and a rewatched item's
// watched state, reaches delta-sync clients through synced_seq, and advances
// event_at so an older provider resume point cannot restore the position.
// Skips unless SILO_TEST_DATABASE_URL names a migrated database.
func TestPostgresClearResumePosition(t *testing.T) {
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

	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("conf-resume-reset-%d", time.Now().UnixNano()),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_watch_history WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_history_hidden_items WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_watch_progress WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	store := newStore(pool, userID)
	const profileID = "resume-reset"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Resume Reset"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	service := watchstate.NewService(NewPostgresProvider(pool))

	// movie-1 was watched to the end two days ago and is now partly rewatched;
	// movie-2 was only ever skimmed to the middle.
	if err := service.RecordManualMarkWatched(ctx, userID, profileID, []watchstate.LeafWatchTarget{{MediaItemID: "movie-1", DurationSeconds: 7200}}, time.Now().UTC().Add(-48*time.Hour)); err != nil {
		t.Fatalf("RecordManualMarkWatched: %v", err)
	}
	for _, play := range []struct {
		id       string
		position float64
	}{{"movie-1", 1800}, {"movie-2", 2700}} {
		if _, err := service.RecordPlaybackStop(ctx, userID, profileID, play.id, 7200, play.position, time.Now().UTC().Add(-time.Hour), userstore.VersionHints{}, userstore.ProgressThresholds{}); err != nil {
			t.Fatalf("RecordPlaybackStop(%s): %v", play.id, err)
		}
	}
	// Backdate movie-2's position so a provider resume point from half an hour
	// ago would win without the clear; the playback stop stamped it now.
	if err := store.SetProgressAt(ctx, profileID, "movie-2", 2700, 7200, false, time.Now().UTC().Add(-2*time.Hour)); err != nil {
		t.Fatalf("backdate movie-2: %v", err)
	}
	// movie-3's resume point sits under a hidden-history watermark an hour
	// ahead of the clock: live writes are stamped just past it and stay
	// visible, and the clear must still land.
	if _, err := pool.Exec(ctx, `INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before)
		VALUES ($1, $2, 'movie-3', now() + interval '1 hour')`, userID, profileID); err != nil {
		t.Fatalf("seed hidden watermark: %v", err)
	}
	if err := store.UpdateProgress(ctx, profileID, "movie-3", 600, 7200, userstore.ProgressThresholds{}); err != nil {
		t.Fatalf("UpdateProgress(movie-3): %v", err)
	}
	if progress, err := store.GetProgress(ctx, profileID, "movie-3"); err != nil || progress == nil || progress.PositionSeconds != 600 {
		t.Fatalf("movie-3 before = %+v, %v; want a visible resume point", progress, err)
	}
	before, err := store.ListHistory(ctx, profileID, 10, 0)
	if err != nil || len(before) != 3 {
		t.Fatalf("history before = %+v, %v; want the mark and two partial plays", before, err)
	}
	_, cursor, err := store.ListProgressSince(ctx, profileID, "")
	if err != nil {
		t.Fatalf("ListProgressSince: %v", err)
	}

	clearedAt := time.Now().UTC()
	for _, id := range []string{"movie-1", "movie-2", "movie-3"} {
		cleared, err := service.ClearResumePosition(ctx, userID, profileID, id)
		if err != nil || !cleared {
			t.Fatalf("ClearResumePosition(%s) = %v, %v; want cleared", id, cleared, err)
		}
	}

	delta, _, err := store.ListProgressSince(ctx, profileID, cursor)
	if err != nil {
		t.Fatalf("ListProgressSince after clear: %v", err)
	}
	if len(delta) != 3 {
		t.Fatalf("delta after clear = %+v, want all three cleared rows", delta)
	}
	for _, row := range delta {
		want := row.MediaItemID == "movie-1"
		if row.PositionSeconds != 0 || row.Completed != want || row.DurationSeconds != 7200 {
			t.Fatalf("%s delta row = %+v, want position 0, completed %v, duration kept", row.MediaItemID, row, want)
		}
	}
	var eventAt time.Time
	if err := pool.QueryRow(ctx, `SELECT event_at FROM user_watch_progress WHERE user_id = $1 AND profile_id = $2 AND media_item_id = 'movie-2'`,
		userID, profileID).Scan(&eventAt); err != nil || eventAt.Before(clearedAt.Add(-time.Second)) {
		t.Fatalf("movie-2 event_at = %v, %v; want the clear's time", eventAt, err)
	}

	after, err := store.ListHistory(ctx, profileID, 10, 0)
	if err != nil || len(after) != len(before) {
		t.Fatalf("history after = %+v, %v; want all %d earlier watches kept", after, err, len(before))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Completed != before[i].Completed {
			t.Fatalf("history row %d = %+v, want %+v", i, after[i], before[i])
		}
	}

	// A provider resume point recorded before the clear must not restore it.
	applied, err := store.SetProgressIfNewer(ctx, profileID, "movie-2", 2700, 7200, false, time.Now().UTC().Add(-30*time.Minute))
	if err != nil || applied {
		t.Fatalf("older provider resume point applied = %v, %v; want rejected", applied, err)
	}
	if progress, err := store.GetProgress(ctx, profileID, "movie-2"); err != nil || progress == nil || progress.PositionSeconds != 0 {
		t.Fatalf("movie-2 progress = %+v, %v; want position 0", progress, err)
	}
}
