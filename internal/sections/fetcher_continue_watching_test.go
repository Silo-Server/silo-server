package sections

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestCollapseContinueWatchingSeriesCandidatesPrefersNewestInProgressEpisode(t *testing.T) {
	t.Parallel()

	seriesID := "series-8"
	importedAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	localAt := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	nextUpAt := time.Date(2025, 1, 3, 12, 0, 0, 0, time.UTC)

	items := []*models.MediaItem{
		{ContentID: "movie-1", Type: "movie", Title: "Movie One"},
		{ContentID: "ep-s8e6", Type: "episode", Title: "Imported partial"},
		{ContentID: "ep-s8e11", Type: "episode", Title: "Local partial"},
		{ContentID: "ep-s8e12", Type: "episode", Title: "Next up"},
		{ContentID: "movie-2", Type: "movie", Title: "Movie Two"},
	}
	meta := map[string]SectionItemMeta{
		"ep-s8e6": {
			SeriesID:      &seriesID,
			SeasonNumber:  intPtr(8),
			EpisodeNumber: intPtr(6),
			ItemSource:    "in_progress",
			SortTimestamp: importedAt,
		},
		"ep-s8e11": {
			SeriesID:      &seriesID,
			SeasonNumber:  intPtr(8),
			EpisodeNumber: intPtr(11),
			ItemSource:    "in_progress",
			SortTimestamp: localAt,
		},
		"ep-s8e12": {
			SeriesID:      &seriesID,
			SeasonNumber:  intPtr(8),
			EpisodeNumber: intPtr(12),
			ItemSource:    "next_up",
			SortTimestamp: nextUpAt,
		},
	}

	collapsed := collapseContinueWatchingSeriesCandidates(items, meta)

	gotIDs := contentIDs(collapsed)
	wantIDs := []string{"movie-1", "ep-s8e11", "movie-2"}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("collapsed IDs = %v, want %v", gotIDs, wantIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("collapsed IDs = %v, want %v", gotIDs, wantIDs)
		}
	}
}

func TestCollapseContinueWatchingSeriesCandidatesKeepsNextUpWhenNoInProgress(t *testing.T) {
	t.Parallel()

	seriesID := "series-8"
	items := []*models.MediaItem{
		{ContentID: "ep-s8e12", Type: "episode", Title: "Next up"},
	}
	meta := map[string]SectionItemMeta{
		"ep-s8e12": {
			SeriesID:      &seriesID,
			SeasonNumber:  intPtr(8),
			EpisodeNumber: intPtr(12),
			ItemSource:    "next_up",
			SortTimestamp: time.Date(2025, 1, 3, 12, 0, 0, 0, time.UTC),
		},
	}

	collapsed := collapseContinueWatchingSeriesCandidates(items, meta)

	if len(collapsed) != 1 || collapsed[0].ContentID != "ep-s8e12" {
		t.Fatalf("collapsed = %v, want only ep-s8e12", contentIDs(collapsed))
	}
}

func TestFilterSupersededEpisodeProgressEntriesDropsOlderPartialsAfterLaterCompletedEpisode(t *testing.T) {
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

	filtered := filterSupersededEpisodeProgressEntries(entries, superseded)

	if len(filtered) != 1 || filtered[0].MediaItemID != "movie-1" {
		t.Fatalf("filtered entries = %+v, want only movie-1", filtered)
	}
}

func TestMatchesContinueWatchingFilterIncludesAudiobooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		filterType string
		itemType   string
		want       bool
	}{
		{name: "movie keeps movie", filterType: "movie", itemType: "movie", want: true},
		{name: "series keeps episode", filterType: "series", itemType: "episode", want: true},
		{name: "audiobook keeps audiobook", filterType: "audiobook", itemType: "audiobook", want: true},
		{name: "audiobook rejects movie", filterType: "audiobook", itemType: "movie", want: false},
		{name: "unknown rejects audiobook", filterType: "unknown", itemType: "audiobook", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesContinueWatchingFilter(tt.filterType, tt.itemType); got != tt.want {
				t.Fatalf("matchesContinueWatchingFilter(%q, %q) = %v, want %v", tt.filterType, tt.itemType, got, tt.want)
			}
		})
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

	snapshots, err := completedProgressSnapshots(context.Background(), store, "p1")
	if err != nil {
		t.Fatalf("completedProgressSnapshots: %v", err)
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

func contentIDs(items []*models.MediaItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ContentID)
	}
	return ids
}

func intPtr(v int) *int {
	return &v
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
