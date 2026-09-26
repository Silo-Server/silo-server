package planstore

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/google/uuid"
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
