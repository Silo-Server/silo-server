package playback

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func progressTestAttempt(sessionID string) AttemptRecordV3 {
	return AttemptRecordV3{PlaybackAttemptID: uuid.NewString(), SessionID: sessionID, UserID: 1, ProfileID: "p", RequestDigest: "d", ExpiresAt: time.Now().Add(time.Hour)}
}

// TestMemoryPlanStoreProgressSequencing mirrors the Postgres compare-and-set
// semantics so handler tests on the memory store observe the same outcomes.
func TestMemoryPlanStoreProgressSequencing(t *testing.T) {
	store := NewMemoryPlanStoreV3()
	ctx := t.Context()
	sessionID := uuid.NewString()
	if err := store.SaveAttempt(ctx, progressTestAttempt(sessionID)); err != nil {
		t.Fatal(err)
	}
	first := ProgressSampleV3{Sequence: 1, Position: 10}
	if r, err := store.ApplyProgress(ctx, sessionID, first); err != nil || r.Outcome != ProgressAppliedV3 || *r.Accepted != first {
		t.Fatalf("apply: %+v %v", r, err)
	}
	if r, err := store.ApplyProgress(ctx, sessionID, first); err != nil || r.Outcome != ProgressReplayedV3 {
		t.Fatalf("replay: %+v %v", r, err)
	}
	if _, err := store.ApplyProgress(ctx, sessionID, ProgressSampleV3{Sequence: 1, Position: 11}); !errors.Is(err, ErrProgressConflictV3) {
		t.Fatalf("conflict: %v", err)
	}
	second := ProgressSampleV3{Sequence: 5, Position: 4, IsPaused: true}
	if r, err := store.ApplyProgress(ctx, sessionID, second); err != nil || r.Outcome != ProgressAppliedV3 {
		t.Fatalf("backward position newer sequence: %+v %v", r, err)
	}
	if r, err := store.ApplyProgress(ctx, sessionID, ProgressSampleV3{Sequence: 3}); err != nil || r.Outcome != ProgressStaleSampleV3 || *r.Accepted != second {
		t.Fatalf("stale: %+v %v", r, err)
	}
	if _, err := store.ApplyProgress(ctx, uuid.NewString(), first); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	record, err := store.GetAttempt(ctx, sessionID)
	if err != nil || record.LastSequence != 5 || *record.LastSample != second {
		t.Fatalf("record: %+v %v", record, err)
	}
}

func TestMemoryPlanStoreStopOnce(t *testing.T) {
	store := NewMemoryPlanStoreV3()
	ctx := t.Context()
	sessionID := uuid.NewString()
	if err := store.SaveAttempt(ctx, progressTestAttempt(sessionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyProgress(ctx, sessionID, ProgressSampleV3{Sequence: 2, Position: 20}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.StopAttempt(ctx, sessionID, "nope", nil); !errors.Is(err, ErrInvalidStopIDV3) {
		t.Fatalf("invalid id: %v", err)
	}
	stopID := uuid.NewString()
	final := ProgressSampleV3{Sequence: 3, Position: 30}
	receipt, first, err := store.StopAttempt(ctx, sessionID, stopID, &final)
	if err != nil || !first || receipt.StopID != stopID || *receipt.Accepted != final {
		t.Fatalf("first: %+v %v %v", receipt, first, err)
	}
	receipt.HistoryID = "h1"
	if err := store.RecordStopReceipt(ctx, sessionID, receipt); err != nil {
		t.Fatal(err)
	}
	replay, first, err := store.StopAttempt(ctx, sessionID, uuid.NewString(), &ProgressSampleV3{Sequence: 9})
	if err != nil || first || replay != receipt {
		t.Fatalf("replay: %+v %v %v", replay, first, err)
	}
	if _, err := store.ApplyProgress(ctx, sessionID, ProgressSampleV3{Sequence: 10}); !errors.Is(err, ErrAttemptStoppedV3) {
		t.Fatalf("after stop: %v", err)
	}
	if err := store.RecordStopReceipt(ctx, sessionID, StopReceiptV3{StopID: uuid.NewString()}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("foreign receipt: %v", err)
	}
	// An older final sample does not move a stop backward.
	other := uuid.NewString()
	if err := store.SaveAttempt(ctx, progressTestAttempt(other)); err != nil {
		t.Fatal(err)
	}
	last := ProgressSampleV3{Sequence: 7, Position: 70}
	if _, err := store.ApplyProgress(ctx, other, last); err != nil {
		t.Fatal(err)
	}
	if r, first, err := store.StopAttempt(ctx, other, uuid.NewString(), &ProgressSampleV3{Sequence: 6}); err != nil || !first || *r.Accepted != last {
		t.Fatalf("older final: %+v %v %v", r, first, err)
	}
}
