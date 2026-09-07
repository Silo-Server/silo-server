package catalog

import (
	"context"
	"strconv"
	"strings"
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

func TestCompletedProgressSnapshotsPagesThroughConfiguredStore(t *testing.T) {
	t.Parallel()

	entries := make([]userstore.WatchProgress, supersededProgressPageSize+1)
	for i := range entries {
		entries[i] = userstore.WatchProgress{
			MediaItemID: "done-" + time.Unix(int64(i), 0).Format("150405"),
			UpdatedAt:   time.Date(2025, 1, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339),
		}
	}
	store := &stubProgressLister{entries: entries}

	snapshots, err := CompletedProgressSnapshots(context.Background(), store, "p1", time.Time{})
	if err != nil {
		t.Fatalf("CompletedProgressSnapshots: %v", err)
	}
	if len(snapshots) != len(entries) {
		t.Fatalf("completed snapshots count = %d, want %d", len(snapshots), len(entries))
	}
	if len(store.calls) != 2 {
		t.Fatalf("ListProgress calls = %+v, want 2 paged calls", store.calls)
	}
	if store.calls[0] != (progressListCall{profileID: "p1", status: "completed", limit: supersededProgressPageSize, offset: 0}) {
		t.Fatalf("first ListProgress call = %+v", store.calls[0])
	}
	if store.calls[1] != (progressListCall{profileID: "p1", status: "completed", limit: supersededProgressPageSize, offset: supersededProgressPageSize}) {
		t.Fatalf("second ListProgress call = %+v", store.calls[1])
	}
}

func TestCompletedProgressSnapshotsStopsAtCutoff(t *testing.T) {
	t.Parallel()

	// Newest-first, spanning two full pages: the store hands rows back in
	// updated_at DESC order the same way the completed listing query does.
	entries := make([]userstore.WatchProgress, 2*supersededProgressPageSize)
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range entries {
		entries[i] = userstore.WatchProgress{
			MediaItemID: "done-" + strconv.Itoa(i),
			UpdatedAt:   base.Add(time.Duration(-i) * time.Minute).Format(time.RFC3339),
		}
	}
	store := &stubProgressLister{entries: entries}

	// Cut off inside the first page: only rows strictly newer than the cutoff
	// are returned, and paging stops without ever reading the second page.
	cutoff := base.Add(-10 * time.Minute)
	snapshots, err := CompletedProgressSnapshots(context.Background(), store, "p1", cutoff)
	if err != nil {
		t.Fatalf("CompletedProgressSnapshots: %v", err)
	}
	if len(snapshots) != 10 {
		t.Fatalf("completed snapshots count = %d, want 10 (rows newer than cutoff)", len(snapshots))
	}
	if len(store.calls) != 1 {
		t.Fatalf("ListProgress calls = %+v, want a single page before the cutoff halts paging", store.calls)
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

func TestBuildSupersededEpisodeProgressQueryUsesStoreSnapshotsWithFreshnessGate(t *testing.T) {
	t.Parallel()

	query := buildSupersededEpisodeProgressQuery()
	expectedFragments := []string{
		"unnest($1::text[], $2::timestamptz[])",
		"unnest($3::text[], $4::timestamptz[])",
		"FROM in_progress ip_progress",
		"done_progress.updated_at > ip_progress.updated_at",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected superseded progress query to contain %q, got:\n%s", fragment, query)
		}
	}
	unexpectedFragments := []string{
		"user_watch_progress",
		"user_history_hidden_items",
	}
	for _, fragment := range unexpectedFragments {
		if strings.Contains(query, fragment) {
			t.Fatalf("superseded progress query contains %q, got:\n%s", fragment, query)
		}
	}
}

func TestSupersededEpisodeProgressIDsWithoutPoolReturnsEmptySet(t *testing.T) {
	t.Parallel()

	filter := NewContinueWatchingProgressFilter(nil)
	entries := []userstore.WatchProgress{{
		MediaItemID: "ep-1",
		UpdatedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}}
	store := &stubProgressLister{}

	superseded, err := filter.SupersededEpisodeProgressIDs(context.Background(), store, "p1", entries)
	if err != nil {
		t.Fatalf("SupersededEpisodeProgressIDs: %v", err)
	}
	if len(superseded) != 0 {
		t.Fatalf("superseded = %v, want empty set", superseded)
	}
	if len(store.calls) != 0 {
		t.Fatalf("ListProgress calls = %+v, want none without a pool", store.calls)
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

func TestProgressSnapshotsSkipsBlankIDsAndBadTimestamps(t *testing.T) {
	t.Parallel()

	valid := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := []userstore.WatchProgress{
		{MediaItemID: "ok", UpdatedAt: valid.Format(time.RFC3339)},
		{MediaItemID: "  ", UpdatedAt: valid.Format(time.RFC3339)},
		{MediaItemID: "bad-time", UpdatedAt: "not-a-time"},
	}

	snapshots := ProgressSnapshots(entries)

	if len(snapshots) != 1 || snapshots[0].ContentID != "ok" || !snapshots[0].UpdatedAt.Equal(valid) {
		t.Fatalf("snapshots = %+v, want single valid snapshot for %q", snapshots, "ok")
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

func TestCompletedProgressCacheMatchesFreshWalkForNestedCutoffs(t *testing.T) {
	t.Parallel()

	entries := completedWalkFixture(supersededProgressPageSize*2 + 40)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Continue Watching pages in-progress rows updated_at DESC, so each page
	// asks for an older cutoff than the last. Walk from newest to oldest.
	cutoffs := []time.Time{
		base.Add(1000 * time.Minute),
		base.Add(600 * time.Minute),
		base.Add(200 * time.Minute),
		base.Add(30 * time.Minute),
		{},
	}

	cached := NewCompletedProgressCache()
	cachedStore := &stubProgressLister{entries: entries}

	for _, cutoff := range cutoffs {
		freshStore := &stubProgressLister{entries: entries}
		want, err := CompletedProgressSnapshots(context.Background(), freshStore, "p1", cutoff)
		if err != nil {
			t.Fatalf("fresh walk at %v: %v", cutoff, err)
		}
		got, err := cached.snapshots(context.Background(), cachedStore, "p1", cutoff)
		if err != nil {
			t.Fatalf("cached walk at %v: %v", cutoff, err)
		}
		if len(got) != len(want) {
			t.Fatalf("cutoff %v: cached returned %d snapshots, fresh returned %d", cutoff, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("cutoff %v: snapshot %d = %+v, want %+v", cutoff, i, got[i], want[i])
			}
		}
	}

	// The whole fixture is three pages. A fresh walk per cutoff would have read
	// far more; the cache must never read a page twice.
	wantPages := 3
	if len(cachedStore.calls) != wantPages {
		t.Fatalf("cached ListProgress calls = %d (%+v), want %d — each page read exactly once", len(cachedStore.calls), cachedStore.calls, wantPages)
	}
	for i, call := range cachedStore.calls {
		if call.offset != i*supersededProgressPageSize {
			t.Fatalf("call %d offset = %d, want %d — the walk must read strictly forward", i, call.offset, i*supersededProgressPageSize)
		}
	}
}

func TestCompletedProgressCacheStopsEarlyWhenCutoffIsSatisfied(t *testing.T) {
	t.Parallel()

	entries := completedWalkFixture(supersededProgressPageSize * 3)
	store := &stubProgressLister{entries: entries}
	cache := NewCompletedProgressCache()

	// A cutoff inside the first page must not drag in later pages.
	cutoff, err := time.Parse(time.RFC3339, entries[10].UpdatedAt)
	if err != nil {
		t.Fatalf("parsing fixture cutoff: %v", err)
	}
	cutoff = cutoff.UTC()
	got, err := cache.snapshots(context.Background(), store, "p1", cutoff)
	if err != nil {
		t.Fatalf("cached walk: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("snapshots = %d, want 10 rows newer than the cutoff", len(got))
	}
	if len(store.calls) != 1 {
		t.Fatalf("ListProgress calls = %d, want 1 — a satisfied cutoff must not page further", len(store.calls))
	}
}

func TestCompletedProgressCacheHonoursPageCap(t *testing.T) {
	t.Parallel()

	entries := completedWalkFixture(supersededProgressPageSize * (supersededProgressMaxPages + 3))
	store := &stubProgressLister{entries: entries}
	cache := NewCompletedProgressCache()

	got, err := cache.snapshots(context.Background(), store, "p1", time.Time{})
	if err != nil {
		t.Fatalf("cached walk: %v", err)
	}
	if len(store.calls) != supersededProgressMaxPages {
		t.Fatalf("ListProgress calls = %d, want the %d-page cap", len(store.calls), supersededProgressMaxPages)
	}
	if len(got) != supersededProgressPageSize*supersededProgressMaxPages {
		t.Fatalf("snapshots = %d, want the capped window", len(got))
	}
	// Once capped the cache is done: a further ask must not resume paging.
	if _, err := cache.snapshots(context.Background(), store, "p1", time.Time{}); err != nil {
		t.Fatalf("second cached walk: %v", err)
	}
	if len(store.calls) != supersededProgressMaxPages {
		t.Fatalf("ListProgress calls after second ask = %d, want no further paging", len(store.calls))
	}
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
