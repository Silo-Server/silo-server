package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// ladderRunner adds the optional one-shot ladder pass to the drain fake.
type ladderRunner struct {
	fakeMetadataImageCacheRunner
	ladderCalls   int
	ladderStats   metadata.ImageCacheRunStats
	ladderUpdates []metadata.ImageCacheRunStats
	complete      bool
	ladderErr     error
}

func (f *ladderRunner) RunLadderBackfill(_ context.Context, workerID string, _ int, _ int, _ time.Duration, report metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, bool, error) {
	f.ladderCalls++
	f.workerIDs = append(f.workerIDs, workerID)
	for _, update := range f.ladderUpdates {
		report(update)
	}
	return f.ladderStats, f.complete, f.ladderErr
}

type fakeLadderState struct {
	version       int
	lastAttempt   time.Time
	attempts      int
	recorded      []int
	getErr        error
	setErr        error
	rejectConfirm bool
}

func (s *fakeLadderState) Get(context.Context) (metadata.ImageLadderBackfillState, error) {
	return metadata.ImageLadderBackfillState{
		BackfilledVersion: s.version,
		LastAttemptAt:     s.lastAttempt,
	}, s.getErr
}

func (s *fakeLadderState) MarkAttempt(context.Context) error {
	s.attempts++
	s.lastAttempt = time.Now()
	return nil
}

func (s *fakeLadderState) ConfirmBackfilled(_ context.Context, version int) (bool, error) {
	if s.setErr != nil {
		return false, s.setErr
	}
	if s.rejectConfirm {
		s.lastAttempt = time.Time{}
		return false, nil
	}
	s.recorded = append(s.recorded, version)
	s.version = version
	return true, nil
}

func runLadderTask(t *testing.T, runner *ladderRunner, state *fakeLadderState, target int) *recordingProgress {
	t.Helper()
	task := NewCacheMetadataImagesTask(runner)
	task.SetLadderBackfill(state, target)
	progress := &recordingProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	return progress
}

func TestLadderBackfillRunsOnceAndRecordsTheVersion(t *testing.T) {
	runner := &ladderRunner{complete: true}
	runner.ladderStats = metadata.ImageCacheRunStats{Succeeded: 12}
	state := &fakeLadderState{version: 1}

	progress := runLadderTask(t, runner, state, 2)

	if runner.ladderCalls != 1 {
		t.Fatalf("ladder runs = %d, want 1", runner.ladderCalls)
	}
	if len(state.recorded) != 1 || state.recorded[0] != 2 {
		t.Fatalf("recorded versions = %v, want [2]", state.recorded)
	}
	if !strings.Contains(strings.Join(progress.messages, "\n"), "12 images regenerated") {
		t.Fatalf("progress messages = %v, want the regenerated count reported", progress.messages)
	}

	// A second execution now finds the version recorded and does nothing.
	runLadderTask(t, runner, state, 2)
	if runner.ladderCalls != 1 {
		t.Fatalf("ladder runs = %d, want the pass to be one-shot", runner.ladderCalls)
	}
}

// The ordinary queue drain has to happen first — a user waiting on freshly
// scanned artwork must not queue behind a library-wide regeneration.
func TestLadderBackfillRunsAfterTheDrain(t *testing.T) {
	runner := &ladderRunner{complete: true}
	state := &fakeLadderState{}

	runLadderTask(t, runner, state, 2)

	if runner.drainCalls != 1 {
		t.Fatalf("drain runs = %d, want 1", runner.drainCalls)
	}
	if runner.ladderCalls != 1 {
		t.Fatalf("ladder runs = %d, want 1", runner.ladderCalls)
	}
}

func TestLadderBackfillNotRecordedWhenIncomplete(t *testing.T) {
	runner := &ladderRunner{complete: false}
	runner.ladderStats = metadata.ImageCacheRunStats{Succeeded: 4}
	state := &fakeLadderState{}

	runLadderTask(t, runner, state, 2)

	if len(state.recorded) != 0 {
		t.Fatalf("recorded versions = %v, want none for an unfinished pass", state.recorded)
	}
}

func TestLadderBackfillFinalConfirmationCanReopenThePass(t *testing.T) {
	runner := &ladderRunner{complete: true}
	state := &fakeLadderState{rejectConfirm: true}

	progress := runLadderTask(t, runner, state, 2)

	if len(state.recorded) != 0 {
		t.Fatalf("recorded versions = %v, want none after rejected confirmation", state.recorded)
	}
	if !state.lastAttempt.IsZero() {
		t.Fatalf("last attempt = %v, want final confirmation to make the next run eligible", state.lastAttempt)
	}
	if !strings.Contains(strings.Join(progress.messages, "\n"), "resume on the next run") {
		t.Fatalf("progress messages = %v, want reopened pass reported", progress.messages)
	}
}

func TestLadderBackfillSurvivesRunnerFailure(t *testing.T) {
	runner := &ladderRunner{ladderErr: errors.New("storage unavailable")}
	state := &fakeLadderState{}

	progress := runLadderTask(t, runner, state, 2)

	if len(state.recorded) != 0 {
		t.Fatalf("recorded versions = %v, want none after a failure", state.recorded)
	}
	if !strings.Contains(strings.Join(progress.messages, "\n"), "interrupted") {
		t.Fatalf("progress messages = %v, want the failure reported", progress.messages)
	}
}

func TestLadderBackfillSkippedWhenStateUnavailable(t *testing.T) {
	runner := &ladderRunner{complete: true}
	state := &fakeLadderState{getErr: errors.New("database unavailable")}

	runLadderTask(t, runner, state, 2)

	if runner.ladderCalls != 0 {
		t.Fatalf("ladder runs = %d, want none when the recorded version is unknown", runner.ladderCalls)
	}
}

// One execution runs two phases, and a progress bar that reaches 100 and then
// restarts at 0 reads as a failed-and-retrying task. The reported percent must
// only ever climb.
func TestLadderBackfillProgressIsMonotone(t *testing.T) {
	runner := &ladderRunner{complete: true}
	runner.updates = []metadata.ImageCacheRunStats{
		{Backlog: metadata.ImageCacheBacklog{Known: true, Queued: 10}, Succeeded: 5},
		{Backlog: metadata.ImageCacheBacklog{Known: true, Queued: 10}, Succeeded: 10},
	}
	state := &fakeLadderState{}
	runner.ladderUpdates = []metadata.ImageCacheRunStats{
		{Backlog: metadata.ImageCacheBacklog{Known: true, Queued: 2}, Succeeded: 2},
		{Backlog: metadata.ImageCacheBacklog{Known: true, Queued: 10}, Succeeded: 3},
		{Backlog: metadata.ImageCacheBacklog{Known: true, Queued: 10}, Succeeded: 10},
	}

	progress := runLadderTask(t, runner, state, 2)

	if len(progress.percents) < 2 {
		t.Fatalf("percents = %v, want several reports", progress.percents)
	}
	for i, percent := range progress.percents {
		if i > 0 && percent < progress.percents[i-1] {
			t.Fatalf("progress went backwards at %d: %v", i, progress.percents)
		}
	}
	if last := progress.percents[len(progress.percents)-1]; last != 100 {
		t.Fatalf("final percent = %v, want 100", last)
	}
	// The drain must not consume the whole bar when a ladder pass follows it.
	for i, message := range progress.messages {
		if strings.Contains(message, "Regenerating cached artwork") && progress.percents[i] >= 100 {
			t.Fatalf("ladder phase started at %v, want it below 100", progress.percents[i])
		}
	}
}

// With no ladder pass pending the drain still owns the whole bar and ends at 100.
func TestDrainOwnsTheWholeBarWithoutALadderPass(t *testing.T) {
	runner := &ladderRunner{complete: true}
	state := &fakeLadderState{version: 2}

	progress := runLadderTask(t, runner, state, 2)

	if runner.ladderCalls != 0 {
		t.Fatalf("ladder runs = %d, want none", runner.ladderCalls)
	}
	if last := progress.percents[len(progress.percents)-1]; last != 100 {
		t.Fatalf("final percent = %v, want 100", last)
	}
}

// Completion is measured against the artwork, so a deployment holding an image
// that can never be regenerated never reaches "done". The sweep must therefore
// be paced: a scheduler tick a minute after the last attempt does not re-scan.
func TestLadderBackfillPacesRepeatedScans(t *testing.T) {
	runner := &ladderRunner{complete: false}
	state := &fakeLadderState{}

	runLadderTask(t, runner, state, 2)
	if runner.ladderCalls != 1 || state.attempts != 1 {
		t.Fatalf("first run: ladder=%d attempts=%d, want 1/1", runner.ladderCalls, state.attempts)
	}

	// Immediately again: inside the interval, so it must not scan.
	runLadderTask(t, runner, state, 2)
	if runner.ladderCalls != 1 {
		t.Fatalf("ladder runs = %d, want the second tick to be paced out", runner.ladderCalls)
	}

	// Once the interval has elapsed it resumes.
	state.lastAttempt = time.Now().Add(-ladderBackfillScanInterval - time.Minute)
	runLadderTask(t, runner, state, 2)
	if runner.ladderCalls != 2 {
		t.Fatalf("ladder runs = %d, want the sweep to resume after the interval", runner.ladderCalls)
	}
}

// The attempt is recorded before the pass, so a crash mid-sweep still paces the
// next one rather than letting every restart re-scan the catalog.
func TestLadderBackfillRecordsAttemptBeforeRunning(t *testing.T) {
	runner := &ladderRunner{ladderErr: errors.New("boom")}
	state := &fakeLadderState{}

	runLadderTask(t, runner, state, 2)

	if state.attempts != 1 {
		t.Fatalf("attempts = %d, want the attempt recorded even though the pass failed", state.attempts)
	}
	if len(state.recorded) != 0 {
		t.Fatalf("recorded versions = %v, want none", state.recorded)
	}
}

// Without SetLadderBackfill the task behaves exactly as before.
func TestCacheTaskWithoutLadderBackfillOnlyDrains(t *testing.T) {
	runner := &ladderRunner{complete: true}
	task := NewCacheMetadataImagesTask(runner)

	if err := task.Execute(context.Background(), &recordingProgress{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if runner.drainCalls != 1 || runner.ladderCalls != 0 {
		t.Fatalf("drain=%d ladder=%d, want 1/0", runner.drainCalls, runner.ladderCalls)
	}
}
