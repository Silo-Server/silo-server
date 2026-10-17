package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

// The HTTP handler keeper independently exercises failover through this exact
// store seam. This control composes its committed tuple with real PG replica
// readers and real Redis report publication; it does not start an HTTP worker.
//
//nolint:funlen,gocyclo // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func TestWorkerReportAfterCommittedExecutorMove(t *testing.T) {
	_, plans, _ := overlapPlanStores(t)
	f := newReportFixture(t)
	f.card.SessionID = uuid.NewString()
	if err := f.sink.recipes.Put(t.Context(), f.assignment.WorkID, f.card); err != nil {
		t.Fatal(err)
	}
	f.sink.plans = plans[1]
	replica := f.replica(t)
	replica.plans = plans[2]
	current := f.session()
	current.Info.SessionID, current.Info.CodecVideo, current.Info.StartedAt = current.WorkID, "h264", "1900-01-01T00:00:00Z"
	candidateCard := f.card
	candidateCard.MediaFileID++
	candidate := overlapTransport(t, f, "candidate:"+uuid.NewString(), candidateCard, "hevc")
	otherCard := f.card
	otherCard.SessionID, otherCard.UserID, otherCard.ProfileID, otherCard.MediaFileID = uuid.NewString(), 52, "other-profile", 84
	other := overlapTransport(t, f, "other:"+uuid.NewString(), otherCard, "av1")
	record := playback.AttemptRecordV3{PlaybackAttemptID: uuid.NewString(), SessionID: f.card.SessionID, UserID: f.card.UserID, ProfileID: f.card.ProfileID, RequestedMediaFileID: f.card.MediaFileID, EffectiveMediaFileID: f.card.MediaFileID, CurrentPlanID: "plan-before-move", CurrentPlan: playback.PlanV3{PlanID: "plan-before-move", SessionID: f.card.SessionID}, FrozenRecipe: playback.ExecutableRecipeV3{Version: 1, PlanID: "plan-before-move", PlayMethod: playback.PlayTranscode, TranscodeNodeURL: "https://departed.example.test", TranscodeTransportID: current.WorkID}, ExpiresAt: time.Now().Add(time.Hour)}
	if err := plans[0].SaveAttempt(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	var sequence uint64
	check := func(stage string, wantCurrent bool) {
		t.Helper()
		for order := range 2 {
			sequence++
			items := []workerwire.SessionReport{current, candidate, other}
			if order == 1 {
				items[0], items[1] = items[1], items[0]
			}
			observations := f.observations(int64(sequence * 100))
			observations.Scopes = []workerwire.ObservationScope{
				{WorkID: current.WorkID, Generation: current.Generation, SessionID: f.card.SessionID},
				{WorkID: candidate.WorkID, Generation: candidate.Generation, SessionID: f.card.SessionID},
				{WorkID: other.WorkID, Generation: other.Generation, SessionID: otherCard.SessionID},
			}
			observations.Snapshot.Sessions = append(observations.Snapshot.Sessions, streamtelemetry.SessionView{SessionID: otherCard.SessionID, BytesAccepted: 25})
			sink := f.sink
			if order == 1 {
				sink = replica
			}
			if err := sink.accept(t.Context(), f.node.ID, reportJSON(t, workerwire.Report{BootID: "rehome-report-boot", Sequence: sequence, Sessions: items, Observations: observations})); err != nil {
				t.Fatalf("%s: %v", stage, err)
			}
			info, found := f.load(t, f.card.SessionID)
			if found != wantCurrent || found && (info.MediaFileID != f.card.MediaFileID || info.CodecVideo != "h264" || info.NodeURL != f.node.URL) {
				t.Fatalf("%s order%d selected an uncommitted route: %+v found%v", stage, order, info, found)
			}
			if unrelated, found := f.load(t, otherCard.SessionID); !found || unrelated.MediaFileID != otherCard.MediaFileID {
				t.Fatalf("%s lost unrelated report: %+v", stage, unrelated)
			}
			snapshot, err := sink.publishers[f.node.ID].Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			wantSessions := 1
			if wantCurrent {
				wantSessions++
			}
			if len(snapshot.Sessions) != wantSessions {
				t.Fatalf("%s telemetry sessions=%d want%d", stage, len(snapshot.Sessions), wantSessions)
			}
			for _, view := range snapshot.Sessions {
				if view.SessionID == f.card.SessionID && (view.MediaFileID != f.card.MediaFileID || view.BytesAccepted != int64(sequence*100)) {
					t.Fatalf("%s candidate contaminated current telemetry: %+v", stage, view)
				}
				if view.SessionID == otherCard.SessionID && view.BytesAccepted != 25 {
					t.Fatalf("%s unrelated telemetry changed: %+v", stage, view)
				}
			}
		}
	}
	check("uncommitted move", false)
	owned, err := plans[0].AcquireRouteSession(t.Context(), record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := owned.GetAttempt(t.Context())
	if err != nil {
		owned.Close()
		t.Fatal(err)
	}
	moved, err := owned.CompareAndSwap(t.Context(), *fresh, playback.TranscodeRoute{NodeURL: f.node.URL, TransportID: current.WorkID})
	owned.Close()
	if err != nil || !moved {
		t.Fatalf("real owned route CAS did not commit: moved%v err%v", moved, err)
	}
	check("committed moved executor with newer pending candidate", true)
	settled, err := plans[0].GetAttempt(t.Context(), record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := plans[0].BeginReplan(t.Context(), record.SessionID, "failed-after-move", "digest", "", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	failed := *settled
	failed.CurrentPlanID, failed.CurrentPlan.PlanID, failed.CurrentReplanRequestID = "failed-plan", "failed-plan", "failed-after-move"
	failed.FrozenRecipe.TranscodeTransportID = candidate.WorkID
	failed.EffectiveMediaFileID = candidateCard.MediaFileID
	if err := plans[0].CompleteReplan(t.Context(), record.SessionID, "failed-after-move", uuid.NewString(), "", json.RawMessage(`{"outcome":"playable"}`), failed); err == nil {
		t.Fatal("replacement without owned replan lease committed")
	}
	check("failed replacement preserves moved executor", true)
	if err := plans[0].ReleaseReplan(t.Context(), record.SessionID, "failed-after-move", lease.LeaseToken); err != nil {
		t.Fatal(err)
	}
}
