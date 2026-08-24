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
	ladderCalls int
	ladderStats metadata.ImageCacheRunStats
	complete    bool
	ladderErr   error
}

func (f *ladderRunner) RunLadderBackfill(_ context.Context, workerID string, _ int, _ int, _ time.Duration, _ metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, bool, error) {
	f.ladderCalls++
	f.workerIDs = append(f.workerIDs, workerID)
	return f.ladderStats, f.complete, f.ladderErr
}

type fakeLadderState struct {
	version  int
	recorded []int
	getErr   error
	setErr   error
}

func (s *fakeLadderState) Get(context.Context) (int, error) { return s.version, s.getErr }

func (s *fakeLadderState) SetBackfilled(_ context.Context, version int) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.recorded = append(s.recorded, version)
	s.version = version
	return nil
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
