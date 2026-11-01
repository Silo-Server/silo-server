package planstore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/playback"
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
