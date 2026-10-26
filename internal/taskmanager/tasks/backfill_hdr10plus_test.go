package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeHDR10PlusBackfiller struct {
	runs   int
	result scanner.HDR10PlusBackfillResult
}

func (f *fakeHDR10PlusBackfiller) Run(_ context.Context, progress func(scanner.HDR10PlusBackfillResult, float64)) (scanner.HDR10PlusBackfillResult, error) {
	f.runs++
	progress(f.result, 40)
	return f.result, nil
}

func TestBackfillHDR10PlusTaskRunsAtStartupAndDailyUnderClusterLock(t *testing.T) {
	backfiller := &fakeHDR10PlusBackfiller{result: scanner.HDR10PlusBackfillResult{Checked: 3, HDR10Plus: 2}}
	task := NewBackfillHDR10PlusTask(nil, backfiller)
	triggers := task.DefaultTriggers()
	if len(triggers) != 2 || triggers[0].Type != taskmanager.TriggerTypeStartup ||
		triggers[1].Type != taskmanager.TriggerTypeInterval || triggers[1].IntervalMs != (24*time.Hour).Milliseconds() {
		t.Fatalf("DefaultTriggers() = %+v, want startup and a daily interval", triggers)
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
	if last := progress.messages[len(progress.messages)-1]; last != "Checked 3 HDR files, found 2 HDR10+" {
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
