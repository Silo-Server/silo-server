package catalog

import (
	"context"

	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestFilterSupersededProgressDropsOlderPartialsAfterLaterCompletedEpisode(t *testing.T) {
	t.Parallel()

	entries := []userstore.WatchProgress{
		{MediaItemID: "boys-s1e1"},
		{MediaItemID: "boys-s5e3"},
		{MediaItemID: "movie-1"},
	}
	superseded := map[string]struct{}{
		"boys-s1e1": {},
		"boys-s5e3": {},
	}

	filtered := FilterSupersededProgress(entries, superseded)

	if len(filtered) != 1 || filtered[0].MediaItemID != "movie-1" {
		t.Fatalf("filtered entries = %+v, want only movie-1", filtered)
	}
}

func TestCompletedProgressSnapshotsHaltsAtPageCap(t *testing.T) {
	t.Parallel()

	// More completed history than the page cap allows, all newer than the
	// (zero) cutoff so nothing halts the walk except the cap itself.
	entries := make([]userstore.WatchProgress, (supersededProgressMaxPages+1)*supersededProgressPageSize)
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range entries {
		entries[i] = userstore.WatchProgress{
			MediaItemID: "done-" + strconv.Itoa(i),
			UpdatedAt:   base.Add(time.Duration(-i) * time.Second).Format(time.RFC3339),
		}
	}
	store := &stubProgressLister{entries: entries}

	snapshots, err := CompletedProgressSnapshots(context.Background(), store, "p1", time.Time{})
	if err != nil {
		t.Fatalf("CompletedProgressSnapshots: %v", err)
	}
	if len(store.calls) != supersededProgressMaxPages {
		t.Fatalf("ListProgress calls = %d, want %d (page cap)", len(store.calls), supersededProgressMaxPages)
	}
	if len(snapshots) != supersededProgressMaxPages*supersededProgressPageSize {
		t.Fatalf("completed snapshots count = %d, want %d (capped pages)", len(snapshots), supersededProgressMaxPages*supersededProgressPageSize)
	}
}

func TestHomeDismissalIndexFilterProgressDropsOnlyMatchingTimestamps(t *testing.T) {
	t.Parallel()

	dismissedAt := "2025-01-01T00:00:00Z"
	resumedAt := "2025-01-02T00:00:00Z"
	idx := NewHomeDismissalIndex([]userstore.HomeItemDismissal{
		{MediaItemID: "still-dismissed", ProgressUpdatedAt: &dismissedAt},
		{MediaItemID: "resumed-since", ProgressUpdatedAt: &dismissedAt},
		{MediaItemID: "no-timestamp"},
	})

	entries := []userstore.WatchProgress{
		{MediaItemID: "still-dismissed", UpdatedAt: dismissedAt},
		{MediaItemID: "resumed-since", UpdatedAt: resumedAt},
		{MediaItemID: "no-timestamp", UpdatedAt: dismissedAt},
		{MediaItemID: "never-dismissed", UpdatedAt: dismissedAt},
	}

	filtered := idx.FilterProgress(entries)

	got := make([]string, 0, len(filtered))
	for _, entry := range filtered {
		got = append(got, entry.MediaItemID)
	}
	want := []string{"resumed-since", "no-timestamp", "never-dismissed"}
	if len(got) != len(want) {
		t.Fatalf("filtered = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filtered = %v, want %v", got, want)
		}
	}
}

type progressListCall struct {
	profileID string
	status    string
	limit     int
	offset    int
}

type stubProgressLister struct {
	entries []userstore.WatchProgress
	calls   []progressListCall
}

func (s *stubProgressLister) ListProgress(_ context.Context, profileID, status string, limit, offset int) ([]userstore.WatchProgress, error) {
	s.calls = append(s.calls, progressListCall{
		profileID: profileID,
		status:    status,
		limit:     limit,
		offset:    offset,
	})
	if offset >= len(s.entries) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.entries) {
		end = len(s.entries)
	}
	return s.entries[offset:end], nil
}

// completedWalkFixture builds n completed rows ordered updated_at DESC, the
// order ListProgress("completed") returns them in.
func completedWalkFixture(n int) []userstore.WatchProgress {
	entries := make([]userstore.WatchProgress, n)
	for i := range entries {
		entries[i] = userstore.WatchProgress{
			MediaItemID: "done-" + strconv.Itoa(i),
			UpdatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(n-i) * time.Minute).Format(time.RFC3339),
		}
	}
	return entries
}

func TestCompletedProgressCacheFallsBackForADifferentProfile(t *testing.T) {
	t.Parallel()

	entries := completedWalkFixture(10)
	store := &stubProgressLister{entries: entries}
	cache := NewCompletedProgressCache()

	if _, err := cache.snapshots(context.Background(), store, "p1", time.Time{}); err != nil {
		t.Fatalf("first profile: %v", err)
	}
	got, err := cache.snapshots(context.Background(), store, "p2", time.Time{})
	if err != nil {
		t.Fatalf("second profile: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("snapshots for p2 = %d, want %d from an uncached walk", len(got), len(entries))
	}
	last := store.calls[len(store.calls)-1]
	if last.profileID != "p2" || last.offset != 0 {
		t.Fatalf("last call = %+v, want a fresh offset-0 read for p2", last)
	}
}

// stubSinceLister serves ListCompletedProgressSince from the same fixture as
// the offset walk, so the two forms can be compared.
type stubSinceLister struct {
	stubProgressLister
	sinceCalls []int
	sinceAt    []time.Time
	rowsRead   int
}

func (s *stubSinceLister) ListCompletedProgressSince(_ context.Context, _ string, since, until time.Time, limit int) ([]userstore.WatchProgress, error) {
	s.sinceCalls = append(s.sinceCalls, limit)
	s.sinceAt = append(s.sinceAt, since)
	var out []userstore.WatchProgress
	for _, entry := range s.entries {
		updatedAt, _ := time.Parse(time.RFC3339, entry.UpdatedAt)
		if !until.IsZero() && updatedAt.After(until) {
			continue
		}
		if !updatedAt.After(since) {
			break
		}
		if len(out) == limit {
			break
		}
		out = append(out, entry)
	}
	s.rowsRead += len(out)
	return out, nil
}
