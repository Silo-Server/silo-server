package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeNotificationDigester struct {
	calls     int
	lastSince time.Time
	err       error
}

func (f *fakeNotificationDigester) RunDailyDigest(_ context.Context, since time.Time) error {
	f.calls++
	f.lastSince = since
	return f.err
}

func TestNotificationsDigestTask_Metadata(t *testing.T) {
	task := NewNotificationsDigestTask(&fakeNotificationDigester{})

	if task.Key() != "notifications_digest" {
		t.Fatalf("Key() = %q, want notifications_digest", task.Key())
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

func TestNotificationsDigestTask_Execute(t *testing.T) {
	digester := &fakeNotificationDigester{}
	task := NewNotificationsDigestTask(digester)
	progress := &notificationsProgress{}

	before := time.Now()
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	after := time.Now()

	if digester.calls != 1 {
		t.Fatalf("RunDailyDigest calls = %d, want 1", digester.calls)
	}
	// since should be ~24h ago
	wantSince := before.Add(-24 * time.Hour)
	if digester.lastSince.Before(wantSince.Add(-time.Second)) || digester.lastSince.After(after.Add(-24*time.Hour+time.Second)) {
		t.Errorf("since = %v, want ~24h before now", digester.lastSince)
	}
	if len(progress.reports) == 0 {
		t.Fatal("expected progress reports")
	}
}

func TestNotificationsDigestTask_NilDigesterIsNoOp(t *testing.T) {
	task := NewNotificationsDigestTask(nil)
	progress := &notificationsProgress{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute with nil digester returned error: %v", err)
	}
	if len(progress.reports) == 0 {
		t.Fatal("expected at least one progress report")
	}
}

func TestNotificationsDigestTask_DigestError(t *testing.T) {
	wantErr := errors.New("digest failed")
	task := NewNotificationsDigestTask(&fakeNotificationDigester{err: wantErr})
	progress := &notificationsProgress{}

	err := task.Execute(context.Background(), progress)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute error = %v, want %v", err, wantErr)
	}
}
