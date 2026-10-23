package handlers

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type finishHistoryReplica struct {
	mgr     *playback.SessionManager
	handler *PlaybackHandler
}

// newFinishHistoryReplica builds one API replica's playback handler over the
// user store and admin log every replica shares.
func newFinishHistoryReplica(store userstore.UserStore, admin PlaybackAdminStore, file *models.MediaFile) finishHistoryReplica {
	mgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(mgr, testPlaybackFileResolver{file: file})
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.AdminStore = admin
	return finishHistoryReplica{mgr: mgr, handler: handler}
}

// holdCopy registers this replica's copy of a session at the position the
// reports that reached this replica left it at.
func (r finishHistoryReplica) holdCopy(t *testing.T, session playback.Session, position float64) *playback.Session {
	t.Helper()
	r.mgr.RegisterReconstructed(&session)
	if err := r.mgr.UpdateProgress(session.ID, position, false); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
	held, err := r.mgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	return held
}

// expire finalizes this replica's copy the way idle cleanup does.
func (r finishHistoryReplica) expire(session *playback.Session) {
	r.handler.finalizeSessionStop(context.Background(), session, false, "", false)
}

func compatFinishSession() playback.Session {
	return playback.Session{
		ID:               "compat-upstream-1",
		UserID:           1,
		ProfileID:        "profile-1",
		MediaFileID:      42,
		IsJellyfinCompat: true,
		StartedAt:        time.Now().Add(-time.Hour),
	}
}

func finishHistoryFile() *models.MediaFile {
	return &models.MediaFile{ID: 42, ContentID: "movie-1", Duration: 3600}
}

func listFinishHistory(t *testing.T, store userstore.UserStore) []userstore.WatchHistoryEntry {
	t.Helper()
	history, err := store.ListHistory(context.Background(), "profile-1", 100, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	return history
}

func assertOneCompletedPlay(t *testing.T, store userstore.UserStore) {
	t.Helper()
	history := listFinishHistory(t, store)
	if len(history) != 1 {
		t.Fatalf("watch history rows = %d, want 1", len(history))
	}
	if got := history[0]; got.MediaItemID != "movie-1" || !got.Completed || got.Source != userstore.WatchHistorySourcePlayback {
		t.Fatalf("watch history = %+v, want a completed playback row for movie-1", got)
	}
}

// reportWatched writes the progress a Jellyfin client's final report leaves:
// the play is watched and its resume point cleared.
func reportWatched(t *testing.T, store userstore.UserStore) {
	t.Helper()
	if err := store.UpdateProgress(context.Background(), "profile-1", "movie-1", 3500, 3600, userstore.ProgressThresholds{}); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
}

// assertStillWatched checks the finished play's progress: watched, with the
// resume point cleared rather than moved back to a stale copy's position.
func assertStillWatched(t *testing.T, store userstore.UserStore) {
	t.Helper()
	progress, err := store.GetProgress(context.Background(), "profile-1", "movie-1")
	if err != nil || progress == nil {
		t.Fatalf("GetProgress: %v, %v", progress, err)
	}
	if !progress.Completed || progress.PositionSeconds != 0 {
		t.Fatalf("progress = completed %v at %v, want completed at 0", progress.Completed, progress.PositionSeconds)
	}
}

// TestFinishedJellyfinPlayIsRecordedOnce covers issue #1738: a Jellyfin play
// finished through FinishSession gains one watch-history row and an admin
// row. A retried finish adds nothing, and neither does another replica
// expiring its older copy of the same session, which must not move the
// resume point back either.
func TestFinishedJellyfinPlayIsRecordedOnce(t *testing.T) {
	store := newPlaybackTestStore(t)
	admin := &recordingPlaybackAdminStore{}
	stopped := newFinishHistoryReplica(store, admin, finishHistoryFile())
	stale := newFinishHistoryReplica(store, admin, finishHistoryFile())
	session := compatFinishSession()
	stopped.holdCopy(t, session, 3500)
	staleCopy := stale.holdCopy(t, session, 1800)
	reportWatched(t, store)

	if err := stopped.mgr.FinishSession(context.Background(), session.ID); err != nil {
		t.Fatalf("FinishSession: %v", err)
	}
	if err := stopped.mgr.FinishSession(context.Background(), session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("retried FinishSession: err = %v, want ErrSessionNotFound", err)
	}
	stale.expire(staleCopy)

	assertOneCompletedPlay(t, store)
	assertStillWatched(t, store)
	if len(admin.history) == 0 || admin.history[0].WatchedSeconds != 3500 || !admin.history[0].Completed {
		t.Fatalf("admin history = %+v, want the finished play first, at 3500 and completed", admin.history)
	}
}

// TestStaleJellyfinCopyCannotBlockThePlay covers a copy that expires first
// but could not record the play itself: one that never received a progress
// report, and one whose reports stopped below the minimum resume threshold.
// The copy that saw the play still records it.
func TestStaleJellyfinCopyCannotBlockThePlay(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stalePosition float64
	}{
		{name: "no position", stalePosition: 0},
		{name: "below minimum resume", stalePosition: 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newPlaybackTestStore(t)
			admin := &recordingPlaybackAdminStore{}
			stale := newFinishHistoryReplica(store, admin, finishHistoryFile())
			live := newFinishHistoryReplica(store, admin, finishHistoryFile())
			session := compatFinishSession()
			staleCopy := stale.holdCopy(t, session, tc.stalePosition)
			liveCopy := live.holdCopy(t, session, 3500)

			stale.expire(staleCopy)
			live.expire(liveCopy)

			assertOneCompletedPlay(t, store)
		})
	}
}

// TestFurtherJellyfinCopyCompletesThePlay covers copies that expire out of
// order: an older copy past the minimum resume threshold records the play
// incomplete, and the copy that saw it finish completes the same row.
func TestFurtherJellyfinCopyCompletesThePlay(t *testing.T) {
	store := newPlaybackTestStore(t)
	admin := &recordingPlaybackAdminStore{}
	older := newFinishHistoryReplica(store, admin, finishHistoryFile())
	further := newFinishHistoryReplica(store, admin, finishHistoryFile())
	session := compatFinishSession()
	olderCopy := older.holdCopy(t, session, 1800)
	furtherCopy := further.holdCopy(t, session, 3500)

	older.expire(olderCopy)
	if history := listFinishHistory(t, store); len(history) != 1 || history[0].Completed {
		t.Fatalf("history after the older copy = %+v, want one incomplete row", history)
	}
	further.expire(furtherCopy)

	assertOneCompletedPlay(t, store)
}

// failingHistoryStore fails the first history insert, as a dropped database
// connection would.
type failingHistoryStore struct {
	userstore.UserStore
	failures int
}

func (s *failingHistoryStore) AddVisibleHistory(ctx context.Context, entry userstore.WatchHistoryEntry) (userstore.WatchHistoryEntry, error) {
	if s.failures > 0 {
		s.failures--
		return entry, errors.New("connection reset")
	}
	return userstore.AddVisibleHistory(ctx, s.UserStore, entry)
}

// TestFailedJellyfinHistoryWriteIsRecoveredByAnotherCopy covers a stop whose
// watch-history write fails after its admin row is stored: another replica's
// copy of the session still records the play when it expires.
func TestFailedJellyfinHistoryWriteIsRecoveredByAnotherCopy(t *testing.T) {
	sqlite := newPlaybackTestStore(t)
	store := &failingHistoryStore{UserStore: sqlite, failures: 1}
	admin := &recordingPlaybackAdminStore{}
	stopped := newFinishHistoryReplica(store, admin, finishHistoryFile())
	other := newFinishHistoryReplica(store, admin, finishHistoryFile())
	session := compatFinishSession()
	stopped.holdCopy(t, session, 3500)
	otherCopy := other.holdCopy(t, session, 3400)

	if err := stopped.mgr.FinishSession(context.Background(), session.ID); err != nil {
		t.Fatalf("FinishSession: %v", err)
	}
	if got := len(listFinishHistory(t, sqlite)); got != 0 {
		t.Fatalf("watch history rows after the failed write = %d, want 0", got)
	}
	other.expire(otherCopy)

	assertOneCompletedPlay(t, sqlite)
}

// failingHintsStore fails version-hint writes, as a dropped database
// connection after the history insert would.
type failingHintsStore struct{ userstore.UserStore }

func (s failingHintsStore) AddVisibleHistory(ctx context.Context, entry userstore.WatchHistoryEntry) (userstore.WatchHistoryEntry, error) {
	return userstore.AddVisibleHistory(ctx, s.UserStore, entry)
}

func (failingHintsStore) UpdateProgressHints(context.Context, string, string, userstore.VersionHints) error {
	return errors.New("connection reset")
}

// TestRecordedJellyfinPlayRefreshesProfileDespiteFailedHints covers a stop
// whose history row commits and whose hints write then fails: the profile is
// still refreshed for the new play, once, since later copies find the play
// already recorded.
func TestRecordedJellyfinPlayRefreshesProfileDespiteFailedHints(t *testing.T) {
	sqlite := newPlaybackTestStore(t)
	store := failingHintsStore{sqlite}
	admin := &recordingPlaybackAdminStore{}
	stopped := newFinishHistoryReplica(store, admin, finishHistoryFile())
	stale := newFinishHistoryReplica(store, admin, finishHistoryFile())
	notifier := &countingSignalsNotifier{}
	stopped.handler.SetSignalsChangedNotifier(notifier)
	stale.handler.SetSignalsChangedNotifier(notifier)
	session := compatFinishSession()
	stopped.holdCopy(t, session, 3500)
	staleCopy := stale.holdCopy(t, session, 1800)

	if err := stopped.mgr.FinishSession(context.Background(), session.ID); err != nil {
		t.Fatalf("FinishSession: %v", err)
	}
	stale.expire(staleCopy)

	assertOneCompletedPlay(t, sqlite)
	if notifier.calls != 1 {
		t.Fatalf("profile refreshes = %d, want 1", notifier.calls)
	}
}

// TestFinishedSessionWithoutPositionRecordsNothing covers a Jellyfin start
// that failed to route or transcode: it never played, so it is no play.
func TestFinishedSessionWithoutPositionRecordsNothing(t *testing.T) {
	store := newPlaybackTestStore(t)
	admin := &recordingPlaybackAdminStore{}
	replica := newFinishHistoryReplica(store, admin, finishHistoryFile())
	session := compatFinishSession()
	replica.mgr.RegisterReconstructed(&session)

	if err := replica.mgr.FinishSession(context.Background(), session.ID); err != nil {
		t.Fatalf("FinishSession: %v", err)
	}
	if got := len(listFinishHistory(t, store)); len(admin.history) != 0 || got != 0 {
		t.Fatalf("admin rows = %d, watch rows = %d, want none", len(admin.history), got)
	}
}

// TestNativeSessionKeepsHistoryRowPerStop guards the native rule the
// Jellyfin row ID leaves alone: a native session resumed after expiry under
// the same id records each stretch in watch history.
func TestNativeSessionKeepsHistoryRowPerStop(t *testing.T) {
	file := finishHistoryFile()
	store := newPlaybackTestStore(t)
	replica := newFinishHistoryReplica(store, &recordingPlaybackAdminStore{}, file)
	session := &playback.Session{
		ID: "native-1", UserID: 1, ProfileID: "profile-1", MediaFileID: file.ID,
		Position: 1800, StartedAt: time.Now().Add(-time.Hour),
	}

	replica.handler.finalizeSessionStop(context.Background(), session, false, "", false)
	session.Position = 3500
	replica.handler.finalizeSessionStop(context.Background(), session, false, "", true)

	if got := len(listFinishHistory(t, store)); got != 2 {
		t.Fatalf("watch history rows = %d, want 2", got)
	}
}

// TestAdminPlaybackWatchTimeDB proves a resumed play counts what was watched,
// not where it ended: the measured watch time survives a later finalization
// of another copy that never saw progress, and the watch summary sums it with
// the capped estimate for a play recorded before measurement existed.
func TestAdminPlaybackWatchTimeDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := uuid.NewString()
	var account int
	if err = pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "watch-time-"+suffix).Scan(&account); err != nil {
		t.Fatal(err)
	}
	// Deleting the account cascades to its playback history.
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, account) }()

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	started, ended := now.Add(-15*time.Minute), now.Add(-time.Minute)
	store := NewPGPlaybackAdminStore(pool, nil)
	finalize := func(position float64, completed bool, watch playback.WatchTime) {
		t.Helper()
		if err := store.RecordHistory(ctx, AdminPlaybackHistoryEntry{
			SessionID: "resumed-" + suffix, UserID: account, ProfileID: "p-1", PlayMethod: "direct_play",
			StartedAt: started.Format(time.RFC3339Nano), EndedAt: ended.Format(time.RFC3339Nano),
			WatchedSeconds: position, Completed: completed, Watch: watch,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Resumed at 1:30:00, watched two minutes; then a copy on another replica
	// that only served media finalizes further along with no measurement. A
	// copy that recorded completion short of that position marks the play
	// completed without moving it back, and an incomplete copy can't clear it.
	finalize(5520, false, playback.WatchTime{Measured: true, StartPosition: 5400, PlayedSeconds: 120, PausedSeconds: 30})
	finalize(5600, false, playback.WatchTime{})
	finalize(5500, true, playback.WatchTime{})
	finalize(5590, false, playback.WatchTime{})
	// A one-minute play recorded before measurement, ending at 100 seconds.
	if _, err = pool.Exec(ctx, `INSERT INTO admin_playback_history(session_id,user_id,profile_id,media_file_id,play_method,started_at,ended_at,watched_seconds)
		VALUES($1,$2,'p-1',0,'direct_play',$3,$4,100)`, "legacy-"+suffix, account, now.Add(-2*time.Hour), now.Add(-2*time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}

	var watched, played float64
	var measured, completed bool
	if err = pool.QueryRow(ctx, `SELECT watched_seconds, played_seconds, measured, completed FROM admin_playback_history WHERE session_id=$1`, "resumed-"+suffix).Scan(&watched, &played, &measured, &completed); err != nil {
		t.Fatal(err)
	}
	if watched != 5600 || played != 120 || !measured || !completed {
		t.Fatalf("resumed row = watched %v, played %v, measured %v, completed %v; want 5600, 120, measured, completed", watched, played, measured, completed)
	}
	got, err := (&AdminHandler{pool: pool}).ReadAdminWatchSummary(ctx, AdminWatchSummaryQuery{UserID: account, Days: 7, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got.Plays != 2 || got.WatchedSeconds != 180 {
		t.Fatalf("summary = %d plays, %v seconds; want 2 plays, 180 seconds (120 measured + 60 capped)", got.Plays, got.WatchedSeconds)
	}

	// A session finalized, rebuilt under the same id and finalized again
	// watched two stretches; a copy that overlaps one of them, or the same
	// copy finalized twice, is not another stretch.
	rebuilt := "rebuilt-" + suffix
	stretch := func(from, to time.Duration, played float64) {
		t.Helper()
		if err := store.RecordHistory(ctx, AdminPlaybackHistoryEntry{
			SessionID: rebuilt, UserID: account, ProfileID: "p-1", PlayMethod: "direct_play",
			StartedAt: started.Format(time.RFC3339Nano), EndedAt: started.Add(to).Format(time.RFC3339Nano),
			WatchedSeconds: played, Watch: playback.WatchTime{Measured: true, PlayedSeconds: played, Since: started.Add(from), Until: started.Add(to)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	stretch(0, 2*time.Minute, 120)
	stretch(5*time.Minute, 8*time.Minute, 180)
	stretch(5*time.Minute, 8*time.Minute, 180)
	stretch(time.Minute, 7*time.Minute, 200)
	if err = pool.QueryRow(ctx, `SELECT played_seconds FROM admin_playback_history WHERE session_id=$1`, rebuilt).Scan(&played); err != nil {
		t.Fatal(err)
	}
	if played != 300 {
		t.Fatalf("rebuilt session played = %v, want 300 (two stretches of 120 and 180)", played)
	}
}
