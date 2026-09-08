package planstore

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// sameStopReceipt compares receipts by value; Accepted is a pointer and the
// Postgres store decodes a fresh one on every read.
func sameStopReceipt(a, b playback.StopReceiptV3) bool {
	if a.StopID != b.StopID || a.HistoryID != b.HistoryID {
		return false
	}
	if a.Accepted == nil || b.Accepted == nil {
		return a.Accepted == b.Accepted
	}
	return *a.Accepted == *b.Accepted
}

// TestProgressSequencing proves the compare-and-set on last_sequence: a higher
// sequence always wins, an exact repeat replays, a lower one is stale, and the
// same sequence with a different payload conflicts.
func TestProgressSequencing(t *testing.T) {
	f := newPlanstoreFixture(t)
	store := NewPostgres(f.pool)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if err := store.SaveAttempt(ctx, f.attemptRecord(sessionID, uuid.NewString(), "digest")); err != nil {
		t.Fatal(err)
	}

	first := playback.ProgressSampleV3{Sequence: 1, Position: 10, IsPaused: false}
	receipt, err := store.ApplyProgress(ctx, sessionID, first)
	if err != nil || receipt.Outcome != playback.ProgressAppliedV3 || receipt.Accepted == nil || *receipt.Accepted != first {
		t.Fatalf("first apply: %+v %v", receipt, err)
	}
	receipt, err = store.ApplyProgress(ctx, sessionID, first)
	if err != nil || receipt.Outcome != playback.ProgressReplayedV3 || receipt.Accepted == nil || *receipt.Accepted != first {
		t.Fatalf("replay: %+v %v", receipt, err)
	}
	changed := first
	changed.Position = 11
	if _, err := store.ApplyProgress(ctx, sessionID, changed); !errors.Is(err, playback.ErrProgressConflictV3) {
		t.Fatalf("same sequence different payload: %v", err)
	}
	// A higher sequence wins even when the position moves backward.
	second := playback.ProgressSampleV3{Sequence: 5, Position: 4, IsPaused: true}
	receipt, err = store.ApplyProgress(ctx, sessionID, second)
	if err != nil || receipt.Outcome != playback.ProgressAppliedV3 {
		t.Fatalf("second apply: %+v %v", receipt, err)
	}
	stale := playback.ProgressSampleV3{Sequence: 3, Position: 99}
	receipt, err = store.ApplyProgress(ctx, sessionID, stale)
	if err != nil || receipt.Outcome != playback.ProgressStaleSampleV3 || receipt.Accepted == nil || *receipt.Accepted != second {
		t.Fatalf("stale: %+v %v", receipt, err)
	}
	// Sequence 0 never applies: rows start at 0 and only strictly newer wins.
	receipt, err = store.ApplyProgress(ctx, uuid.NewString(), first)
	if !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("unknown session: %+v %v", receipt, err)
	}

	record, err := store.GetAttempt(ctx, sessionID)
	if err != nil || record.LastSequence != 5 || record.LastSample == nil || *record.LastSample != second || record.StoppedAt != nil {
		t.Fatalf("attempt row: %+v %v", record, err)
	}
}

// TestStopAttemptOnce proves the compare-and-set on stopped_at: the first stop
// applies a newer final sample and reports first=true; every later stop, with
// any stop id, replays the stored receipt, including the history id recorded
// after the writer ran.
func TestStopAttemptOnce(t *testing.T) {
	f := newPlanstoreFixture(t)
	store := NewPostgres(f.pool)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if err := store.SaveAttempt(ctx, f.attemptRecord(sessionID, uuid.NewString(), "digest")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyProgress(ctx, sessionID, playback.ProgressSampleV3{Sequence: 2, Position: 20}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.StopAttempt(ctx, sessionID, "not-a-uuid", nil); !errors.Is(err, playback.ErrInvalidStopIDV3) {
		t.Fatalf("invalid stop id: %v", err)
	}

	stopID := uuid.NewString()
	final := playback.ProgressSampleV3{Sequence: 3, Position: 30, IsPaused: true}
	receipt, first, err := store.StopAttempt(ctx, sessionID, stopID, &final)
	if err != nil || !first || receipt.StopID != stopID || receipt.Accepted == nil || *receipt.Accepted != final || receipt.HistoryID != "" {
		t.Fatalf("first stop: %+v first=%v %v", receipt, first, err)
	}
	receipt.HistoryID = uuid.NewString()
	if err := store.RecordStopReceipt(ctx, sessionID, receipt); err != nil {
		t.Fatalf("record receipt: %v", err)
	}
	replay, first, err := store.StopAttempt(ctx, sessionID, uuid.NewString(), &playback.ProgressSampleV3{Sequence: 9, Position: 90})
	if err != nil || first || !sameStopReceipt(replay, receipt) {
		t.Fatalf("replayed stop: %+v first=%v %v (want %+v)", replay, first, err, receipt)
	}
	if _, err := store.ApplyProgress(ctx, sessionID, playback.ProgressSampleV3{Sequence: 10, Position: 100}); !errors.Is(err, playback.ErrAttemptStoppedV3) {
		t.Fatalf("progress after stop: %v", err)
	}
	record, err := store.GetAttempt(ctx, sessionID)
	if err != nil || record.StoppedAt == nil || record.LastSequence != 3 || record.LastSample == nil || *record.LastSample != final {
		t.Fatalf("stopped row: %+v %v", record, err)
	}
	// A receipt for a different stop id is not the stored stop and is refused.
	if err := store.RecordStopReceipt(ctx, sessionID, playback.StopReceiptV3{StopID: uuid.NewString()}); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("foreign receipt: %v", err)
	}
	if _, _, err := store.StopAttempt(ctx, uuid.NewString(), uuid.NewString(), nil); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
}

// TestStopAttemptKeepsNewerProgress proves an older final sample does not
// move the row backward: the accepted sample is the last applied progress.
func TestStopAttemptKeepsNewerProgress(t *testing.T) {
	f := newPlanstoreFixture(t)
	store := NewPostgres(f.pool)
	ctx := t.Context()

	sessionID := uuid.NewString()
	if err := store.SaveAttempt(ctx, f.attemptRecord(sessionID, uuid.NewString(), "digest")); err != nil {
		t.Fatal(err)
	}
	last := playback.ProgressSampleV3{Sequence: 7, Position: 70}
	if _, err := store.ApplyProgress(ctx, sessionID, last); err != nil {
		t.Fatal(err)
	}
	receipt, first, err := store.StopAttempt(ctx, sessionID, uuid.NewString(), &playback.ProgressSampleV3{Sequence: 6, Position: 60})
	if err != nil || !first || receipt.Accepted == nil || *receipt.Accepted != last {
		t.Fatalf("older final: %+v first=%v %v", receipt, first, err)
	}

	// No progress at all: the receipt carries no accepted sample.
	quiet := uuid.NewString()
	if err := store.SaveAttempt(ctx, f.attemptRecord(quiet, uuid.NewString(), "digest")); err != nil {
		t.Fatal(err)
	}
	receipt, first, err = store.StopAttempt(ctx, quiet, uuid.NewString(), nil)
	if err != nil || !first || receipt.Accepted != nil {
		t.Fatalf("quiet stop: %+v first=%v %v", receipt, first, err)
	}
	replay, first, err := store.StopAttempt(ctx, quiet, uuid.NewString(), nil)
	if err != nil || first || !sameStopReceipt(replay, receipt) {
		t.Fatalf("quiet replay: %+v first=%v %v", replay, first, err)
	}
}
