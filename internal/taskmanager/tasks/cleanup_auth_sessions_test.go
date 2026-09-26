package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// fakeSessionTable holds sessions by id with their expiry and models
// SessionRepository.DeleteExpired: every row past its expiry goes, revoked or
// not, and live rows stay.
type fakeSessionTable struct {
	now     time.Time
	expires map[string]time.Time
	err     error
	calls   int
}

func (f *fakeSessionTable) DeleteExpired(_ context.Context) (int, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	deleted := 0
	for id, exp := range f.expires {
		if exp.Before(f.now) {
			delete(f.expires, id)
			deleted++
		}
	}
	return deleted, nil
}

type authSessionCleanupProgress struct {
	reports []string
	result  json.RawMessage
}

func (p *authSessionCleanupProgress) Report(_ float64, message string) {
	p.reports = append(p.reports, message)
}

func (p *authSessionCleanupProgress) SetResultData(data json.RawMessage) {
	p.result = append(p.result[:0], data...)
}

func TestAuthSessionCleanupTaskRemovesExpiredRows(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	table := &fakeSessionTable{now: now, expires: map[string]time.Time{
		"expired-active":  now.Add(-time.Hour),
		"expired-revoked": now.Add(-24 * time.Hour),
		"live":            now.Add(time.Hour),
	}}
	task := NewAuthSessionCleanupTask(table)

	if task.Key() != "cleanup_auth_sessions" {
		t.Fatalf("Key() = %q", task.Key())
	}
	if task.Category() != taskmanager.TaskCategorySystem {
		t.Fatalf("Category() = %q", task.Category())
	}
	wantTriggers := []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
	gotTriggers := task.DefaultTriggers()
	if len(gotTriggers) != len(wantTriggers) || gotTriggers[0] != wantTriggers[0] || gotTriggers[1] != wantTriggers[1] {
		t.Fatalf("DefaultTriggers() = %#v, want %#v", gotTriggers, wantTriggers)
	}

	progress := &authSessionCleanupProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if table.calls != 1 {
		t.Fatalf("DeleteExpired calls = %d, want 1", table.calls)
	}
	if _, ok := table.expires["live"]; !ok || len(table.expires) != 1 {
		t.Fatalf("remaining rows = %v, want only live", table.expires)
	}
	if got := progress.reports[len(progress.reports)-1]; got != "Deleted 2 expired login sessions" {
		t.Fatalf("last progress report = %q", got)
	}
	var result authSessionCleanupResult
	if err := json.Unmarshal(progress.result, &result); err != nil || result.Deleted != 2 {
		t.Fatalf("result = %s (%v)", progress.result, err)
	}
}

func TestAuthSessionCleanupTaskReportsFailure(t *testing.T) {
	table := &fakeSessionTable{err: errors.New("boom")}
	progress := &authSessionCleanupProgress{}
	err := NewAuthSessionCleanupTask(table).Execute(context.Background(), progress)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("Execute error = %v", err)
	}
	if got := progress.reports[len(progress.reports)-1]; got != "Login session cleanup failed" {
		t.Fatalf("last progress report = %q", got)
	}
}
