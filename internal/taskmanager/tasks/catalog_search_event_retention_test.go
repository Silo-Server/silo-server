package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type retentionPrunerCall struct {
	provider string
	cutoff   time.Time
	afterID  int64
	limit    int
}

type fakeCatalogSearchEventPruner struct {
	results []retentionPruneResult
	err     error
	calls   []retentionPrunerCall
}

type retentionPruneResult struct {
	deleted int64
	lastID  int64
}

func (p *fakeCatalogSearchEventPruner) PruneProcessed(_ context.Context, provider string, cutoff time.Time, afterID int64, limit int) (int64, int64, error) {
	p.calls = append(p.calls, retentionPrunerCall{provider: provider, cutoff: cutoff, afterID: afterID, limit: limit})
	if len(p.results) == 0 {
		return 0, afterID, p.err
	}
	result := p.results[0]
	p.results = p.results[1:]
	return result.deleted, result.lastID, p.err
}

type retentionProgress struct{ message string }

func (p *retentionProgress) Report(_ float64, message string) { p.message = message }
func (*retentionProgress) SetResultData(json.RawMessage)      {}

func TestCatalogSearchEventRetentionTaskPrunesInBatches(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	pruner := &fakeCatalogSearchEventPruner{results: []retentionPruneResult{
		{deleted: catalogSearchEventBatchSize, lastID: 7000},
		{deleted: 3, lastID: 9000},
	}}
	task := NewCatalogSearchEventRetentionTask(pruner)
	task.now = func() time.Time { return now }
	progress := &retentionProgress{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(pruner.calls) != 2 {
		t.Fatalf("PruneProcessed calls = %d, want 2", len(pruner.calls))
	}
	if got := pruner.calls[1]; got.limit != catalogSearchEventBatchSize {
		t.Fatalf("second PruneProcessed call = %+v", got)
	}
	if got := pruner.calls[1].afterID; got != 7000 {
		t.Fatalf("second PruneProcessed afterID = %d, want 7000", got)
	}
	for _, call := range pruner.calls {
		if call.provider != "meilisearch" || call.limit != catalogSearchEventBatchSize {
			t.Fatalf("PruneProcessed call = %+v", call)
		}
		if want := now.Add(-catalogSearchEventRetention); !call.cutoff.Equal(want) {
			t.Fatalf("cutoff = %v, want %v", call.cutoff, want)
		}
	}
	if !strings.Contains(progress.message, "5003") {
		t.Fatalf("final progress message = %q, want deleted total", progress.message)
	}
}

func TestCatalogSearchEventRetentionTaskReportsPartialFailure(t *testing.T) {
	pruner := &fakeCatalogSearchEventPruner{
		results: []retentionPruneResult{{deleted: catalogSearchEventBatchSize, lastID: 5000}},
		err:     errors.New("database unavailable"),
	}
	task := NewCatalogSearchEventRetentionTask(pruner)

	err := task.Execute(context.Background(), &retentionProgress{})
	if err == nil || !strings.Contains(err.Error(), "deleted 5000 rows") {
		t.Fatalf("Execute error = %v, want partial deletion count", err)
	}
}
