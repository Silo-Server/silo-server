package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeNotificationPurger struct {
	calls         int
	lastDismissed time.Time
	lastAll       time.Time
	returnCount   int64
	err           error
}

func (f *fakeNotificationPurger) PurgeOld(_ context.Context, dismissedBefore, allBefore time.Time) (int64, error) {
	f.calls++
	f.lastDismissed = dismissedBefore
	f.lastAll = allBefore
	return f.returnCount, f.err
}

type fakePushPurger struct {
	calls       int
	lastBefore  time.Time
	returnCount int64
	err         error
}

func (f *fakePushPurger) PurgeTerminal(_ context.Context, before time.Time) (int64, error) {
	f.calls++
	f.lastBefore = before
	return f.returnCount, f.err
}

func TestNotificationsRetentionTask_Metadata(t *testing.T) {
	task := NewNotificationsRetentionTask(&fakeNotificationPurger{}, nil)

	if task.Key() != "notifications_retention" {
		t.Fatalf("Key() = %q, want notifications_retention", task.Key())
	}
	if task.Name() == "" {
		t.Fatal("Name() should not be empty")
	}
	if task.Description() == "" {
		t.Fatal("Description() should not be empty")
	}
	if task.Category() != taskmanager.TaskCategorySystem {
		t.Fatalf("Category() = %q, want %q", task.Category(), taskmanager.TaskCategorySystem)
	}
	if task.IsHidden() {
		t.Fatal("IsHidden() = true, want false")
	}

	triggers := task.DefaultTriggers()
	if len(triggers) != 1 {
		t.Fatalf("DefaultTriggers() length = %d, want 1", len(triggers))
	}
	if triggers[0].Type != taskmanager.TriggerTypeInterval {
		t.Fatalf("trigger type = %q, want interval", triggers[0].Type)
	}
	wantMs := int64(24 * 60 * 60 * 1000)
	if triggers[0].IntervalMs != wantMs {
		t.Fatalf("IntervalMs = %d, want %d", triggers[0].IntervalMs, wantMs)
	}
}

func TestNotificationsRetentionTask_Execute(t *testing.T) {
	purger := &fakeNotificationPurger{returnCount: 42}
	task := NewNotificationsRetentionTask(purger, nil)
	progress := &notificationsProgress{}

	before := time.Now()
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	after := time.Now()

	if purger.calls != 1 {
		t.Fatalf("PurgeOld calls = %d, want 1", purger.calls)
	}
	// dismissedBefore should be ~30 days ago
	wantDismissed := before.Add(-30 * 24 * time.Hour)
	if purger.lastDismissed.Before(wantDismissed.Add(-time.Second)) || purger.lastDismissed.After(after.Add(-30*24*time.Hour+time.Second)) {
		t.Errorf("dismissedBefore = %v, want ~30 days before now", purger.lastDismissed)
	}
	// allBefore should be ~90 days ago
	wantAll := before.Add(-90 * 24 * time.Hour)
	if purger.lastAll.Before(wantAll.Add(-time.Second)) || purger.lastAll.After(after.Add(-90*24*time.Hour+time.Second)) {
		t.Errorf("allBefore = %v, want ~90 days before now", purger.lastAll)
	}
	if len(progress.reports) == 0 {
		t.Fatal("expected progress reports")
	}
}

func TestNotificationsRetentionTask_NilStorerIsNoOp(t *testing.T) {
	task := NewNotificationsRetentionTask(nil, nil)
	progress := &notificationsProgress{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute with nil store returned error: %v", err)
	}
	if len(progress.reports) == 0 {
		t.Fatal("expected at least one progress report")
	}
}

func TestNotificationsRetentionTask_PurgeError(t *testing.T) {
	wantErr := errors.New("db down")
	task := NewNotificationsRetentionTask(&fakeNotificationPurger{err: wantErr}, nil)
	progress := &notificationsProgress{}

	err := task.Execute(context.Background(), progress)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute error = %v, want %v", err, wantErr)
	}
}

func TestNotificationsRetentionTask_PushPurgerInvoked(t *testing.T) {
	notifPurger := &fakeNotificationPurger{returnCount: 5}
	pushPurger := &fakePushPurger{returnCount: 3}
	task := NewNotificationsRetentionTask(notifPurger, pushPurger)
	progress := &notificationsProgress{}

	before := time.Now()
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	after := time.Now()

	if pushPurger.calls != 1 {
		t.Fatalf("PurgeTerminal calls = %d, want 1", pushPurger.calls)
	}
	// cutoff should be ~7 days ago
	wantCutoff := before.AddDate(0, 0, -7)
	if pushPurger.lastBefore.Before(wantCutoff.Add(-time.Second)) || pushPurger.lastBefore.After(after.AddDate(0, 0, -7).Add(time.Second)) {
		t.Errorf("PurgeTerminal before = %v, want ~7 days before now", pushPurger.lastBefore)
	}
}

func TestNotificationsRetentionTask_NilPushPurgerIsNoOp(t *testing.T) {
	notifPurger := &fakeNotificationPurger{returnCount: 2}
	task := NewNotificationsRetentionTask(notifPurger, nil)
	progress := &notificationsProgress{}

	// Must not panic when pushPurger is nil.
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute with nil pushPurger returned error: %v", err)
	}
	if notifPurger.calls != 1 {
		t.Fatalf("PurgeOld calls = %d, want 1", notifPurger.calls)
	}
}

func TestNotificationsRetentionTask_PushPurgerError(t *testing.T) {
	notifPurger := &fakeNotificationPurger{returnCount: 1}
	pushPurger := &fakePushPurger{err: errors.New("push db error")}
	task := NewNotificationsRetentionTask(notifPurger, pushPurger)
	progress := &notificationsProgress{}

	// Push purge errors are logged as warnings, not propagated.
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute returned error on push purge failure: %v", err)
	}
	if pushPurger.calls != 1 {
		t.Fatalf("PurgeTerminal calls = %d, want 1", pushPurger.calls)
	}
}

// notificationsProgress is a shared fake ProgressReporter for notifications task tests.
type notificationsProgress struct {
	reports []string
}

func (p *notificationsProgress) Report(_ float64, msg string) {
	p.reports = append(p.reports, msg)
}

func (p *notificationsProgress) SetResultData(_ json.RawMessage) {}
