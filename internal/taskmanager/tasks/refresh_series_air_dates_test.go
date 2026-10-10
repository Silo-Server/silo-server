package tasks

import (
	"context"
	"errors"
	"strings"
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

func TestRefreshSeriesAirDatesTaskDrainsFullBatches(t *testing.T) {
	refresher := &fakeSeriesAirDateRefresher{results: []airDateRefreshResult{
		{due: refreshSeriesAirDatesBatchSize, updated: refreshSeriesAirDatesBatchSize},
		{due: 7, updated: 7},
	}}
	progress := &retentionProgress{}

	if err := NewRefreshSeriesAirDatesTask(refresher).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(refresher.limits) != 2 {
		t.Fatalf("RefreshDueSeriesAirDates calls = %d, want 2", len(refresher.limits))
	}
	for _, limit := range refresher.limits {
		if limit != refreshSeriesAirDatesBatchSize {
			t.Fatalf("limit = %d, want %d", limit, refreshSeriesAirDatesBatchSize)
		}
	}
	if !strings.Contains(progress.message, "507") {
		t.Fatalf("final progress message = %q, want updated total", progress.message)
	}
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

func TestRefreshSeriesAirDatesTaskReportsFailure(t *testing.T) {
	boom := errors.New("boom")
	refresher := &fakeSeriesAirDateRefresher{results: []airDateRefreshResult{
		{due: refreshSeriesAirDatesBatchSize, updated: refreshSeriesAirDatesBatchSize},
		{due: 3, err: boom},
	}}

	err := NewRefreshSeriesAirDatesTask(refresher).Execute(context.Background(), &retentionProgress{})
	if !errors.Is(err, boom) {
		t.Fatalf("Execute error = %v, want wrapped boom", err)
	}
	if !strings.Contains(err.Error(), "updated 500 series") {
		t.Fatalf("Execute error = %q, want updated count", err)
	}
}
