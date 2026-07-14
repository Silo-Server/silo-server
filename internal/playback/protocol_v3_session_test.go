package playback

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckReplacementAllowedTransfersExistingCapacitySlot(t *testing.T) {
	manager := NewSessionManager(2, 1)
	direct, err := manager.StartSession(1, "profile-1", 1, PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CheckReplacementAllowed(context.Background(), direct.ID, PlayTranscode, true); err != nil {
		t.Fatalf("direct to first transcode denied: %v", err)
	}
	if _, err := manager.StartSession(1, "profile-1", 2, PlayTranscode, true); !errors.Is(err, ErrTooManyTranscodes) {
		t.Fatalf("reservation did not hold the transcode slot: %v", err)
	}
	manager.CancelReplacementReservation(direct.ID)
	if _, err := manager.StartSession(1, "profile-1", 2, PlayTranscode, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.CheckReplacementAllowed(context.Background(), direct.ID, PlayTranscode, true); !errors.Is(err, ErrTooManyTranscodes) {
		t.Fatalf("replacement error = %v, want ErrTooManyTranscodes", err)
	}
}

func TestCheckReplacementAllowedRechecksDynamicTranscodePolicy(t *testing.T) {
	manager := NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 1, PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetLimitProvider(func(context.Context, int) (SessionLimits, error) {
		return SessionLimits{TranscodingDisabled: true}, nil
	})
	if err := manager.CheckReplacementAllowed(context.Background(), session.ID, PlayTranscode, true); !errors.Is(err, ErrTranscodingDisabled) {
		t.Fatalf("replacement error = %v, want ErrTranscodingDisabled", err)
	}
}

func TestReconstructionOutputDirPreservesPlanScopedIsolation(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-1"
	want := filepath.Join(root, sessionID+"-plan-deadbeef")
	if got := reconstructionOutputDir(root, sessionID, sessionID+"-plan-deadbeef"); got != want {
		t.Fatalf("output dir = %q, want %q", got, want)
	}
	if got := reconstructionOutputDir(root, sessionID, filepath.Join("..", "escape")); got != filepath.Join(root, sessionID) {
		t.Fatalf("unsafe output dir = %q", got)
	}
}

func TestRecipeCardRoundTripPreservesRemoteTransportIdentity(t *testing.T) {
	card := NewRecipeCard(1, "profile-1", 42, "https://node.example", TranscodeOpts{SessionID: "public-session", TranscodeTransportID: "public-session-plan-deadbeef", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", SegmentDuration: 2})
	claims := card.ToClaims()
	if claims.SessionID != "public-session" || claims.TranscodeTransportID != "public-session-plan-deadbeef" {
		t.Fatalf("claims = %#v", claims)
	}
	roundTrip := RecipeCardFromClaims(&claims)
	if roundTrip.SessionID != card.SessionID || roundTrip.TranscodeTransportID != card.TranscodeTransportID {
		t.Fatalf("round trip = %#v, want %#v", roundTrip, card)
	}
}

func TestMemoryPlanStoreV3StartAndReplanIdempotency(t *testing.T) {
	store := NewMemoryPlanStoreV3()
	record := AttemptRecordV3{PlaybackAttemptID: "attempt-0001", SessionID: "session-1", UserID: 1, ProfileID: "profile-1", CurrentPlanID: "plan-1", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.SaveAttempt(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	duplicate := record
	duplicate.SessionID = "session-2"
	if err := store.SaveAttempt(context.Background(), duplicate); !errors.Is(err, ErrPlaybackAttemptExistsV3) {
		t.Fatalf("duplicate start error = %v", err)
	}
	lease, err := store.BeginReplan(context.Background(), record.SessionID, "replan-0001", "digest-a", record.CurrentReplanRequestID, time.Now().Add(time.Minute))
	if err != nil || lease.State != ReplanLeaseOwnedV3 {
		t.Fatalf("first lease = %#v, err=%v", lease, err)
	}
	lease, err = store.BeginReplan(context.Background(), record.SessionID, "replan-0001", "digest-a", record.CurrentReplanRequestID, time.Now().Add(time.Minute))
	if err != nil || lease.State != ReplanLeaseInFlightV3 {
		t.Fatalf("in-flight lease = %#v, err=%v", lease, err)
	}
	if _, err := store.BeginReplan(context.Background(), record.SessionID, "replan-0001", "digest-b", record.CurrentReplanRequestID, time.Now().Add(time.Minute)); !errors.Is(err, ErrIdempotencyKeyReusedV3) {
		t.Fatalf("digest conflict = %v", err)
	}
	response := json.RawMessage(`{"protocol_version":3}`)
	if err := store.CompleteReplan(context.Background(), record.SessionID, "replan-0001", record.CurrentReplanRequestID, response, record); err != nil {
		t.Fatal(err)
	}
	lease, err = store.BeginReplan(context.Background(), record.SessionID, "replan-0001", "digest-a", record.CurrentReplanRequestID, time.Now().Add(time.Minute))
	if err != nil || lease.State != ReplanLeaseCompletedV3 || string(lease.Response) != string(response) {
		t.Fatalf("completed lease = %#v, err=%v", lease, err)
	}
}

func TestMemoryPlanStoreV3RejectsExpiredLeaseAfterNewerCommit(t *testing.T) {
	store := NewMemoryPlanStoreV3()
	record := AttemptRecordV3{
		PlaybackAttemptID:      "attempt-stale-lease",
		SessionID:              "session-stale-lease",
		UserID:                 1,
		ProfileID:              "profile-1",
		CurrentPlanID:          "plan-same",
		CurrentReplanRequestID: "replan-base",
		ExpiresAt:              time.Now().Add(time.Hour),
	}
	if err := store.SaveAttempt(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if lease, err := store.BeginReplan(
		context.Background(), record.SessionID, "replan-abandoned", "digest-a",
		record.CurrentReplanRequestID, time.Now().Add(-time.Second),
	); err != nil || lease.State != ReplanLeaseOwnedV3 {
		t.Fatalf("abandoned lease = %#v, err=%v", lease, err)
	}
	if lease, err := store.BeginReplan(
		context.Background(), record.SessionID, "replan-newer", "digest-b",
		record.CurrentReplanRequestID, time.Now().Add(time.Minute),
	); err != nil || lease.State != ReplanLeaseOwnedV3 {
		t.Fatalf("newer lease = %#v, err=%v", lease, err)
	}
	base := record.CurrentReplanRequestID
	record.CurrentReplanRequestID = "replan-newer"
	if err := store.CompleteReplan(
		context.Background(), record.SessionID, "replan-newer", base,
		json.RawMessage(`{"protocol_version":3}`), record,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginReplan(
		context.Background(), record.SessionID, "replan-abandoned", "digest-a",
		record.CurrentReplanRequestID, time.Now().Add(time.Minute),
	); !errors.Is(err, ErrStaleReplanLeaseV3) {
		t.Fatalf("expired stale lease error = %v", err)
	}
}
