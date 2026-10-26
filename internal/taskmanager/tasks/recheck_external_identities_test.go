package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// fakeIdleRechecker answers the scheduled re-check from memory; the
// selection and outcomes are pinned by the auth package's database tests.
type fakeIdleRechecker struct {
	due    bool
	counts map[string]int
	err    error
	calls  int
}

func (f *fakeIdleRechecker) IdleRecheckDue(context.Context) (bool, error) { return f.due, f.err }

func (f *fakeIdleRechecker) RecheckIdleIdentities(context.Context) (map[string]int, error) {
	f.calls++
	return f.counts, f.err
}

func TestRecheckExternalIdentitiesTask(t *testing.T) {
	rechecker := &fakeIdleRechecker{due: true, counts: map[string]int{"active": 2, "not_found": 1}}
	task := NewRecheckExternalIdentitiesTask(nil, rechecker)
	if task.Key() != "recheck_external_identities" || task.Category() != taskmanager.TaskCategorySystem {
		t.Fatalf("task = %q %q", task.Key(), task.Category())
	}
	triggers := task.DefaultTriggers()
	if len(triggers) != 1 || triggers[0].Type != taskmanager.TriggerTypeInterval || triggers[0].IntervalMs != int64(time.Hour/time.Millisecond) {
		t.Fatalf("triggers = %+v", triggers)
	}
	var _ taskmanager.ScheduledConditionalTask = task
	if run, err := task.ShouldRun(context.Background()); err != nil || !run {
		t.Fatalf("ShouldRun = %v, %v", run, err)
	}
	rechecker.due = false
	if run, _ := task.ShouldRun(context.Background()); run {
		t.Fatal("ShouldRun with nothing due")
	}

	lock := &fakeClusterLock{acquired: true}
	task.lock = lock
	progress := &authSessionCleanupProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rechecker.calls != 1 || lock.released != 1 {
		t.Fatalf("calls = %d, released = %d", rechecker.calls, lock.released)
	}
	var result recheckExternalIdentitiesResult
	if err := json.Unmarshal(progress.result, &result); err != nil || result.Checked != 3 || result.ByStatus["not_found"] != 1 {
		t.Fatalf("result = %s (%v)", progress.result, err)
	}
	if got := progress.reports[len(progress.reports)-1]; got != "Re-checked 3 external sign-in accounts" {
		t.Fatalf("last report = %q", got)
	}

	// Another server holds the lock: this one skips.
	lock.acquired = false
	if err := task.Execute(context.Background(), &authSessionCleanupProgress{}); err != nil || rechecker.calls != 1 {
		t.Fatalf("skipped run: %v, calls = %d", err, rechecker.calls)
	}

	lock.acquired = true
	rechecker.err = errors.New("database down")
	if err := task.Execute(context.Background(), &authSessionCleanupProgress{}); err == nil {
		t.Fatal("failure swallowed")
	}

	unconfigured := NewRecheckExternalIdentitiesTask(nil, nil)
	if err := unconfigured.Execute(context.Background(), &authSessionCleanupProgress{}); err != nil {
		t.Fatalf("unconfigured: %v", err)
	}
	if run, _ := unconfigured.ShouldRun(context.Background()); run {
		t.Fatal("unconfigured ShouldRun")
	}
}
