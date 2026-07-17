package planstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// planstoreFixture holds the minimal FK graph (user + media files) that
// playback_v3_attempts and playback_route_events rows require.
type planstoreFixture struct {
	pool        *pgxpool.Pool
	userID      int
	mediaFileID int
	altFileID   int
}

// newPlanstoreFixture connects to SILO_TEST_DATABASE_URL (skipping when
// unset), verifies the v3 migrations are applied, and inserts the fixture
// rows every attempt/event insert depends on. Cleanup deletes everything the
// tests wrote so reruns against the same database stay green.
func newPlanstoreFixture(t *testing.T) *planstoreFixture {
	t.Helper()
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

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.playback_v3_attempts')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check playback_v3_attempts table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied the playback protocol v3 migration")
	}
	var hasRevision bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'playback_v3_attempts' AND column_name = 'current_replan_request_id'
		)`).Scan(&hasRevision); err != nil {
		t.Fatalf("check current_replan_request_id column: %v", err)
	}
	if !hasRevision {
		t.Skip("test database has not applied the playback v3 attempt revision migration")
	}

	f := &planstoreFixture{pool: pool}
	unique := fmt.Sprintf("planstore-test-%d", time.Now().UnixNano())

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name) VALUES ('movies', $1) RETURNING id`, unique).Scan(&folderID); err != nil {
		t.Fatalf("insert fixture media folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username) VALUES ($1) RETURNING id`, unique).Scan(&f.userID); err != nil {
		t.Fatalf("insert fixture user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path) VALUES ($1, $2) RETURNING id`,
		folderID, "/fixtures/"+unique+"/movie.mkv").Scan(&f.mediaFileID); err != nil {
		t.Fatalf("insert fixture media file: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path) VALUES ($1, $2) RETURNING id`,
		folderID, "/fixtures/"+unique+"/movie-alt.mkv").Scan(&f.altFileID); err != nil {
		t.Fatalf("insert alternate fixture media file: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Attempts cascade to replans; deleting the user cascades any attempt
		// or route event a failed subtest left behind; the folder cascades the
		// media files.
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM playback_route_events WHERE user_id = $1`, f.userID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM playback_v3_attempts WHERE user_id = $1`, f.userID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, f.userID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	return f
}

func (f *planstoreFixture) attemptRecord(sessionID, attemptID, digest string) playback.AttemptRecordV3 {
	return playback.AttemptRecordV3{
		PlaybackAttemptID:      attemptID,
		SessionID:              sessionID,
		UserID:                 f.userID,
		ProfileID:              "profile-1",
		RequestedMediaFileID:   f.mediaFileID,
		EffectiveMediaFileID:   f.mediaFileID,
		CurrentPlanID:          "plan-1",
		CurrentReplanRequestID: "",
		CurrentPlan: playback.PlanV3{
			ProtocolVersion:      3,
			PlanID:               "plan-1",
			SessionID:            sessionID,
			DecisionReason:       "direct_play",
			RequestedMediaFileID: f.mediaFileID,
			EffectiveMediaFileID: f.mediaFileID,
		},
		NormalizedRequest: playback.StartRequestV3{
			ProtocolVersion:   3,
			FileID:            f.mediaFileID,
			ProfileID:         "profile-1",
			PlaybackAttemptID: attemptID,
			QualityPreference: "auto",
		},
		RequestDigest: digest,
		ExpiresAt:     time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond),
	}
}

func (f *planstoreFixture) expireAttempt(t *testing.T, attemptID string) {
	t.Helper()
	tag, err := f.pool.Exec(context.Background(), `
		UPDATE playback_v3_attempts SET expires_at = NOW() - INTERVAL '1 minute'
		WHERE playback_attempt_id = $1`, attemptID)
	if err != nil {
		t.Fatalf("expire attempt %s: %v", attemptID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("expire attempt %s: affected %d rows", attemptID, tag.RowsAffected())
	}
}

// mustJSON canonicalizes a value through encoding/json so structs that
// round-trip via JSONB can be compared without tripping on nil-vs-empty
// map/slice differences.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestPostgresPlanStore(t *testing.T) {
	f := newPlanstoreFixture(t)
	store := NewPostgres(f.pool)
	ctx := context.Background()

	// Regression test for the CHECK-constraint drift: every event name the
	// code emits must be accepted by the playback_route_events CHECK in the
	// real schema.
	t.Run("RecordRouteEventAcceptsAllEventNames", func(t *testing.T) {
		sessionID := uuid.NewString()
		names := playback.RouteEventNamesV3()
		if len(names) == 0 {
			t.Fatal("RouteEventNamesV3 returned no events")
		}
		for _, name := range names {
			err := store.RecordRouteEvent(ctx, playback.RouteEventRecordV3{
				RouteEventV3: playback.RouteEventV3{
					ProtocolVersion:       3,
					PlaybackAttemptID:     "att-events-" + sessionID,
					SessionID:             sessionID,
					PlanID:                "plan-1",
					PlanAttemptID:         "plan-attempt-1",
					PlanAttemptKey:        "plan-attempt-key-1",
					Event:                 name,
					FailureClassification: "decode_error",
					FallbackReason:        "test",
					OutputRouteGeneration: 1,
					Diagnostics:           map[string]string{"source": "planstore-test"},
				},
				UserID:        f.userID,
				ProfileID:     "profile-1",
				ClientName:    "planstore-test",
				ClientVersion: "1.0",
				ClientModel:   "test-model",
			})
			if err != nil {
				t.Errorf("RecordRouteEvent(%q) rejected by real schema: %v", name, err)
			}
		}
		var count int
		if err := f.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM playback_route_events WHERE session_id = $1::uuid`, sessionID).Scan(&count); err != nil {
			t.Fatalf("count route events: %v", err)
		}
		if count != len(names) {
			t.Fatalf("persisted %d route events, want %d", count, len(names))
		}
	})

	t.Run("SaveAttemptIdempotency", func(t *testing.T) {
		sessionID := uuid.NewString()
		attemptID := "att-save-" + sessionID
		record := f.attemptRecord(sessionID, attemptID, "digest-a")

		if err := store.SaveAttempt(ctx, record); err != nil {
			t.Fatalf("fresh SaveAttempt: %v", err)
		}

		// Exact replay of the same attempt-ID and digest.
		if err := store.SaveAttempt(ctx, record); !errors.Is(err, playback.ErrPlaybackAttemptExistsV3) {
			t.Fatalf("same-digest replay: got %v, want ErrPlaybackAttemptExistsV3", err)
		}

		// Same attempt-ID reused with different input (digest) is an
		// idempotency violation, not a replay.
		conflicting := f.attemptRecord(uuid.NewString(), attemptID, "digest-b")
		if err := store.SaveAttempt(ctx, conflicting); !errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
			t.Fatalf("different-digest reuse: got %v, want ErrIdempotencyKeyReusedV3", err)
		}

		// Once the original row expires, the pre-delete path must clear it so
		// the attempt-ID becomes reusable.
		f.expireAttempt(t, attemptID)
		if err := store.SaveAttempt(ctx, record); err != nil {
			t.Fatalf("SaveAttempt after expiry should reclaim the attempt-ID: %v", err)
		}
	})

	t.Run("GetAttemptRoundTrip", func(t *testing.T) {
		sessionID := uuid.NewString()
		attemptID := "att-get-" + sessionID
		record := f.attemptRecord(sessionID, attemptID, "digest-get")
		if err := store.SaveAttempt(ctx, record); err != nil {
			t.Fatalf("SaveAttempt: %v", err)
		}

		for name, fetch := range map[string]func() (*playback.AttemptRecordV3, error){
			"GetAttempt":                    func() (*playback.AttemptRecordV3, error) { return store.GetAttempt(ctx, sessionID) },
			"GetAttemptByPlaybackAttemptID": func() (*playback.AttemptRecordV3, error) { return store.GetAttemptByPlaybackAttemptID(ctx, attemptID) },
		} {
			got, err := fetch()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got.PlaybackAttemptID != attemptID || got.SessionID != sessionID {
				t.Fatalf("%s identity mismatch: %+v", name, got)
			}
			if got.UserID != record.UserID || got.ProfileID != record.ProfileID {
				t.Fatalf("%s ownership mismatch: %+v", name, got)
			}
			if got.RequestedMediaFileID != record.RequestedMediaFileID || got.EffectiveMediaFileID != record.EffectiveMediaFileID {
				t.Fatalf("%s media file mismatch: %+v", name, got)
			}
			if got.CurrentPlanID != record.CurrentPlanID || got.CurrentReplanRequestID != record.CurrentReplanRequestID {
				t.Fatalf("%s plan revision mismatch: %+v", name, got)
			}
			if got.RequestDigest != record.RequestDigest {
				t.Fatalf("%s request_digest = %q, want %q", name, got.RequestDigest, record.RequestDigest)
			}
			if !bytes.Equal(mustJSON(t, got.CurrentPlan), mustJSON(t, record.CurrentPlan)) {
				t.Fatalf("%s plan JSON did not round-trip:\n got %s\nwant %s", name, mustJSON(t, got.CurrentPlan), mustJSON(t, record.CurrentPlan))
			}
			if !bytes.Equal(mustJSON(t, got.NormalizedRequest), mustJSON(t, record.NormalizedRequest)) {
				t.Fatalf("%s normalized request JSON did not round-trip", name)
			}
			if diff := got.ExpiresAt.Sub(record.ExpiresAt); diff < -time.Millisecond || diff > time.Millisecond {
				t.Fatalf("%s expires_at drifted by %v", name, diff)
			}
		}

		identity, err := store.GetAttemptIdentity(ctx, sessionID)
		if err != nil {
			t.Fatalf("GetAttemptIdentity: %v", err)
		}
		byAttempt, err := store.GetAttemptIdentityByPlaybackAttemptID(ctx, attemptID)
		if err != nil {
			t.Fatalf("GetAttemptIdentityByPlaybackAttemptID: %v", err)
		}
		for name, got := range map[string]*playback.AttemptIdentityV3{"bySession": identity, "byAttempt": byAttempt} {
			if got.PlaybackAttemptID != attemptID || got.SessionID != sessionID ||
				got.UserID != f.userID || got.ProfileID != "profile-1" {
				t.Fatalf("%s identity ownership mismatch: %+v", name, got)
			}
		}

		// Expired rows must be invisible to every read path.
		f.expireAttempt(t, attemptID)
		if _, err := store.GetAttempt(ctx, sessionID); !errors.Is(err, playback.ErrSessionNotFound) {
			t.Fatalf("GetAttempt on expired row: got %v, want ErrSessionNotFound", err)
		}
		if _, err := store.GetAttemptByPlaybackAttemptID(ctx, attemptID); !errors.Is(err, playback.ErrSessionNotFound) {
			t.Fatalf("GetAttemptByPlaybackAttemptID on expired row: got %v, want ErrSessionNotFound", err)
		}
		if _, err := store.GetAttemptIdentity(ctx, sessionID); !errors.Is(err, playback.ErrSessionNotFound) {
			t.Fatalf("GetAttemptIdentity on expired row: got %v, want ErrSessionNotFound", err)
		}
		if _, err := store.GetAttemptIdentityByPlaybackAttemptID(ctx, attemptID); !errors.Is(err, playback.ErrSessionNotFound) {
			t.Fatalf("GetAttemptIdentityByPlaybackAttemptID on expired row: got %v, want ErrSessionNotFound", err)
		}
	})

	t.Run("BeginReplanLifecycle", func(t *testing.T) {
		sessionID := uuid.NewString()
		attemptID := "att-replan-" + sessionID
		if err := store.SaveAttempt(ctx, f.attemptRecord(sessionID, attemptID, "digest-replan")); err != nil {
			t.Fatalf("SaveAttempt: %v", err)
		}
		future := time.Now().Add(time.Minute)

		// New replan request: caller owns the lease.
		lease, err := store.BeginReplan(ctx, sessionID, "rq-1", "rq-digest-1", "", future)
		if err != nil {
			t.Fatalf("BeginReplan new: %v", err)
		}
		if lease.State != playback.ReplanLeaseOwnedV3 {
			t.Fatalf("BeginReplan new state = %q, want owned", lease.State)
		}

		// Same request-ID with different input.
		if _, err := store.BeginReplan(ctx, sessionID, "rq-1", "rq-digest-other", "", future); !errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
			t.Fatalf("digest mismatch: got %v, want ErrIdempotencyKeyReusedV3", err)
		}

		// Active unexpired lease held by someone else.
		lease, err = store.BeginReplan(ctx, sessionID, "rq-1", "rq-digest-1", "", future)
		if err != nil {
			t.Fatalf("BeginReplan in-flight: %v", err)
		}
		if lease.State != playback.ReplanLeaseInFlightV3 {
			t.Fatalf("BeginReplan in-flight state = %q, want in_flight", lease.State)
		}

		// Completed replan replays the stored response.
		completed := f.attemptRecord(sessionID, attemptID, "digest-replan")
		completed.CurrentPlanID = "plan-2"
		completed.CurrentReplanRequestID = "rq-1"
		completed.CurrentPlan.PlanID = "plan-2"
		response := json.RawMessage(`{"plan_id": "plan-2"}`)
		if err := store.CompleteReplan(ctx, sessionID, "rq-1", "", response, completed); err != nil {
			t.Fatalf("CompleteReplan: %v", err)
		}
		lease, err = store.BeginReplan(ctx, sessionID, "rq-1", "rq-digest-1", "", future)
		if err != nil {
			t.Fatalf("BeginReplan completed: %v", err)
		}
		if lease.State != playback.ReplanLeaseCompletedV3 {
			t.Fatalf("BeginReplan completed state = %q, want completed", lease.State)
		}
		var storedResponse, wantResponse any
		if err := json.Unmarshal(lease.Response, &storedResponse); err != nil {
			t.Fatalf("unmarshal replayed response: %v", err)
		}
		if err := json.Unmarshal(response, &wantResponse); err != nil {
			t.Fatalf("unmarshal expected response: %v", err)
		}
		if !bytes.Equal(mustJSON(t, storedResponse), mustJSON(t, wantResponse)) {
			t.Fatalf("replayed response = %s, want %s", lease.Response, response)
		}

		// Expired lease whose base revision no longer matches the retry.
		past := time.Now().Add(-time.Minute)
		if _, err := store.BeginReplan(ctx, sessionID, "rq-stale", "rq-digest-stale", "base-x", past); err != nil {
			t.Fatalf("BeginReplan seed stale lease: %v", err)
		}
		if _, err := store.BeginReplan(ctx, sessionID, "rq-stale", "rq-digest-stale", "base-y", future); !errors.Is(err, playback.ErrStaleReplanLeaseV3) {
			t.Fatalf("expired lease with stale base: got %v, want ErrStaleReplanLeaseV3", err)
		}

		// Expired lease with a matching base is re-owned.
		if _, err := store.BeginReplan(ctx, sessionID, "rq-retry", "rq-digest-retry", "rq-1", past); err != nil {
			t.Fatalf("BeginReplan seed expired lease: %v", err)
		}
		lease, err = store.BeginReplan(ctx, sessionID, "rq-retry", "rq-digest-retry", "rq-1", future)
		if err != nil {
			t.Fatalf("BeginReplan re-own expired lease: %v", err)
		}
		if lease.State != playback.ReplanLeaseOwnedV3 {
			t.Fatalf("re-owned lease state = %q, want owned", lease.State)
		}
	})

	t.Run("CompleteReplan", func(t *testing.T) {
		sessionID := uuid.NewString()
		attemptID := "att-complete-" + sessionID
		if err := store.SaveAttempt(ctx, f.attemptRecord(sessionID, attemptID, "digest-complete")); err != nil {
			t.Fatalf("SaveAttempt: %v", err)
		}
		future := time.Now().Add(time.Minute)
		if _, err := store.BeginReplan(ctx, sessionID, "rq-1", "rq-digest-1", "", future); err != nil {
			t.Fatalf("BeginReplan: %v", err)
		}

		updated := f.attemptRecord(sessionID, attemptID, "digest-complete")
		updated.EffectiveMediaFileID = f.altFileID
		updated.CurrentPlanID = "plan-2"
		updated.CurrentReplanRequestID = "rq-1"
		updated.CurrentPlan.PlanID = "plan-2"
		updated.CurrentPlan.EffectiveMediaFileID = f.altFileID
		updated.CurrentPlan.DecisionReason = "transcode_fallback"
		updated.ExpiresAt = time.Now().Add(2 * time.Hour).UTC().Truncate(time.Microsecond)
		response := json.RawMessage(`{"plan_id": "plan-2", "status": "replanned"}`)

		if err := store.CompleteReplan(ctx, sessionID, "rq-1", "", response, updated); err != nil {
			t.Fatalf("CompleteReplan happy path: %v", err)
		}

		got, err := store.GetAttempt(ctx, sessionID)
		if err != nil {
			t.Fatalf("GetAttempt after replan: %v", err)
		}
		if got.CurrentReplanRequestID != "rq-1" {
			t.Fatalf("current_replan_request_id = %q, want rq-1", got.CurrentReplanRequestID)
		}
		if got.EffectiveMediaFileID != f.altFileID {
			t.Fatalf("effective_media_file_id = %d, want %d", got.EffectiveMediaFileID, f.altFileID)
		}
		if got.CurrentPlanID != "plan-2" || got.CurrentPlan.PlanID != "plan-2" || got.CurrentPlan.DecisionReason != "transcode_fallback" {
			t.Fatalf("plan did not round-trip through replan: %+v", got.CurrentPlan)
		}
		if !bytes.Equal(mustJSON(t, got.CurrentPlan), mustJSON(t, updated.CurrentPlan)) {
			t.Fatalf("plan JSON mismatch after replan:\n got %s\nwant %s", mustJSON(t, got.CurrentPlan), mustJSON(t, updated.CurrentPlan))
		}

		// The migration's sync trigger must not fight the in-transaction CAS:
		// the raw column must equal the new request ID, with no extra rewrite.
		var rawRevision, replanState string
		if err := f.pool.QueryRow(ctx, `
			SELECT a.current_replan_request_id, r.state
			FROM playback_v3_attempts a
			JOIN playback_v3_replans r ON r.session_id = a.session_id AND r.replan_request_id = $2
			WHERE a.session_id = $1::uuid`, sessionID, "rq-1").Scan(&rawRevision, &replanState); err != nil {
			t.Fatalf("inspect attempt/replan rows: %v", err)
		}
		if rawRevision != "rq-1" {
			t.Fatalf("raw current_replan_request_id = %q, want rq-1", rawRevision)
		}
		if replanState != "completed" {
			t.Fatalf("replan state = %q, want completed", replanState)
		}

		// A second replan whose base does not match the current revision must
		// lose the compare-and-swap.
		if _, err := store.BeginReplan(ctx, sessionID, "rq-2", "rq-digest-2", "rq-1", future); err != nil {
			t.Fatalf("BeginReplan second: %v", err)
		}
		stale := updated
		stale.CurrentReplanRequestID = "rq-2"
		if err := store.CompleteReplan(ctx, sessionID, "rq-2", "wrong-base", response, stale); !errors.Is(err, playback.ErrReplanSupersededV3) {
			t.Fatalf("CompleteReplan wrong base: got %v, want ErrReplanSupersededV3", err)
		}

		// Unknown session.
		if err := store.CompleteReplan(ctx, uuid.NewString(), "rq-1", "", response, updated); !errors.Is(err, playback.ErrSessionNotFound) {
			t.Fatalf("CompleteReplan missing session: got %v, want ErrSessionNotFound", err)
		}
	})

	t.Run("CleanupExpired", func(t *testing.T) {
		sessionID := uuid.NewString()
		attemptID := "att-cleanup-" + sessionID
		if err := store.SaveAttempt(ctx, f.attemptRecord(sessionID, attemptID, "digest-cleanup")); err != nil {
			t.Fatalf("SaveAttempt: %v", err)
		}
		if _, err := store.BeginReplan(ctx, sessionID, "rq-1", "rq-digest-1", "", time.Now().Add(time.Minute)); err != nil {
			t.Fatalf("BeginReplan: %v", err)
		}

		// A survivor attempt that must not be swept.
		keepSession := uuid.NewString()
		keepAttempt := "att-keep-" + keepSession
		if err := store.SaveAttempt(ctx, f.attemptRecord(keepSession, keepAttempt, "digest-keep")); err != nil {
			t.Fatalf("SaveAttempt survivor: %v", err)
		}

		event := func(attempt string) playback.RouteEventRecordV3 {
			return playback.RouteEventRecordV3{
				RouteEventV3: playback.RouteEventV3{
					ProtocolVersion:   3,
					PlaybackAttemptID: attempt,
					SessionID:         sessionID,
					Event:             playback.RouteEventNamesV3()[0],
					Diagnostics:       map[string]string{},
				},
				UserID:    f.userID,
				ProfileID: "profile-1",
			}
		}
		if err := store.RecordRouteEvent(ctx, event("att-cleanup-old")); err != nil {
			t.Fatalf("RecordRouteEvent old: %v", err)
		}
		if err := store.RecordRouteEvent(ctx, event("att-cleanup-recent")); err != nil {
			t.Fatalf("RecordRouteEvent recent: %v", err)
		}
		if _, err := f.pool.Exec(ctx, `
			UPDATE playback_route_events SET received_at = NOW() - INTERVAL '31 days'
			WHERE playback_attempt_id = 'att-cleanup-old'`); err != nil {
			t.Fatalf("age route event: %v", err)
		}

		f.expireAttempt(t, attemptID)
		removed, err := store.CleanupExpired(ctx, time.Now())
		if err != nil {
			t.Fatalf("CleanupExpired: %v", err)
		}
		if removed < 1 {
			t.Fatalf("CleanupExpired removed %d attempts, want at least 1", removed)
		}

		var attempts, replans, oldEvents, recentEvents int
		if err := f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM playback_v3_attempts WHERE playback_attempt_id = $1`, attemptID).Scan(&attempts); err != nil {
			t.Fatalf("count attempts: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM playback_v3_replans WHERE session_id = $1::uuid`, sessionID).Scan(&replans); err != nil {
			t.Fatalf("count replans: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM playback_route_events WHERE playback_attempt_id = 'att-cleanup-old'`).Scan(&oldEvents); err != nil {
			t.Fatalf("count old events: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM playback_route_events WHERE playback_attempt_id = 'att-cleanup-recent'`).Scan(&recentEvents); err != nil {
			t.Fatalf("count recent events: %v", err)
		}
		if attempts != 0 {
			t.Fatalf("expired attempt survived cleanup")
		}
		if replans != 0 {
			t.Fatalf("replans did not cascade with the expired attempt")
		}
		if oldEvents != 0 {
			t.Fatalf("31-day-old route event survived cleanup")
		}
		if recentEvents != 1 {
			t.Fatalf("recent route event count = %d, want 1", recentEvents)
		}
		if _, err := store.GetAttempt(ctx, keepSession); err != nil {
			t.Fatalf("unexpired attempt was swept: %v", err)
		}
	})

	t.Run("AcquireSessionLock", func(t *testing.T) {
		sessionID := uuid.NewString()

		release1, err := store.AcquireSessionLock(ctx, sessionID)
		if err != nil {
			t.Fatalf("first AcquireSessionLock: %v", err)
		}

		// A different session must not be serialized behind the first lock.
		otherCtx, cancelOther := context.WithTimeout(ctx, 5*time.Second)
		defer cancelOther()
		releaseOther, err := store.AcquireSessionLock(otherCtx, uuid.NewString())
		if err != nil {
			t.Fatalf("different-session AcquireSessionLock blocked: %v", err)
		}
		releaseOther()

		// A second acquire on the same session must block until release.
		type lockResult struct {
			release func()
			err     error
		}
		acquired := make(chan lockResult, 1)
		go func() {
			release2, err := store.AcquireSessionLock(ctx, sessionID)
			acquired <- lockResult{release: release2, err: err}
		}()

		select {
		case result := <-acquired:
			if result.err == nil {
				result.release()
			}
			t.Fatalf("second lock acquired while first was held (err=%v)", result.err)
		case <-time.After(300 * time.Millisecond):
			// Still blocked, as required.
		}

		release1()
		select {
		case result := <-acquired:
			if result.err != nil {
				t.Fatalf("second AcquireSessionLock after release: %v", result.err)
			}
			result.release()
		case <-time.After(5 * time.Second):
			t.Fatal("second lock never acquired after first release")
		}

		// Releasing twice is safe (sync.Once) and the lock is free again.
		release1()
		release3, err := store.AcquireSessionLock(ctx, sessionID)
		if err != nil {
			t.Fatalf("reacquire after release: %v", err)
		}
		release3()
	})
}
