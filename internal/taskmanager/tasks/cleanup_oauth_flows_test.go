package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeOAuthFlowTable answers DeleteExpiredFlows with a fixed count; the
// real thresholds are pinned by the auth package's database test.
type fakeOAuthFlowTable struct {
	deleted int
	err     error
	calls   int
}

func (f *fakeOAuthFlowTable) DeleteExpiredFlows(_ context.Context) (int, error) {
	f.calls++
	return f.deleted, f.err
}

func TestOAuthFlowCleanupTaskReportsDeletedRows(t *testing.T) {
	table := &fakeOAuthFlowTable{deleted: 3}
	task := NewOAuthFlowCleanupTask(table)
	if task.Key() != "cleanup_oauth_flows" {
		t.Fatalf("Key() = %q", task.Key())
	}
	progress := &authSessionCleanupProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if table.calls != 1 {
		t.Fatalf("DeleteExpiredFlows calls = %d", table.calls)
	}
	if got := progress.reports[len(progress.reports)-1]; got != "Deleted 3 expired OAuth sign-in flows" {
		t.Fatalf("last progress report = %q", got)
	}
	var result oauthFlowCleanupResult
	if err := json.Unmarshal(progress.result, &result); err != nil || result.Deleted != 3 {
		t.Fatalf("result = %s (%v)", progress.result, err)
	}
}

func TestOAuthFlowCleanupTaskReportsFailure(t *testing.T) {
	table := &fakeOAuthFlowTable{err: errors.New("boom")}
	progress := &authSessionCleanupProgress{}
	if err := NewOAuthFlowCleanupTask(table).Execute(context.Background(), progress); err == nil {
		t.Fatal("Execute succeeded with a failing delete")
	}
	if got := progress.reports[len(progress.reports)-1]; got != "OAuth sign-in flow cleanup failed" {
		t.Fatalf("last progress report = %q", got)
	}
}

func TestOAuthFlowCleanupTaskUnconfigured(t *testing.T) {
	progress := &authSessionCleanupProgress{}
	if err := NewOAuthFlowCleanupTask(nil).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}
