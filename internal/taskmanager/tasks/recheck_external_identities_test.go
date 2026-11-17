package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// resultProgress keeps each task's last result data.
type resultProgress struct {
	reports []string
	result  json.RawMessage
}

func (p *resultProgress) Report(_ float64, message string) {
	p.reports = append(p.reports, message)
}

func (p *resultProgress) SetResultData(data json.RawMessage) {
	p.result = append(p.result[:0], data...)
}

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
	progress := &resultProgress{}
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
	if err := task.Execute(context.Background(), &resultProgress{}); err != nil || rechecker.calls != 1 {
		t.Fatalf("skipped run: %v, calls = %d", err, rechecker.calls)
	}

	lock.acquired = true
	rechecker.err = errors.New("database down")
	if err := task.Execute(context.Background(), &resultProgress{}); err == nil {
		t.Fatal("failure swallowed")
	}

	unconfigured := NewRecheckExternalIdentitiesTask(nil, nil)
	if err := unconfigured.Execute(context.Background(), &resultProgress{}); err != nil {
		t.Fatalf("unconfigured: %v", err)
	}
	if run, _ := unconfigured.ShouldRun(context.Background()); run {
		t.Fatal("unconfigured ShouldRun")
	}
}
