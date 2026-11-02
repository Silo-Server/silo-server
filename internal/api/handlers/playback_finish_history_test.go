package handlers

import (
	"context"
	"errors"
	"os"
	"reflect"
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
	r.handler.finalizeSessionStop(context.Background(), session, false, "", "")
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

	replica.handler.finalizeSessionStop(context.Background(), session, false, "", "")
	session.Position = 3500
	replica.handler.finalizeSessionStop(context.Background(), session, false, "", PlaybackEndStopped)

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

// TestAdminPlaybackHistoryFactsDB proves the device, client and stream
// decisions are filled once whether or not a finalization advanced the
// counters, the end reason follows the latest finalization, and the listing
// windows ended_at inclusive below and exclusive above.
func TestAdminPlaybackHistoryFactsDB(t *testing.T) {
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
	if err = pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "history-facts-"+suffix).Scan(&account); err != nil {
		t.Fatal(err)
	}
	// Deleting the account cascades to its playback history.
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, account) }()

	started := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	store := NewPGPlaybackAdminStore(pool, nil)
	session := "facts-" + suffix
	finalize := func(position float64, ended time.Duration, entry AdminPlaybackHistoryEntry) {
		t.Helper()
		if entry.SessionID == "" {
			entry.SessionID = session
		}
		entry.UserID, entry.ProfileID, entry.PlayMethod = account, "p-1", "direct_play"
		entry.StartedAt, entry.EndedAt = started.Format(time.RFC3339Nano), started.Add(ended).Format(time.RFC3339Nano)
		entry.WatchedSeconds = position
		if err := store.RecordHistory(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	// A copy that knew nothing about the device expires first. A later copy
	// that did not get further still records the facts, and a third, with
	// other values, replaces none of them. A stop after the expiry replaces
	// its reason; an admin terminate and a delayed expiry recorded with
	// earlier ends do not replace the stop.
	finalize(600, 10*time.Minute, AdminPlaybackHistoryEntry{EndReason: PlaybackEndExpired})
	finalize(500, 9*time.Minute, AdminPlaybackHistoryEntry{DeviceID: "tv-1", DevicePlatform: "tvOS", ClientName: "Silo tvOS", ClientVersion: "2.1.0", VideoDecision: "direct", AudioDecision: "transcode"})
	finalize(700, 12*time.Minute, AdminPlaybackHistoryEntry{DeviceID: "tv-2", DevicePlatform: "iOS", ClientName: "Other", ClientVersion: "9", VideoDecision: "transcode", AudioDecision: "direct", EndReason: PlaybackEndStopped})
	finalize(700, 11*time.Minute, AdminPlaybackHistoryEntry{EndReason: PlaybackEndTerminated})
	// The same holds when the stop got no further, so ended_at never moved
	// past the expiry.
	stalled := "stalled-" + suffix
	finalize(300, 10*time.Minute, AdminPlaybackHistoryEntry{SessionID: stalled, EndReason: PlaybackEndExpired})
	finalize(300, 12*time.Minute, AdminPlaybackHistoryEntry{SessionID: stalled, EndReason: PlaybackEndStopped})
	finalize(300, 11*time.Minute, AdminPlaybackHistoryEntry{SessionID: stalled, EndReason: PlaybackEndExpired})
	var stalledReason string
	if err = pool.QueryRow(ctx, `SELECT end_reason FROM admin_playback_history WHERE session_id=$1`, stalled).Scan(&stalledReason); err != nil {
		t.Fatal(err)
	}
	if stalledReason != string(PlaybackEndStopped) {
		t.Errorf("stalled play end_reason = %s, want stopped: a delayed earlier expiry must not replace a later stop", stalledReason)
	}
	// A play recorded before any of these facts existed.
	legacy := "legacy-" + suffix
	if _, err = pool.Exec(ctx, `INSERT INTO admin_playback_history(session_id,user_id,profile_id,media_file_id,play_method,started_at,ended_at,watched_seconds)
		VALUES($1,$2,'p-1',0,'direct_play',$3,$4,100)`, legacy, account, started, started.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// An expiring stale copy reads the recorded end to stay out of it.
	for id, want := range map[string]PlaybackEndReason{stalled: PlaybackEndStopped, legacy: "", "missing-" + suffix: ""} {
		if got, err := store.HistoryEndReason(ctx, id); err != nil || got != want {
			t.Errorf("HistoryEndReason(%s) = %q, %v; want %q", id, got, err, want)
		}
	}

	admin := &AdminHandler{pool: pool}
	list := func(after, before time.Time) []AdminPlaybackHistoryRow {
		t.Helper()
		page, err := admin.ListAdminPlaybackHistoryPage(ctx, AdminPlaybackHistoryFilter{UserID: account, EndedAfter: &after, EndedBefore: &before}, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		return page.Items
	}
	rows := list(started.Add(12*time.Minute), started.Add(time.Hour))
	if len(rows) != 1 || rows[0].SessionID != session {
		t.Fatalf("window [12m, 1h) = %+v, want only the finalized play (ended_after inclusive, ended_before exclusive)", rows)
	}
	got := rows[0]
	text := func(v *string) string {
		if v == nil {
			return "<nil>"
		}
		return *v
	}
	for _, check := range []struct{ field, got, want string }{
		{"device_id", text(got.DeviceID), "tv-1"},
		{"device_platform", text(got.DevicePlatform), "tvOS"},
		{"client_name", text(got.ClientName), "Silo tvOS"},
		{"client_version", text(got.ClientVersion), "2.1.0"},
		{"video_decision", text(got.VideoDecision), "direct"},
		{"audio_decision", text(got.AudioDecision), "transcode"},
		{"end_reason", text(got.EndReason), "stopped"},
	} {
		if check.got != check.want {
			t.Errorf("%s = %s, want %s", check.field, check.got, check.want)
		}
	}
	if !got.EndedAt.Equal(started.Add(12*time.Minute)) || got.WatchedSeconds != 700 {
		t.Errorf("ended_at %v watched %v, want the furthest finalization", got.EndedAt, got.WatchedSeconds)
	}

	rows = list(started.Add(time.Hour), started.Add(2*time.Hour))
	if len(rows) != 1 || rows[0].SessionID != legacy {
		t.Fatalf("window [1h, 2h) = %+v, want only the legacy play", rows)
	}
	if old := rows[0]; old.DeviceID != nil || old.ClientName != nil || old.VideoDecision != nil || old.EndReason != nil || old.Measured || old.StartPositionSeconds != nil || old.ClientIP != nil {
		t.Errorf("legacy row = %+v, want unknown facts as nil", old)
	}
}

// liveRowAdminStore is the admin log with one shared live row, which a
// replica synced before the play ended somewhere without its copy.
type liveRowAdminStore struct {
	recordingPlaybackAdminStore
	live *playback.Session
}

func (s *liveRowAdminStore) LiveSession(_ context.Context, sessionID string) (*playback.Session, error) {
	if s.live == nil || s.live.ID != sessionID {
		return nil, playback.ErrSessionNotFound
	}
	live := *s.live
	return &live, nil
}

// TestEveryEndRecordsTheAdminLog covers ends that wrote no admin history: an
// abort, a Jellyfin method switch, and a stop that reached a replica without
// the session's copy. Each records one row with its reason; a replaced play
// adds no watch history, since it continues under the session that replaced
// it, and a detached snapshot takes its start and client from the live row.
func TestEveryEndRecordsTheAdminLog(t *testing.T) {
	started := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	live := compatFinishSession()
	live.StartedAt, live.ClientName, live.DeviceID = started, "Jellyfin Web", "browser-1"
	for _, tc := range []struct {
		name        string
		end         func(r finishHistoryReplica, held *playback.Session)
		wantReason  PlaybackEndReason
		wantWatched int
		wantClient  string
	}{{
		name: "abort",
		end: func(r finishHistoryReplica, held *playback.Session) {
			if err := r.handler.abortPlaybackSession(context.Background(), held); err != nil {
				t.Fatal(err)
			}
		},
		wantReason: PlaybackEndAborted,
	}, {
		name: "method switch",
		end: func(r finishHistoryReplica, held *playback.Session) {
			_ = r.mgr.StopSession(held.ID)
			r.mgr.FinishDetached(playback.WithEndReason(context.Background(), playback.EndReasonReplaced), *held)
		},
		wantReason: PlaybackEndReplaced,
	}, {
		name: "stop without the copy",
		end: func(r finishHistoryReplica, held *playback.Session) {
			_ = r.mgr.StopSession(held.ID)
			// The stop's own snapshot: identity and the final position only.
			r.mgr.FinishDetached(context.Background(), playback.Session{
				ID: held.ID, UserID: held.UserID, ProfileID: held.ProfileID,
				MediaFileID: held.MediaFileID, Position: held.Position, IsJellyfinCompat: true,
			})
		},
		wantReason:  PlaybackEndStopped,
		wantWatched: 1,
		wantClient:  "Jellyfin Web",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			store := newPlaybackTestStore(t)
			admin := &liveRowAdminStore{live: &live}
			replica := newFinishHistoryReplica(store, admin, finishHistoryFile())
			held := replica.holdCopy(t, compatFinishSession(), 1800)
			tc.end(replica, held)

			if len(admin.history) != 1 {
				t.Fatalf("admin history = %+v, want one row", admin.history)
			}
			row := admin.history[0]
			if row.EndReason != tc.wantReason || row.WatchedSeconds != 1800 {
				t.Fatalf("admin row = reason %q at %v, want %q at 1800", row.EndReason, row.WatchedSeconds, tc.wantReason)
			}
			if tc.wantClient != "" && (row.ClientName != tc.wantClient || row.DeviceID != "browser-1" || row.StartedAt != started.Format(time.RFC3339Nano)) {
				t.Fatalf("admin row = client %q device %q started %s, want the live row's", row.ClientName, row.DeviceID, row.StartedAt)
			}
			if got := len(listFinishHistory(t, store)); got != tc.wantWatched {
				t.Fatalf("watch history rows = %d, want %d", got, tc.wantWatched)
			}
		})
	}
	// A Jellyfin snapshot with no position never saw the play: it records
	// nothing, whatever the reason.
	store := newPlaybackTestStore(t)
	admin := &liveRowAdminStore{}
	replica := newFinishHistoryReplica(store, admin, finishHistoryFile())
	replica.mgr.FinishDetached(playback.WithEndReason(context.Background(), playback.EndReasonReplaced), compatFinishSession())
	if len(admin.history) != 0 {
		t.Fatalf("admin history = %+v, want none for a copy without progress", admin.history)
	}
	held := replica.holdCopy(t, compatFinishSession(), 0)
	if err := replica.handler.abortPlaybackSession(context.Background(), held); err != nil {
		t.Fatal(err)
	}
	if len(admin.history) != 0 {
		t.Fatalf("admin history = %+v, want none for an aborted copy without progress", admin.history)
	}
}

// TestLiveSessionReadsTheSyncedRowDB pins the columns a detached end reads
// from the shared live row, including a row that never recorded a start.
func TestLiveSessionReadsTheSyncedRowDB(t *testing.T) {
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
	if err = pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "live-row-"+suffix).Scan(&account); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, account) }()
	started := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	synced, bare := "synced-"+suffix, "bare-"+suffix
	if _, err = pool.Exec(ctx, `INSERT INTO playback_sessions_sync
		(session_id, user_id, profile_id, media_file_id, requested_media_file_id, play_method, started_at, client_ip,
		 client_name, client_version, device_id, device_platform, transcode_audio, target_video_codec, position_seconds, compat_origin)
		VALUES ($1, $2, 'p-1', 7, 8, 'transcode', $3, '203.0.113.9', 'Jellyfin Web', '10.9', 'browser-1', 'web', TRUE, 'h264', 1800, TRUE)`,
		synced, account, started); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO playback_sessions_sync (session_id, user_id) VALUES ($1, $2)`, bare, account); err != nil {
		t.Fatal(err)
	}
	store := NewPGPlaybackAdminStore(pool, nil)
	live, err := store.LiveSession(ctx, synced)
	if err != nil {
		t.Fatal(err)
	}
	want := playback.Session{ID: synced, UserID: account, ProfileID: "p-1", MediaFileID: 7, RequestedMediaFileID: 8,
		PlayMethod: playback.PlayTranscode, StartedAt: started, ClientIP: "203.0.113.9", ClientName: "Jellyfin Web",
		ClientVersion: "10.9", DeviceID: "browser-1", DevicePlatform: "web", TranscodeAudio: true,
		TargetVideoCodec: "h264", Position: 1800, IsJellyfinCompat: true}
	if live.StartedAt.Equal(started) {
		live.StartedAt = started
	}
	if !reflect.DeepEqual(*live, want) {
		t.Fatalf("live = %+v\nwant %+v", *live, want)
	}
	if live, err = store.LiveSession(ctx, bare); err != nil || !live.StartedAt.IsZero() || live.UserID != account {
		t.Fatalf("bare row = %+v, %v; want the account and no start", live, err)
	}
	if _, err = store.LiveSession(ctx, "missing-"+suffix); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("missing row: err = %v, want ErrSessionNotFound", err)
	}
}

// TestDetachedSnapshotsKeepWhatTheyReported covers the position of a stop
// recorded from a snapshot: one the stop did not report is taken from the
// shared live row, along with the account a Jellyfin play row does not keep,
// and a reported zero stays zero, so a Jellyfin copy that saw no progress
// records nothing even when an older live row shows some.
func TestDetachedSnapshotsKeepWhatTheyReported(t *testing.T) {
	live := compatFinishSession()
	live.StartedAt, live.Position, live.ClientName, live.DeviceID = time.Now().Add(-time.Hour), 1500, "Jellyfin Web", "browser-1"
	for _, tc := range []struct {
		name     string
		position float64
		wantRows int
	}{
		{"unreported", playback.PositionUnknown, 1},
		{"reported zero", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admin := &liveRowAdminStore{live: &live}
			replica := newFinishHistoryReplica(newPlaybackTestStore(t), admin, finishHistoryFile())
			replica.mgr.FinishDetached(context.Background(), playback.Session{ID: live.ID, MediaFileID: 42, Position: tc.position, IsJellyfinCompat: true})
			if len(admin.history) != tc.wantRows {
				t.Fatalf("admin history = %+v, want %d rows", admin.history, tc.wantRows)
			}
			if tc.wantRows == 1 && (admin.history[0].WatchedSeconds != 1500 || admin.history[0].UserID != live.UserID) {
				t.Fatalf("row = %+v, want the live row's account and position", admin.history[0])
			}
		})
	}
	// A snapshot that knows its start (an Audiobookshelf close) still takes
	// the client and device only the live row has.
	admin := &liveRowAdminStore{live: &live}
	replica := newFinishHistoryReplica(newPlaybackTestStore(t), admin, finishHistoryFile())
	replica.mgr.FinishDetached(context.Background(), playback.Session{ID: live.ID, UserID: live.UserID, ProfileID: live.ProfileID, MediaFileID: 42, Position: 600, StartedAt: live.StartedAt})
	if len(admin.history) != 1 || admin.history[0].ClientName != "Jellyfin Web" || admin.history[0].DeviceID != "browser-1" || admin.history[0].WatchedSeconds != 600 {
		t.Fatalf("admin history = %+v, want the live row's client and device at the reported 600", admin.history)
	}
}

type missingFileResolver struct{}

func (missingFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	return nil, playback.ErrFileNotFound
}

// TestEndRecordsAPlayWhoseFileLeftTheCatalog covers an end recorded after
// the play's media file was removed (a deleted library): the admin log
// still gets its row, without the item and duration only the file gave.
func TestEndRecordsAPlayWhoseFileLeftTheCatalog(t *testing.T) {
	admin := &recordingPlaybackAdminStore{}
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0), missingFileResolver{})
	h.AdminStore = admin
	session := compatFinishSession()
	session.IsJellyfinCompat, session.Position = false, 900
	h.recordAdminStopHistory(context.Background(), &session, PlaybackEndExpired)
	if len(admin.history) != 1 || admin.history[0].EndReason != PlaybackEndExpired || admin.history[0].MediaItemID != "" || admin.history[0].DurationSeconds != nil || admin.history[0].WatchedSeconds != 900 {
		t.Fatalf("admin history = %+v, want one expired row without an item or duration", admin.history)
	}
}

// endedAdminStore is the admin log that also answers which end it recorded.
type endedAdminStore struct {
	recordingPlaybackAdminStore
}

func (s *endedAdminStore) HistoryEndReason(_ context.Context, sessionID string) (PlaybackEndReason, error) {
	for _, entry := range s.history {
		if entry.SessionID == sessionID && entry.EndReason != "" {
			return entry.EndReason, nil
		}
	}
	return "", nil
}

// TestStaleJellyfinCopyKeepsAnotherReplicasEnd covers a Jellyfin play stopped
// on one replica while another still holds an older copy: when that copy
// expires, it records nothing over the stop.
func TestStaleJellyfinCopyKeepsAnotherReplicasEnd(t *testing.T) {
	store := newPlaybackTestStore(t)
	admin := &endedAdminStore{}
	stopped := newFinishHistoryReplica(store, admin, finishHistoryFile())
	stale := newFinishHistoryReplica(store, admin, finishHistoryFile())
	session := compatFinishSession()
	stopped.holdCopy(t, session, 3500)
	staleCopy := stale.holdCopy(t, session, 1800)
	if err := stopped.mgr.FinishSession(context.Background(), session.ID); err != nil {
		t.Fatalf("FinishSession: %v", err)
	}
	stale.handler.expireSessionCopy(context.Background(), staleCopy)
	if len(admin.history) != 1 || admin.history[0].EndReason != PlaybackEndStopped {
		t.Fatalf("admin history = %+v, want only the stop", admin.history)
	}

	// An expiry is not a definite end: when an older copy expires first, the
	// copy that saw further still records its progress over it.
	admin = &endedAdminStore{}
	older := newFinishHistoryReplica(store, admin, finishHistoryFile())
	newer := newFinishHistoryReplica(store, admin, finishHistoryFile())
	session = compatFinishSession()
	session.ID = "compat-upstream-2"
	olderCopy := older.holdCopy(t, session, 1800)
	newerCopy := newer.holdCopy(t, session, 3500)
	older.handler.expireSessionCopy(context.Background(), olderCopy)
	newer.handler.expireSessionCopy(context.Background(), newerCopy)
	if len(admin.history) != 2 || admin.history[1].WatchedSeconds != 3500 {
		t.Fatalf("admin history = %+v, want the newer copy's 3500 recorded after the older expiry", admin.history)
	}
}
