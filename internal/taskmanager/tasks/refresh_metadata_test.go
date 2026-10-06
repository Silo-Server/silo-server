package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/worker"
)

type fakeRefreshCandidateFinder struct {
	candidates []worker.RefreshCandidate
	batches    [][]worker.RefreshCandidate
	calls      int
	pruneCalls int
}

func (f *fakeRefreshCandidateFinder) FindCandidates(_ context.Context, _ int) ([]worker.RefreshCandidate, error) {
	f.calls++
	if len(f.batches) > 0 {
		batch := f.batches[0]
		f.batches = f.batches[1:]
		return append([]worker.RefreshCandidate(nil), batch...), nil
	}
	return append([]worker.RefreshCandidate(nil), f.candidates...), nil
}

func (f *fakeRefreshCandidateFinder) PruneDisabledLibraryDebt(_ context.Context) error {
	f.pruneCalls++
	return nil
}

type fakeMetadataRefresher struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeMetadataRefresher) RefreshScheduledTarget(_ context.Context, targetType, contentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if targetType == "" {
		targetType = "item"
	}
	f.calls = append(f.calls, targetType+":"+contentID)
	return nil
}

func (f *fakeMetadataRefresher) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type noopProgressReporter struct{}

func (noopProgressReporter) Report(float64, string)        {}
func (noopProgressReporter) SetResultData(json.RawMessage) {}

func TestRefreshMetadataTask_NoCandidates(t *testing.T) {
	finder := &fakeRefreshCandidateFinder{}
	refresher := &fakeMetadataRefresher{}
	task := NewRefreshMetadataTask(finder, refresher)

	if err := task.Execute(context.Background(), noopProgressReporter{}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if calls := refresher.Calls(); len(calls) != 0 {
		t.Fatalf("expected no refresh calls, got %v", calls)
	}
}

func TestRefreshMetadataTask_DrainsFullBatches(t *testing.T) {
	firstBatch := make([]worker.RefreshCandidate, refreshMetadataBatchSize)
	for i := range firstBatch {
		firstBatch[i] = worker.RefreshCandidate{TargetType: "item", ContentID: fmt.Sprintf("item-%03d", i)}
	}
	secondBatch := []worker.RefreshCandidate{{TargetType: "episode", ContentID: "final-item"}}
	finder := &fakeRefreshCandidateFinder{
		batches: [][]worker.RefreshCandidate{firstBatch, secondBatch},
	}
	refresher := &fakeMetadataRefresher{}
	task := NewRefreshMetadataTask(finder, refresher)

	if err := task.Execute(context.Background(), noopProgressReporter{}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	calls := refresher.Calls()
	if len(calls) != refreshMetadataBatchSize+1 {
		t.Fatalf("expected %d refresh calls, got %d", refreshMetadataBatchSize+1, len(calls))
	}
	if finder.pruneCalls != 1 {
		t.Fatalf("expected disabled-library prune once, got %d", finder.pruneCalls)
	}
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		seen[call] = true
	}
	if !seen["item:item-000"] || !seen["item:item-199"] || !seen["episode:final-item"] {
		t.Fatalf("expected calls from both claimed batches, got %v", calls)
	}
}
