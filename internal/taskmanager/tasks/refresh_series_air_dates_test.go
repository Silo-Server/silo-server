package tasks

import (
	"context"
	"testing"
)

type airDateRefreshResult struct {
	due     int
	updated int
	err     error
}

type fakeSeriesAirDateRefresher struct {
	results []airDateRefreshResult
	limits  []int
}

func (f *fakeSeriesAirDateRefresher) RefreshDueSeriesAirDates(_ context.Context, limit int) (int, int, error) {
	f.limits = append(f.limits, limit)
	if len(f.results) == 0 {
		return 0, 0, nil
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result.due, result.updated, result.err
}

func TestRefreshSeriesAirDatesTaskStopsWhenFullBatchMakesNoProgress(t *testing.T) {
	refresher := &fakeSeriesAirDateRefresher{results: []airDateRefreshResult{
		{due: refreshSeriesAirDatesBatchSize, updated: 0},
		{due: refreshSeriesAirDatesBatchSize, updated: 0},
	}}

	if err := NewRefreshSeriesAirDatesTask(refresher).Execute(context.Background(), &retentionProgress{}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(refresher.limits) != 1 {
		t.Fatalf("RefreshDueSeriesAirDates calls = %d, want 1", len(refresher.limits))
	}
}
