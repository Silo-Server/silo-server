package handlers

import (
	"context"
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
}

func newPausedTimeoutSweepFixture(t *testing.T) *pausedTimeoutSweepFixture {
	t.Helper()
	ctx, pool, account := lifecycleTestDB(t)
	f := &pausedTimeoutSweepFixture{pool: pool, account: account,
		video: lifecycleTestFile(t, ctx, pool), audio: lifecycleTestFile(t, ctx, pool),
		settings:  &mutablePlaybackSettingsV3{values: map[string]string{}},
		scrobbler: &recordingPlaybackWatchScrobbler{}}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM playback_session_lifecycle WHERE user_id = $1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM watch_together_rooms WHERE host_user_id = $1`, account)
	})
	return f
}

// sweep runs one lifecycle sweep on a server that holds no session, with the
// paused-session timeout set to minutes.
func (f *pausedTimeoutSweepFixture) sweep(t *testing.T, minutes int) {
	f.settings.values = map[string]string{playback.PausedSessionTimeoutSettingKey: strconv.Itoa(minutes)}
	h := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: map[int]*models.MediaFile{
		f.video: {ID: f.video, ContentID: "movie:paused-timeout", Duration: 3600, CodecVideo: "h264", CodecAudio: "aac"},
		f.audio: {ID: f.audio, ContentID: "audiobook:paused-timeout", Duration: 3600, BaseType: "audiobook", CodecAudio: "aac"},
	}})
	h.PlanStoreV3 = planstore.NewPostgres(f.pool)
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

// TestPausedTimeoutSweepEndsPausedSessionsDB pins which sessions the paused
// timeout ends: one paused past it whose client still reports and that no
// other server holds ends as expired with a pause scrobble, and Watch
// Together members, Cast receivers, audiobooks, clients that stopped
// reporting and sessions another server holds keep going. A timeout of zero
// ends nothing.
func TestPausedTimeoutSweepEndsPausedSessionsDB(t *testing.T) {
	f := newPausedTimeoutSweepFixture(t)
	reporting := 10 * time.Second
	eligible := f.pausedSession(t, f.video, reporting, `{}`)
	kept := map[string]string{
		"cast receiver": f.pausedSession(t, f.video, reporting, `{"client_playback_context":{"output":{"current_sink":"cast_receiver"}}}`),
		"audiobook":     f.pausedSession(t, f.audio, reporting, `{}`),
		// Quiet longer than PausedStreamSlotGrace: the idle expiry's, later.
		"stopped reporting": f.pausedSession(t, f.video, 3*time.Minute, `{}`),
		"watch together":    f.pausedSession(t, f.video, reporting, `{}`),
		"held elsewhere":    f.pausedSession(t, f.video, reporting, `{}`),
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO watch_together_rooms (id, code, join_token, host_user_id, host_profile_id, phase, runtime)
		VALUES ($1, $1, $1, $2, 'p-1', 'playing', jsonb_build_object('members', jsonb_build_object('m-1', jsonb_build_object('session_id', $3::text))))`,
		"pt-"+uuid.NewString(), f.account, kept["watch together"]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO playback_sessions_sync (session_id, user_id, profile_id, media_file_id, last_sync_at)
		VALUES ($1, $2, 'p-1', $3, NOW())`, kept["held elsewhere"], f.account, f.video); err != nil {
		t.Fatal(err)
	}

	f.sweep(t, 0)
	if got := f.stopID(t, eligible); got != "" {
		t.Fatalf("a zero timeout ended a session under %s", got)
	}

	f.sweep(t, playback.DefaultPausedSessionTimeoutMinutes)
	if got := f.stopID(t, eligible); got != pausedTimeoutStopID(eligible) {
		t.Fatalf("the eligible session's stop id = %q, want the paused timeout's", got)
	}
	var reason, state string
	if err := f.pool.QueryRow(t.Context(), `SELECT h.end_reason, lc.state FROM admin_playback_history h
		JOIN playback_session_lifecycle lc ON lc.session_id::text = h.session_id WHERE h.session_id = $1`, eligible).Scan(&reason, &state); err != nil {
		t.Fatalf("the ended session has no history row: %v", err)
	}
	if reason != string(PlaybackEndExpired) || state != "stopped" {
		t.Fatalf("ended session = %s, lifecycle %s; want expired and stopped", reason, state)
	}
	if len(f.scrobbler.pauses) != 1 || !f.scrobbler.pauses[0].SessionEnded || len(f.scrobbler.stops) != 0 {
		t.Fatalf("scrobbles = pauses %+v stops %+v; want one ending pause", f.scrobbler.pauses, f.scrobbler.stops)
	}
	for name, session := range kept {
		if got := f.stopID(t, session); got != "" {
			t.Errorf("%s: session ended under %s, want it live", name, got)
		}
	}
}
