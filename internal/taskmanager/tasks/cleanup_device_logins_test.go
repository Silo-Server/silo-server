package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeDeviceLoginTable answers DeleteExpired with a fixed count; the real
// one-day threshold is pinned by the auth package's database test.
type fakeDeviceLoginTable struct {
	deleted int
	err     error
	calls   int
}

func (f *fakeDeviceLoginTable) DeleteExpired(_ context.Context) (int, error) {
	f.calls++
	return f.deleted, f.err
}

func TestDeviceLoginCleanupTaskReportsDeletedRows(t *testing.T) {
	table := &fakeDeviceLoginTable{deleted: 3}
	task := NewDeviceLoginCleanupTask(table)
	if task.Key() != "cleanup_device_logins" {
		t.Fatalf("Key() = %q", task.Key())
	}
	progress := &authSessionCleanupProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if table.calls != 1 {
		t.Fatalf("DeleteExpired calls = %d", table.calls)
	}
	if got := progress.reports[len(progress.reports)-1]; got != "Deleted 3 expired device sign-in requests" {
		t.Fatalf("last progress report = %q", got)
	}
	var result deviceLoginCleanupResult
	if err := json.Unmarshal(progress.result, &result); err != nil || result.Deleted != 3 {
		t.Fatalf("result = %s (%v)", progress.result, err)
	}
}

func TestDeviceLoginCleanupTaskReportsFailure(t *testing.T) {
	table := &fakeDeviceLoginTable{err: errors.New("boom")}
	progress := &authSessionCleanupProgress{}
	if err := NewDeviceLoginCleanupTask(table).Execute(context.Background(), progress); err == nil {
		t.Fatal("Execute succeeded with a failing delete")
	}
	if got := progress.reports[len(progress.reports)-1]; got != "Device sign-in request cleanup failed" {
		t.Fatalf("last progress report = %q", got)
	}
}

func TestDeviceLoginCleanupTaskUnconfigured(t *testing.T) {
	progress := &authSessionCleanupProgress{}
	if err := NewDeviceLoginCleanupTask(nil).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}
