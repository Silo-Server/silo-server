package planstore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestProgressCommitsLifecycleStateDB pins the ordering the lifecycle events
// rely on: an accepted progress sample commits the session's announced pause
// state with it, in the client's sequence order, so a sample that arrives
// late changes nothing, and a stopped lifecycle never moves. An idle expiry
// claims the attempt only while no progress landed since its cutoff.
func TestProgressCommitsLifecycleStateDB(t *testing.T) {
	ctx, pool, store, session := newLifecycleAttemptDB(t)
	state := func() (string, int64) {
		t.Helper()
		var s string
		var seq int64
		if err := pool.QueryRow(ctx, `SELECT state, seq FROM playback_session_lifecycle WHERE session_id = $1`, session).Scan(&s, &seq); err != nil {
			t.Fatal(err)
		}
		return s, seq
	}
	apply := func(sequence int64, paused bool) *playback.LifecycleChangeV3 {
		t.Helper()
		receipt, err := store.ApplyProgress(ctx, session, playback.ProgressSampleV3{Sequence: sequence, Position: float64(sequence * 10), IsPaused: paused})
		if err != nil {
			t.Fatal(err)
		}
		return receipt.Lifecycle
	}

	_, startSeq := state()
	paused := apply(1, true)
	if paused == nil || !paused.Paused || paused.Seq <= startSeq {
		t.Fatalf("pause = %+v, want a paused change after %d", paused, startSeq)
	}
	resumed := apply(3, false)
	if resumed == nil || resumed.Paused || resumed.Seq <= paused.Seq {
		t.Fatalf("resume = %+v, want a playing change after %d", resumed, paused.Seq)
	}
	if late := apply(2, true); late != nil {
		t.Fatalf("a sample older than the accepted one changed the state: %+v", late)
	}
	if same := apply(4, false); same != nil {
		t.Fatalf("a sample with the same state announced a change: %+v", same)
	}
	if got, seq := state(); got != "playing" || seq != resumed.Seq {
		t.Fatalf("lifecycle = %s at %d, want playing at %d", got, seq, resumed.Seq)
	}
	if _, err := pool.Exec(ctx, `UPDATE playback_session_lifecycle SET state = 'stopped', end_reason = 'stopped' WHERE session_id = $1`, session); err != nil {
		t.Fatal(err)
	}
	if after := apply(5, true); after != nil {
		t.Fatalf("a stopped lifecycle moved: %+v", after)
	}

	stopID := uuid.NewString()
	if expired, err := store.ExpireIdleAttempt(ctx, session, stopID, time.Now().Add(-time.Hour)); err != nil || expired {
		t.Fatalf("expire with fresh progress = %v, %v; want false", expired, err)
	}
	if expired, err := store.ExpireIdleAttempt(ctx, session, stopID, time.Now().Add(time.Minute)); err != nil || !expired {
		t.Fatalf("expire after the cutoff = %v, %v; want true", expired, err)
	}
	if expired, err := store.ExpireIdleAttempt(ctx, session, stopID, time.Now().Add(time.Minute)); err != nil || expired {
		t.Fatalf("second expire = %v, %v; want false", expired, err)
	}
	receipt, first, err := store.StopAttempt(ctx, session, uuid.NewString(), nil)
	if err != nil || first || receipt.StopID != stopID || receipt.Accepted == nil || receipt.Accepted.Sequence != 5 {
		t.Fatalf("stop after expiry = %+v first=%v err=%v; want the expiry's receipt at sequence 5", receipt, first, err)
	}
}

func TestLatePlaybackProgressCannotOverwriteFinalStopDB(t *testing.T) {
	ctx, pool, attempts, session := newLifecycleAttemptDB(t)
	attempt, err := attempts.GetAttempt(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	provider := notifications.WrapUserStoreProvider(pgstore.NewPostgresProvider(pool), &notifications.System{})
	store, err := provider.ForUser(ctx, attempt.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: attempt.ProfileID, Name: "Viewer"}); err != nil {
		t.Fatal(err)
	}
	writer, ok := store.(userstore.SequencedPlaybackProgressWriter)
	if !ok {
		t.Fatal("production user-store wrapper lost the sequenced progress writer")
	}
	const mediaID = "movie:stop-fence"
	const duration = 3600
	thresholds := userstore.ProgressThresholds{}
	hints := userstore.VersionHints{FileID: attempt.RequestedMediaFileID}
	assertPosition := func(want float64) {
		t.Helper()
		saved, err := store.GetProgress(ctx, attempt.ProfileID, mediaID)
		if err != nil || saved == nil || saved.PositionSeconds != want {
			t.Fatalf("saved progress = %+v, %v, want %v", saved, err, want)
		}
	}
	if _, err := attempts.ApplyProgress(ctx, session, playback.ProgressSampleV3{Sequence: 1, Position: 600}); err != nil {
		t.Fatal(err)
	}
	writeHeartbeat := func(wantWritten bool) {
		t.Helper()
		written, _, err := writer.WriteSequencedPlaybackProgress(ctx, session, 1, attempt.ProfileID, mediaID, 600, duration, thresholds, hints)
		if err != nil || written != wantWritten {
			t.Fatalf("heartbeat write = %v, %v, want %v", written, err, wantWritten)
		}
	}
	writeHeartbeat(true)
	assertPosition(600)

	// Another replica wins the stop and stores its final position while
	// the first replica still has an accepted heartbeat to persist.
	final := playback.ProgressSampleV3{Sequence: 2, Position: 900}
	stopID := "c2c87108-1bb1-4d1b-b25d-c37d928a9bd7"
	if _, first, err := attempts.StopAttempt(ctx, session, stopID, &final); err != nil || !first {
		t.Fatalf("stop was not accepted: first=%v, %v", first, err)
	}
	if err := store.SetProgress(ctx, attempt.ProfileID, mediaID, final.Position, duration, thresholds); err != nil {
		t.Fatal(err)
	}
	writeHeartbeat(false)
	assertPosition(900)
}

func TestPlaybackStopCommitsResumeAndEpisodeSpanOnceDB(t *testing.T) {
	ctx, pool, attempts, session := newLifecycleAttemptDB(t)
	attempt, err := attempts.GetAttempt(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	provider := notifications.WrapUserStoreProvider(pgstore.NewPostgresProvider(pool), &notifications.System{})
	store, err := provider.ForUser(ctx, attempt.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: attempt.ProfileID, Name: "Viewer"}); err != nil {
		t.Fatal(err)
	}

	const first = "episode:span-start"
	const second = "episode:span-end"
	history := userstore.WatchHistoryEntry{
		ID: uuid.NewString(), ProfileID: attempt.ProfileID, MediaItemID: first, DurationSeconds: 3600,
		Completed: true, Source: userstore.WatchHistorySourcePlayback,
	}
	spanHistory := history
	spanHistory.MediaItemID = second
	stop := userstore.PlaybackStopWrite{
		Entry: history, PositionSeconds: 3590, Hints: userstore.VersionHints{FileID: attempt.RequestedMediaFileID},
		ReplayUntil:    time.Now().Add(time.Hour),
		SpannedTargets: []userstore.MarkWatchedTarget{{MediaItemID: second}},
		SpannedHistory: []userstore.WatchHistoryEntry{spanHistory},
	}
	assertHistoryCount := func(want int) {
		t.Helper()
		var count int
		err := pool.QueryRow(ctx, "SELECT count(*) FROM user_watch_history WHERE user_id = $1", attempt.UserID).Scan(&count)
		if err != nil || count != want {
			t.Fatalf("history count = %d, %v, want %d", count, err, want)
		}
	}
	// The duplicate ID fails during the span's history insert, after both
	// progress writes. None of those writes may survive that failure.
	if _, _, err := userstore.CommitPlaybackStop(ctx, store, stop); err == nil {
		t.Fatal("expected span history insert to fail")
	}
	for _, id := range []string{first, second} {
		if saved, err := store.GetProgress(ctx, attempt.ProfileID, id); err != nil || saved != nil {
			t.Fatalf("failed stop left %s progress: %+v, %v", id, saved, err)
		}
	}
	assertHistoryCount(0)

	stop.SpannedHistory[0].ID = uuid.NewString()
	spanned, replay, err := userstore.CommitPlaybackStop(ctx, store, stop)
	if err != nil || replay || len(spanned) != 1 {
		t.Fatalf("retried stop changed %d span entries, replay=%v, err=%v", len(spanned), replay, err)
	}
	for _, id := range []string{first, second} {
		if saved, err := store.GetProgress(ctx, attempt.ProfileID, id); err != nil || saved == nil || !saved.Completed || saved.PositionSeconds != 0 {
			t.Fatalf("successful stop left %s progress: %+v, %v", id, saved, err)
		}
	}
	assertHistoryCount(2)

	// History removal can happen after commit and before the receipt is saved.
	// A replay must preserve the removal and the next play's resume point.
	if err := store.RemoveHistoryItems(ctx, attempt.ProfileID, []string{first}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetProgress(ctx, attempt.ProfileID, first, 600, 3600, stop.Thresholds); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := userstore.CommitPlaybackStop(ctx, store, stop); err != nil || !replay {
		t.Fatalf("committed stop replay = %v, %v", replay, err)
	}
	if saved, err := store.GetProgress(ctx, attempt.ProfileID, first); err != nil || saved == nil || saved.PositionSeconds != 600 {
		t.Fatalf("stop replay changed newer resume point: %+v, %v", saved, err)
	}
	assertHistoryCount(1)
}

// newLifecycleAttemptDB saves a live attempt and a playing native lifecycle
// row for one session.
func newLifecycleAttemptDB(t *testing.T) (context.Context, *pgxpool.Pool, *Postgres, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	unique := "lifecycle-progress-" + uuid.NewString()
	var folderID, userID, fileID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('movies',$1) RETURNING id`, unique).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, unique).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`, folderID, "/fixtures/"+unique+".mkv").Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	store := NewPostgres(pool)
	session, attempt := uuid.NewString(), uuid.NewString()
	plan := playback.PlanV3{ProtocolVersion: 3, PlanID: "plan-1", SessionID: session, RequestedMediaFileID: fileID, EffectiveMediaFileID: fileID, Delivery: playback.DeliveryOriginalHTTPV3}
	if err := store.SaveAttempt(ctx, playback.AttemptRecordV3{
		PlaybackAttemptID: attempt, SessionID: session, UserID: userID, ProfileID: "profile-1",
		RequestedMediaFileID: fileID, EffectiveMediaFileID: fileID, CurrentPlanID: "plan-1", CurrentPlan: plan,
		FrozenRecipe:      playback.ExecutableRecipeV3{Version: 1, PlanID: "plan-1", PlayMethod: playback.PlayDirect, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1},
		NormalizedRequest: playback.StartRequestV3{ProtocolVersion: 3, FileID: fileID, ProfileID: "profile-1", PlaybackAttemptID: attempt},
		ExpiresAt:         time.Now().Add(time.Hour),
		StartResponse:     playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, Outcome: playback.OutcomePlayableV3, SessionID: session, PlaybackPlan: &plan},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO playback_session_lifecycle
		(session_id, frontend, state, seq, user_id, profile_id, media_file_id, requested_media_file_id, started_at)
		VALUES ($1, 'native', 'playing', nextval('playback_session_lifecycle_seq'), $2, 'profile-1', $3, $3, NOW())`,
		session, userID, fileID); err != nil {
		t.Fatal(err)
	}
	return ctx, pool, store, session
}
