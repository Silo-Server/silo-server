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

// A buffering summary can name a plan the attempt has since replaced, so its
// delivery comes from the start response or the replan that served it rather
// than the current plan.
func TestSummaryResolvesReplacedPlanDelivery(t *testing.T) {
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

	unique := "plan-delivery-" + uuid.NewString()
	var folderID, userID, fileID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('movies',$1) RETURNING id`, unique).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, unique).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The user cascades the attempt and its replans; the folder cascades the file.
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`, folderID, "/fixtures/"+unique+".mkv").Scan(&fileID); err != nil {
		t.Fatal(err)
	}

	store := NewPostgres(pool)
	session, attempt := uuid.NewString(), uuid.NewString()
	plan := playback.PlanV3{
		ProtocolVersion: 3, PlanID: "plan-1", SessionID: session, DecisionReason: "direct_play",
		RequestedMediaFileID: fileID, EffectiveMediaFileID: fileID, Delivery: playback.DeliveryOriginalHTTPV3,
	}
	startPlan := plan
	record := playback.AttemptRecordV3{
		PlaybackAttemptID: attempt, SessionID: session, UserID: userID, ProfileID: "profile-1",
		RequestedMediaFileID: fileID, EffectiveMediaFileID: fileID, CurrentPlanID: "plan-1", CurrentPlan: plan,
		FrozenRecipe: playback.ExecutableRecipeV3{
			Version: 1, PlanID: "plan-1", PlayMethod: playback.PlayDirect,
			SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
		},
		NormalizedRequest: playback.StartRequestV3{
			ProtocolVersion: 3, FileID: fileID, ProfileID: "profile-1", PlaybackAttemptID: attempt, QualityPreference: "auto",
		},
		RequestDigest: "digest",
		ExpiresAt:     time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond),
		StartResponse: playback.DecisionResponseV3{
			ProtocolVersion: playback.ProtocolV3, ServerFeatures: playback.ServerFeaturesV3(),
			Outcome: playback.OutcomePlayableV3, SessionID: session, PlaybackPlan: &startPlan,
		},
	}
	if err := store.SaveAttempt(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE playback_v3_attempts SET current_plan_id='plan-3',current_plan=jsonb_set(jsonb_set(current_plan,'{plan_id}','"plan-3"'),'{delivery}','"transcode_hls"') WHERE session_id=$1::uuid`, session); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO playback_v3_replans(session_id,replan_request_id,request_digest,state,lease_expires_at,response) VALUES($1::uuid,'replan-1','digest','completed',now(),'{"playback_plan":{"plan_id":"plan-2","delivery":"remux_hls"}}')`, session); err != nil {
		t.Fatal(err)
	}
	for plan, want := range map[string]string{"plan-1": "original_http", "plan-2": "remux_hls", "plan-3": "transcode_hls", "unknown": ""} {
		got, err := store.PlanDelivery(ctx, attempt, plan)
		if err != nil || got != want {
			t.Fatalf("plan %s delivery %q want %q: %v", plan, got, want, err)
		}
	}
}
