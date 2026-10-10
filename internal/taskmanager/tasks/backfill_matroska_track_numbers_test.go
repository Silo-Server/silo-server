package tasks

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeMatroskaTrackBackfiller struct {
	runs   int
	result scanner.MatroskaTrackBackfillResult
}

func (f *fakeMatroskaTrackBackfiller) Run(_ context.Context, progress func(scanner.MatroskaTrackBackfillResult, float64)) (scanner.MatroskaTrackBackfillResult, error) {
	f.runs++
	progress(f.result, 40)
	return f.result, nil
}

func TestBackfillMatroskaTrackNumbersTaskRunsAtStartupUnderClusterLock(t *testing.T) {
	backfiller := &fakeMatroskaTrackBackfiller{result: scanner.MatroskaTrackBackfillResult{Checked: 3, Updated: 2}}
	task := NewBackfillMatroskaTrackNumbersTask(nil, backfiller)
	if triggers := task.DefaultTriggers(); len(triggers) != 1 || triggers[0].Type != taskmanager.TriggerTypeStartup {
		t.Fatalf("DefaultTriggers() = %+v, want one startup trigger", triggers)
	}

	lock := &fakeClusterLock{acquired: true}
	task.lock = lock
	progress := &recordingProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatal(err)
	}
	if backfiller.runs != 1 || lock.released != 1 {
		t.Fatalf("runs = %d, released = %d; want one run under the lock", backfiller.runs, lock.released)
	}
	if last := progress.messages[len(progress.messages)-1]; last != "Checked 3 MKV files, updated 2" {
		t.Fatalf("final message = %q", last)
	}
	if progress.percents[1] != 40 {
		t.Fatalf("batch progress = %v, want the backfiller's 40%%", progress.percents)
	}

	// Another server holds the lock: skip without reading any media.
	task.lock = &fakeClusterLock{acquired: false}
	if err := task.Execute(context.Background(), &recordingProgress{}); err != nil {
		t.Fatal(err)
	}
	if backfiller.runs != 1 {
		t.Fatalf("runs = %d after a held lock, want 1", backfiller.runs)
	}
}
