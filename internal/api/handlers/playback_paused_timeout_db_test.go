package handlers

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/planstore"
)

// pausedTimeoutSweepFixture is a handler wired to Postgres with a video and
// an audiobook file, and sessions paused for 20 minutes against the default
// 15-minute timeout.
type pausedTimeoutSweepFixture struct {
	pool      *pgxpool.Pool
	account   int
	video     int
	audio     int
	settings  *mutablePlaybackSettingsV3
	scrobbler *recordingPlaybackWatchScrobbler
	// sessions is this server's session manager: a copy registered in it is
	// one this server holds.
	sessions *playback.SessionManager
}

// The handler's paused timeout runs against planstore.Postgres in
// production; fail the build if it stops providing the claim.
var _ pausedAttemptExpirer = (*planstore.Postgres)(nil)

// leaseHeldElsewhereStore is the Postgres plan store while another caller
// holds every stopped attempt's finalization lease.
type leaseHeldElsewhereStore struct{ *planstore.Postgres }

func (leaseHeldElsewhereStore) ClaimStopFinalization(context.Context, string, time.Time) (bool, error) {
	return false, nil
}

func newPausedTimeoutSweepFixture(t *testing.T) *pausedTimeoutSweepFixture {
	t.Helper()
	ctx, pool, account := lifecycleTestDB(t)
	f := &pausedTimeoutSweepFixture{pool: pool, account: account,
		video: lifecycleTestFile(t, ctx, pool), audio: lifecycleTestFile(t, ctx, pool),
		settings:  &mutablePlaybackSettingsV3{values: map[string]string{}},
		scrobbler: &recordingPlaybackWatchScrobbler{}, sessions: playback.NewSessionManager(0, 0)}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM playback_session_lifecycle WHERE user_id = $1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM watch_together_rooms WHERE host_user_id = $1`, account)
	})
	return f
}

// sweep runs one lifecycle sweep on the fixture's server, with the
// paused-session timeout set to minutes.
func (f *pausedTimeoutSweepFixture) sweep(t *testing.T, minutes int) {
	f.sweepWith(t, minutes, planstore.NewPostgres(f.pool))
}

// sweepWith is sweep with the given plan store.
func (f *pausedTimeoutSweepFixture) sweepWith(t *testing.T, minutes int, store playback.PlanStoreV3) {
	f.settings.values = map[string]string{playback.PausedSessionTimeoutSettingKey: strconv.Itoa(minutes)}
	h := newKeyedPlaybackHandler(f.sessions, mapPlaybackFileResolver{files: map[int]*models.MediaFile{
		f.video: {ID: f.video, ContentID: "movie:paused-timeout", Duration: 3600, CodecVideo: "h264", CodecAudio: "aac"},
		f.audio: {ID: f.audio, ContentID: "audiobook:paused-timeout", Duration: 3600, BaseType: "audiobook", CodecAudio: "aac"},
	}})
	h.PlanStoreV3 = store
	h.AdminStore = NewPGPlaybackAdminStore(f.pool, nil)
	h.SettingsRepo = f.settings
	h.WatchScrobbler = f.scrobbler
	h.sweepLifecycle(t.Context(), f.pool, time.Now())
}

// pausedSession inserts a native session paused for 20 minutes whose client
// last reported reportedAgo ago, on file, with the given normalized request.
func (f *pausedTimeoutSweepFixture) pausedSession(t *testing.T, file int, reportedAgo time.Duration, request string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO playback_session_lifecycle
		(session_id, frontend, state, seq, user_id, profile_id, media_file_id, requested_media_file_id, started_at, updated_at)
		VALUES ($1, 'native', 'paused', nextval('playback_session_lifecycle_seq'), $2, 'p-1', $3, $3, NOW() - interval '1 hour', NOW() - interval '20 minutes')`,
		id, f.account, file); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO playback_v3_attempts
		(playback_attempt_id, session_id, user_id, profile_id, requested_media_file_id, effective_media_file_id,
		 current_plan_id, current_plan, normalized_request, expires_at, last_sequence, last_sample, updated_at)
		VALUES ($1, $2::uuid, $3, 'p-1', $4, $4, 'plan-1', '{}', $5::jsonb, NOW() + interval '1 hour', 7,
		 '{"sequence":7,"position":1200,"is_paused":true}', NOW() - make_interval(secs => $6))`,
		uuid.NewString(), id, f.account, file, request, reportedAgo.Seconds()); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *pausedTimeoutSweepFixture) stopID(t *testing.T, session string) string {
	t.Helper()
	var stopID string
	if err := f.pool.QueryRow(t.Context(), `SELECT COALESCE(stop_id::text, '') FROM playback_v3_attempts WHERE session_id = $1`, session).Scan(&stopID); err != nil {
		t.Fatal(err)
	}
	return stopID
}

// exec runs a fixture statement.
func (f *pausedTimeoutSweepFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// syncRow writes the session's live row as synced syncedAgo ago.
func (f *pausedTimeoutSweepFixture) syncRow(t *testing.T, session string, syncedAgo time.Duration) {
	t.Helper()
	f.exec(t, `INSERT INTO playback_sessions_sync (session_id, user_id, profile_id, media_file_id, last_sync_at)
		VALUES ($1, $2, 'p-1', $3, NOW() - make_interval(secs => $4))`, session, f.account, f.video, syncedAgo.Seconds())
}

// hold registers a paused copy of session on this server, synced just now.
func (f *pausedTimeoutSweepFixture) hold(t *testing.T, session string) {
	t.Helper()
	f.sessions.RegisterReconstructed(&playback.Session{ID: session, UserID: f.account, ProfileID: "p-1",
		MediaFileID: f.video, RequestedMediaFileID: f.video, PlayMethod: playback.PlayDirect, Position: 1200, IsPaused: true})
	f.syncRow(t, session, 0)
}

// held reports whether this server still holds a copy of session, and
// whether its live row is still there.
func (f *pausedTimeoutSweepFixture) held(t *testing.T, session string) (copyHeld, rowKept bool) {
	t.Helper()
	_, err := f.sessions.GetSession(session)
	if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM playback_sessions_sync WHERE session_id = $1)`, session).Scan(&rowKept); err != nil {
		t.Fatal(err)
	}
	return err == nil, rowKept
}

// history is the session's admin history: how many rows, and the last end
// reason.
func (f *pausedTimeoutSweepFixture) history(t *testing.T, session string) (rows int, reason string) {
	t.Helper()
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*), COALESCE(max(end_reason), '') FROM admin_playback_history WHERE session_id = $1`,
		session).Scan(&rows, &reason); err != nil {
		t.Fatal(err)
	}
	return rows, reason
}

// stopElsewhere stops session's attempt under stopID the way another server
// does, leaving it unfinalized.
func (f *pausedTimeoutSweepFixture) stopElsewhere(t *testing.T, session, stopID string) {
	t.Helper()
	f.exec(t, `UPDATE playback_v3_attempts SET stopped_at = NOW(), stop_id = $2::uuid,
		stop_receipt = jsonb_build_object('stop_id', $2::text, 'accepted', last_sample) WHERE session_id = $1`, session, stopID)
}

// TestPausedTimeoutSweepEndsPausedSessionsDB pins which sessions the paused
// timeout ends: one paused past it whose client still reports, held here or
// by no server, ends as expired with a pause scrobble, and a copy held here
// goes with its live row. Watch Together members, Cast receivers,
// audiobooks, clients that stopped reporting and sessions another server
// holds keep going, and a room whose runtime has no members object does not
// stop the sweep. A timeout of zero ends nothing.
func TestPausedTimeoutSweepEndsPausedSessionsDB(t *testing.T) {
	f := newPausedTimeoutSweepFixture(t)
	reporting := 10 * time.Second
	eligible := f.pausedSession(t, f.video, reporting, `{}`)
	heldHere := f.pausedSession(t, f.video, reporting, `{}`)
	f.hold(t, heldHere)
	kept := map[string]string{
		"cast receiver": f.pausedSession(t, f.video, reporting, `{"client_playback_context":{"output":{"current_sink":"cast_receiver"}}}`),
		"audiobook":     f.pausedSession(t, f.audio, reporting, `{}`),
		// Quiet longer than PausedStreamSlotGrace: the idle expiry's, later.
		"stopped reporting": f.pausedSession(t, f.video, 3*time.Minute, `{}`),
		"watch together":    f.pausedSession(t, f.video, reporting, `{}`),
		"held elsewhere":    f.pausedSession(t, f.video, reporting, `{}`),
	}
	f.exec(t, `INSERT INTO watch_together_rooms (id, code, join_token, host_user_id, host_profile_id, phase, runtime)
		VALUES ($1, $1, $1, $2, 'p-1', 'playing', jsonb_build_object('members', jsonb_build_object('m-1', jsonb_build_object('session_id', $3::text))))`,
		"pt-"+uuid.NewString(), f.account, kept["watch together"])
	// A nil members map is stored as JSON null.
	f.exec(t, `INSERT INTO watch_together_rooms (id, code, join_token, host_user_id, host_profile_id, phase, runtime)
		VALUES ($1, $1, $1, $2, 'p-1', 'playing', '{"members": null}')`, "pt-"+uuid.NewString(), f.account)
	f.syncRow(t, kept["held elsewhere"], 0)

	f.sweep(t, 0)
	if got := f.stopID(t, eligible); got != "" {
		t.Fatalf("a zero timeout ended a session under %s", got)
	}

	f.sweep(t, playback.DefaultPausedSessionTimeoutMinutes)
	for name, session := range map[string]string{"unheld": eligible, "held here": heldHere} {
		if got := f.stopID(t, session); got != pausedTimeoutStopID(session) {
			t.Fatalf("%s: stop id = %q, want the paused timeout's", name, got)
		}
		var state string
		if err := f.pool.QueryRow(t.Context(), `SELECT state FROM playback_session_lifecycle WHERE session_id = $1`, session).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if rows, reason := f.history(t, session); rows != 1 || reason != string(PlaybackEndExpired) || state != "stopped" {
			t.Fatalf("%s: %d history rows ending %s, lifecycle %s; want one expired and stopped", name, rows, reason, state)
		}
	}
	if copyHeld, rowKept := f.held(t, heldHere); copyHeld || rowKept {
		t.Fatalf("the held session's copy kept = %v, live row kept = %v; want both gone", copyHeld, rowKept)
	}
	if len(f.scrobbler.pauses) != 2 || !f.scrobbler.pauses[0].SessionEnded || !f.scrobbler.pauses[1].SessionEnded || len(f.scrobbler.stops) != 0 {
		t.Fatalf("scrobbles = pauses %+v stops %+v; want an ending pause for each", f.scrobbler.pauses, f.scrobbler.stops)
	}
	for name, session := range kept {
		if got := f.stopID(t, session); got != "" {
			t.Errorf("%s: session ended under %s, want it live", name, got)
		}
	}
}

// TestPausedTimeoutSweepDropsCopyItCannotFinishDB pins that a session the
// paused timeout ends here loses its copy at once even when this server
// cannot finish the end (another caller holds the finalization lease): the
// copy and its live row go without history, and a later sweep finishes the
// stopped attempt from its row, recording it once.
func TestPausedTimeoutSweepDropsCopyItCannotFinishDB(t *testing.T) {
	f := newPausedTimeoutSweepFixture(t)
	session := f.pausedSession(t, f.video, 10*time.Second, `{}`)
	f.hold(t, session)

	f.sweepWith(t, playback.DefaultPausedSessionTimeoutMinutes, leaseHeldElsewhereStore{planstore.NewPostgres(f.pool)})
	if got := f.stopID(t, session); got != pausedTimeoutStopID(session) {
		t.Fatalf("stop id = %q, want the paused timeout's", got)
	}
	if copyHeld, rowKept := f.held(t, session); copyHeld || rowKept {
		t.Fatalf("copy kept = %v, live row kept = %v; want both gone", copyHeld, rowKept)
	}
	if rows, _ := f.history(t, session); rows != 0 {
		t.Fatalf("the dropped copy wrote %d history rows, want none", rows)
	}

	f.sweep(t, playback.DefaultPausedSessionTimeoutMinutes)
	f.sweep(t, playback.DefaultPausedSessionTimeoutMinutes)
	if rows, reason := f.history(t, session); rows != 1 || reason != string(PlaybackEndExpired) {
		t.Fatalf("after later sweeps: %d history rows ending %s, want one expired", rows, reason)
	}
}

// TestPausedTimeoutSweepDropsCopiesStoppedElsewhereDB pins that a copy this
// server holds of a session another server's paused timeout stopped goes on
// the next sweep, finishing the end from the copy when it was left
// unfinalized, while a copy whose attempt another kind of end stopped is left
// to that end's own path.
func TestPausedTimeoutSweepDropsCopiesStoppedElsewhereDB(t *testing.T) {
	f := newPausedTimeoutSweepFixture(t)
	pausedOut := f.pausedSession(t, f.video, 10*time.Second, `{}`)
	expired := f.pausedSession(t, f.video, 10*time.Second, `{}`)
	for _, session := range []string{pausedOut, expired} {
		f.hold(t, session)
		f.exec(t, `UPDATE playback_sessions_sync SET last_sync_at = NOW() - interval '1 minute' WHERE session_id = $1`, session)
	}
	f.stopElsewhere(t, pausedOut, pausedTimeoutStopID(pausedOut))
	f.stopElsewhere(t, expired, expiredStopID(expired))

	f.sweep(t, playback.DefaultPausedSessionTimeoutMinutes)
	if copyHeld, _ := f.held(t, pausedOut); copyHeld {
		t.Fatal("the copy of a session paused out elsewhere is still held")
	}
	if rows, reason := f.history(t, pausedOut); rows != 1 || reason != string(PlaybackEndExpired) {
		t.Fatalf("paused out elsewhere: %d history rows ending %s, want one expired", rows, reason)
	}
	if copyHeld, _ := f.held(t, expired); !copyHeld {
		t.Fatal("the paused timeout dropped a copy another kind of end stopped")
	}
}

// TestPausedTimeoutSweepDropsCopiesFinalizedElsewhereDB pins the usual case
// of a copy another server's paused timeout stopped: that server already
// finished the end, so this one drops its copy and live row without writing
// history, and does so even while its own timeout reads zero (the setting
// changed after the other server's claim).
func TestPausedTimeoutSweepDropsCopiesFinalizedElsewhereDB(t *testing.T) {
	for _, minutes := range []int{playback.DefaultPausedSessionTimeoutMinutes, 0} {
		t.Run("timeout_"+strconv.Itoa(minutes), func(t *testing.T) {
			f := newPausedTimeoutSweepFixture(t)
			session := f.pausedSession(t, f.video, 10*time.Second, `{}`)
			f.hold(t, session)
			f.exec(t, `UPDATE playback_sessions_sync SET last_sync_at = NOW() - interval '1 minute' WHERE session_id = $1`, session)
			f.exec(t, `UPDATE playback_v3_attempts SET stopped_at = NOW(), stop_id = $2::uuid,
				stop_receipt = jsonb_build_object('stop_id', $2::text, 'accepted', last_sample, 'finalized', true)
				WHERE session_id = $1`, session, pausedTimeoutStopID(session))
			f.exec(t, `UPDATE playback_session_lifecycle SET state = 'stopped', end_reason = 'expired',
				seq = nextval('playback_session_lifecycle_seq'), updated_at = NOW() WHERE session_id = $1`, session)

			f.sweep(t, minutes)
			if copyHeld, rowKept := f.held(t, session); copyHeld || rowKept {
				t.Fatalf("copy kept = %v, live row kept = %v; want both gone", copyHeld, rowKept)
			}
			if rows, _ := f.history(t, session); rows != 0 {
				t.Fatalf("dropping a copy finalized elsewhere wrote %d history rows, want none", rows)
			}
		})
	}
}

// TestPausedTimeoutSweepPagesPastSkippedRowsDB pins that sessions the sweep
// reads and leaves (audio-only files here) cannot hold an eligible session
// behind them, even when they fill a whole candidate batch.
func TestPausedTimeoutSweepPagesPastSkippedRowsDB(t *testing.T) {
	f := newPausedTimeoutSweepFixture(t)
	skipped := make([]string, 0, lifecycleSweepBatch)
	for range lifecycleSweepBatch {
		skipped = append(skipped, f.pausedSession(t, f.audio, 10*time.Second, `{}`))
	}
	// Older pauses than the eligible one, so they come first.
	f.exec(t, `UPDATE playback_session_lifecycle SET updated_at = NOW() - interval '30 minutes' WHERE session_id = ANY($1::uuid[])`, skipped)
	eligible := f.pausedSession(t, f.video, 10*time.Second, `{}`)

	f.sweep(t, playback.DefaultPausedSessionTimeoutMinutes)
	if got := f.stopID(t, eligible); got != pausedTimeoutStopID(eligible) {
		t.Fatalf("the eligible session behind a batch of skipped ones ended under %q, want the paused timeout's", got)
	}
}

// TestPausedTimeoutSweepWaitsForTheSettingDB pins that a server that could
// not read the timeout yet ends nothing: the default would end sessions an
// administrator kept by turning the timeout off. Once the read succeeds, the
// session ends.
func TestPausedTimeoutSweepWaitsForTheSettingDB(t *testing.T) {
	f := newPausedTimeoutSweepFixture(t)
	session := f.pausedSession(t, f.video, 10*time.Second, `{}`)

	f.settings.getErrors = map[string]error{playback.PausedSessionTimeoutSettingKey: errors.New("settings unavailable")}
	f.sweep(t, 0)
	if got := f.stopID(t, session); got != "" {
		t.Fatalf("a sweep that could not read the timeout ended the session under %s", got)
	}

	f.settings.getErrors = nil
	f.sweep(t, playback.DefaultPausedSessionTimeoutMinutes)
	if got := f.stopID(t, session); got != pausedTimeoutStopID(session) {
		t.Fatalf("stop id = %q after the timeout was read, want the paused timeout's", got)
	}
}
