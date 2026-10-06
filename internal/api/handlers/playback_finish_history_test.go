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
	staler := &countingProfileStaler{}
	stopped.handler.SetProfileStaler(staler)
	stale.handler.SetProfileStaler(staler)
	session := compatFinishSession()
	stopped.holdCopy(t, session, 3500)
	staleCopy := stale.holdCopy(t, session, 1800)

	if err := stopped.mgr.FinishSession(context.Background(), session.ID); err != nil {
		t.Fatalf("FinishSession: %v", err)
	}
	stale.expire(staleCopy)

	assertOneCompletedPlay(t, sqlite)
	if staler.calls != 1 {
		t.Fatalf("profile refreshes = %d, want 1", staler.calls)
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

// TestPGPlaybackAdminStoreKeepsFurthestFinalizationDB proves a session
// finalized more than once keeps the furthest position, whichever
// finalization lands first.
func TestPGPlaybackAdminStoreKeepsFurthestFinalizationDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := t.Context()
	suffix := uuid.NewString()
	var account int
	if err = pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "finish-history-"+suffix).Scan(&account); err != nil {
		t.Fatal(err)
	}
	sessionID := "finish-" + suffix
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM admin_playback_history WHERE session_id=$1`, sessionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, account)
	}()

	store := NewPGPlaybackAdminStore(pool, nil)
	started := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	duration := 3600.0
	finalize := func(watched float64, completed bool, ended time.Time) {
		t.Helper()
		if err := store.RecordHistory(ctx, AdminPlaybackHistoryEntry{
			SessionID: sessionID, UserID: account, ProfileID: "profile-1", ProfileName: "Main",
			MediaItemID: "movie-" + suffix, PlayMethod: "direct_play",
			StartedAt: started.Format(time.RFC3339Nano), EndedAt: ended.Format(time.RFC3339Nano),
			WatchedSeconds: watched, DurationSeconds: &duration, Completed: completed,
		}); err != nil {
			t.Fatalf("RecordHistory(%v): %v", watched, err)
		}
	}
	finalize(60, false, started.Add(2*time.Hour))
	finalize(3500, true, started.Add(time.Hour))
	finalize(1800, false, started.Add(3*time.Hour))

	var watched float64
	var completed bool
	var ended time.Time
	if err := pool.QueryRow(ctx, `SELECT watched_seconds, completed, ended_at FROM admin_playback_history WHERE session_id=$1`, sessionID).Scan(&watched, &completed, &ended); err != nil {
		t.Fatal(err)
	}
	if watched != 3500 || !completed || !ended.Equal(started.Add(2*time.Hour)) {
		t.Fatalf("row = watched %v, completed %v, ended %v; want 3500, completed, ended %v", watched, completed, ended, started.Add(2*time.Hour))
	}
}
