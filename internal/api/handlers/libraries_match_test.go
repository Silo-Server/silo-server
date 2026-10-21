package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type fakeMovieMatchQueue struct {
	pending      int
	parked       int
	entries      []models.MovieMatchQueueEntry
	retryFolders []int
	bulkCalls    int
}

func (f *fakeMovieMatchQueue) EnqueueMovieFile(context.Context, int) error { return nil }
func (f *fakeMovieMatchQueue) SyncForFolder(context.Context, int) error    { return nil }
func (f *fakeMovieMatchQueue) DeleteByFolder(context.Context, int) (int, error) {
	return 0, nil
}
func (f *fakeMovieMatchQueue) CountStatesByFolder(context.Context, int) (int, int, error) {
	return f.pending, f.parked, nil
}
func (f *fakeMovieMatchQueue) CountStatesByFolders(_ context.Context, folderIDs []int) (map[int]metadata.MatchQueueStateCounts, error) {
	f.bulkCalls++
	counts := make(map[int]metadata.MatchQueueStateCounts, len(folderIDs))
	for _, folderID := range folderIDs {
		counts[folderID] = metadata.MatchQueueStateCounts{Pending: f.pending, Parked: f.parked}
	}
	return counts, nil
}
func (f *fakeMovieMatchQueue) ListByFolder(context.Context, int, int, int) ([]models.MovieMatchQueueEntry, int, error) {
	return f.entries, len(f.entries), nil
}
func (f *fakeMovieMatchQueue) RetryNowByFolder(_ context.Context, folderID int) (int, error) {
	f.retryFolders = append(f.retryFolders, folderID)
	return len(f.retryFolders), nil
}

type fakeSeriesMatchQueue struct {
	pending      int
	parked       int
	entries      []models.SeriesRootMatchQueueEntry
	retryFolders []int
	bulkCalls    int
}

func (f *fakeSeriesMatchQueue) EnqueueSeriesRoot(context.Context, int, string) error { return nil }
func (f *fakeSeriesMatchQueue) WakeRoot(context.Context, int, string) error          { return nil }
func (f *fakeSeriesMatchQueue) SyncForFolder(context.Context, int) error             { return nil }
func (f *fakeSeriesMatchQueue) DeleteByFolder(context.Context, int) (int, error) {
	return 0, nil
}
func (f *fakeSeriesMatchQueue) CountStatesByFolder(context.Context, int) (int, int, error) {
	return f.pending, f.parked, nil
}
func (f *fakeSeriesMatchQueue) CountStatesByFolders(_ context.Context, folderIDs []int) (map[int]metadata.MatchQueueStateCounts, error) {
	f.bulkCalls++
	counts := make(map[int]metadata.MatchQueueStateCounts, len(folderIDs))
	for _, folderID := range folderIDs {
		counts[folderID] = metadata.MatchQueueStateCounts{Pending: f.pending, Parked: f.parked}
	}
	return counts, nil
}
func (f *fakeSeriesMatchQueue) ListByFolder(context.Context, int, int, int) ([]models.SeriesRootMatchQueueEntry, int, error) {
	return f.entries, len(f.entries), nil
}
func (f *fakeSeriesMatchQueue) RetryNowByFolder(_ context.Context, folderID int) (int, error) {
	f.retryFolders = append(f.retryFolders, folderID)
	return len(f.retryFolders), nil
}

type fakeRawMatchBacklog struct {
	count     int
	bulkCalls int
}

func (f *fakeRawMatchBacklog) CountUnmatchedMatchBacklogByFolder(context.Context, int, scanner.RawMatchBacklogMode) (int, error) {
	return f.count, nil
}
func (f *fakeRawMatchBacklog) CountUnmatchedMatchBacklogByFolders(_ context.Context, folderIDs []int, _ scanner.RawMatchBacklogMode) (map[int]int, error) {
	f.bulkCalls++
	counts := make(map[int]int, len(folderIDs))
	for _, folderID := range folderIDs {
		counts[folderID] = f.count
	}
	return counts, nil
}
func (f *fakeRawMatchBacklog) ListUnmatchedMatchBacklogByFolder(context.Context, int, scanner.RawMatchBacklogMode, int, int) ([]*models.MediaFile, int, error) {
	return nil, 0, nil
}
func (f *fakeRawMatchBacklog) SuppressUnmatchedMatchBacklogByFolder(context.Context, int, scanner.RawMatchBacklogMode) (int, error) {
	return 0, nil
}
func (f *fakeRawMatchBacklog) RetryUnmatchedMatchBacklogByFolder(context.Context, int, scanner.RawMatchBacklogMode) (int, error) {
	return 0, nil
}

// TestMetadataMatchQueueStatus_AggregatesStates verifies the pending/parked
// aggregation rules the admin UI depends on: parked rows stay out of
// pending_count, raw-backlog files count as pending, and the per-queue totals
// still equal pending + parked so movie_count/series_count semantics are
// unchanged from the pre-state API.
func TestMetadataMatchQueueStatus_AggregatesStates(t *testing.T) {
	h := &LibraryHandler{
		MovieMatchQueueRepo:  &fakeMovieMatchQueue{pending: 2, parked: 1},
		SeriesMatchQueueRepo: &fakeSeriesMatchQueue{pending: 3, parked: 2},
		RawMatchBacklogRepo:  &fakeRawMatchBacklog{count: 4},
	}

	status, err := h.metadataMatchQueueStatus(context.Background(), 7)
	if err != nil {
		t.Fatalf("metadataMatchQueueStatus() error = %v", err)
	}
	if status.LibraryID != 7 {
		t.Errorf("LibraryID = %d, want 7", status.LibraryID)
	}
	if status.MovieCount != 3 || status.SeriesCount != 5 || status.RawFileCount != 4 {
		t.Errorf("per-queue counts = %d/%d/%d, want 3/5/4", status.MovieCount, status.SeriesCount, status.RawFileCount)
	}
	if status.PendingCount != 9 {
		t.Errorf("PendingCount = %d, want 9 (2+3 pending + 4 raw)", status.PendingCount)
	}
	if status.ParkedCount != 3 {
		t.Errorf("ParkedCount = %d, want 3", status.ParkedCount)
	}
	if status.TotalCount != 12 {
		t.Errorf("TotalCount = %d, want 12", status.TotalCount)
	}
}

// TestWakeMetadataMatcher_RetriesBothQueues verifies the provider-chain-change
// wake path resets parked work in both queue repos for the changed library.
func TestWakeMetadataMatcher_RetriesBothQueues(t *testing.T) {
	movie := &fakeMovieMatchQueue{}
	series := &fakeSeriesMatchQueue{}
	h := &LibraryHandler{MovieMatchQueueRepo: movie, SeriesMatchQueueRepo: series}

	h.wakeMetadataMatcher(context.Background(), 42)

	if len(movie.retryFolders) != 1 || movie.retryFolders[0] != 42 {
		t.Errorf("movie retry folders = %v, want [42]", movie.retryFolders)
	}
	if len(series.retryFolders) != 1 || series.retryFolders[0] != 42 {
		t.Errorf("series retry folders = %v, want [42]", series.retryFolders)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
