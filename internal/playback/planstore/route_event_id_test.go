package planstore

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestRouteEventIDDedup proves a v2 event id records once per attempt while
// legacy id-less reports keep their unconditional insert. The inserted flag
// must agree with the rows, because metrics count only inserted events.
func TestRouteEventIDDedup(t *testing.T) {
	f := newPlanstoreFixture(t)
	store := NewPostgres(f.pool)
	ctx := t.Context()
	attempt := uuid.NewString()
	if err := store.SaveAttempt(ctx, f.attemptRecord(uuid.NewString(), attempt, "digest")); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	record := playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: 3, PlaybackAttemptID: attempt, Event: playback.RouteEventFirstFrameV3, Diagnostics: map[string]string{}}, EventID: id, UserID: f.userID, ProfileID: "profile-1"}
	for i := range 3 {
		inserted, err := store.RecordRouteEvent(ctx, record)
		if err != nil {
			t.Fatal(err)
		}
		if inserted != (i == 0) {
			t.Fatalf("report %d with the same event id: inserted = %v", i+1, inserted)
		}
	}
	legacy := record
	legacy.EventID = ""
	for i := range 2 {
		inserted, err := store.RecordRouteEvent(ctx, legacy)
		if err != nil {
			t.Fatal(err)
		}
		if !inserted {
			t.Fatalf("legacy report %d: inserted = false", i+1)
		}
	}
	var withID, withoutID int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE event_id IS NOT NULL), count(*) FILTER (WHERE event_id IS NULL) FROM playback_route_events WHERE playback_attempt_id=$1`, attempt).Scan(&withID, &withoutID); err != nil {
		t.Fatal(err)
	}
	if withID != 1 || withoutID != 2 {
		t.Fatalf("dedup: with id=%d without id=%d", withID, withoutID)
	}
}

func TestSummaryResolvesReplacedPlanDelivery(t *testing.T) {
	f := newPlanstoreFixture(t)
	store := NewPostgres(f.pool)
	ctx := t.Context()
	session, attempt := uuid.NewString(), uuid.NewString()
	record := f.attemptRecord(session, attempt, "digest")
	record.CurrentPlan.Delivery = playback.DeliveryOriginalHTTPV3
	original := record.CurrentPlan
	record.StartResponse.PlaybackPlan = &original
	if err := store.SaveAttempt(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE playback_v3_attempts SET current_plan_id='plan-3',current_plan=jsonb_set(jsonb_set(current_plan,'{plan_id}','"plan-3"'),'{delivery}','"transcode_hls"') WHERE session_id=$1::uuid`, session); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO playback_v3_replans(session_id,replan_request_id,request_digest,state,lease_expires_at,response) VALUES($1::uuid,'replan-1','digest','completed',now(),'{"playback_plan":{"plan_id":"plan-2","delivery":"remux_hls"}}')`, session); err != nil {
		t.Fatal(err)
	}
	for plan, want := range map[string]string{"plan-1": "original_http", "plan-2": "remux_hls", "plan-3": "transcode_hls", "unknown": ""} {
		got, err := store.PlanDelivery(ctx, attempt, plan)
		if err != nil || got != want {
			t.Fatalf("plan %s delivery %q want %q: %v", plan, got, want, err)
		}
	}
}
