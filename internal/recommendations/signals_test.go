package recommendations

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type fakeSignalRepo struct {
	canonical map[string]string

	fallbackWatched         map[string]struct{}
	fallbackProgress        []WatchProgressRow
	ebookProgress           []WatchProgressRow
	fallbackRecentCompleted []string
	fallbackRewatches       []RewatchCount
}

func (r *fakeSignalRepo) GetWatchedItemIDSet(context.Context, int, string) (map[string]struct{}, error) {
	return r.fallbackWatched, nil
}

func (r *fakeSignalRepo) GetWatchProgressForUser(context.Context, int, string) ([]WatchProgressRow, error) {
	return r.fallbackProgress, nil
}

func (r *fakeSignalRepo) GetEbookReaderProgressForUser(context.Context, int, string) ([]WatchProgressRow, error) {
	return r.ebookProgress, nil
}

func (r *fakeSignalRepo) GetRecentCompletedItemIDs(context.Context, int, string, int) ([]string, error) {
	return r.fallbackRecentCompleted, nil
}

func (r *fakeSignalRepo) GetRewatchCounts(context.Context, int, string) ([]RewatchCount, error) {
	return r.fallbackRewatches, nil
}

func (r *fakeSignalRepo) ResolveCanonicalItemIDSet(_ context.Context, contentIDs []string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(contentIDs))
	for _, id := range contentIDs {
		if canonical, ok := r.canonical[id]; ok {
			set[canonical] = struct{}{}
			continue
		}
		set[id] = struct{}{}
	}
	return set, nil
}

type fakeSignalProvider struct {
	store userstore.UserStore
}

func (p fakeSignalProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p fakeSignalProvider) Close() error {
	return nil
}

type fakeSignalStore struct {
	userstore.UserStore

	progress []userstore.WatchProgress
	history  []userstore.WatchHistoryEntry
	profile  *userstore.Profile
}

func (s *fakeSignalStore) ListProgress(_ context.Context, profileID, status string, limit, offset int) ([]userstore.WatchProgress, error) {
	filtered := make([]userstore.WatchProgress, 0, len(s.progress))
	for _, progress := range s.progress {
		if progress.ProfileID != profileID {
			continue
		}
		switch status {
		case "completed":
			if !progress.Completed {
				continue
			}
		case "in_progress":
			if progress.Completed {
				continue
			}
		}
		filtered = append(filtered, progress)
	}
	slices.SortStableFunc(filtered, func(a, b userstore.WatchProgress) int {
		left := parseSignalTime(a.UpdatedAt, time.Time{})
		right := parseSignalTime(b.UpdatedAt, time.Time{})
		if left.After(right) {
			return -1
		}
		if right.After(left) {
			return 1
		}
		if a.MediaItemID < b.MediaItemID {
			return -1
		}
		if a.MediaItemID > b.MediaItemID {
			return 1
		}
		return 0
	})

	if offset >= len(filtered) {
		return []userstore.WatchProgress{}, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], nil
}

func (s *fakeSignalStore) ListCompletedHistory(_ context.Context, query userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error) {
	filtered := make([]userstore.WatchHistoryEntry, 0, len(s.history))
	for _, entry := range s.history {
		if entry.ProfileID == query.ProfileID && entry.Completed {
			filtered = append(filtered, entry)
		}
	}
	if query.Offset >= len(filtered) {
		return []userstore.WatchHistoryEntry{}, nil
	}
	end := query.Offset + query.Limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[query.Offset:end], nil
}

func (s *fakeSignalStore) GetProfile(context.Context, string) (*userstore.Profile, error) {
	return s.profile, nil
}

func TestSignalReaderWatchedSetCanonicalizesStoreProgress(t *testing.T) {
	store := &fakeSignalStore{progress: []userstore.WatchProgress{
		{ProfileID: "p1", MediaItemID: "episode-1", Completed: true},
		{ProfileID: "p1", MediaItemID: "movie-half", PositionSeconds: 60, DurationSeconds: 100},
		{ProfileID: "p1", MediaItemID: "movie-low", PositionSeconds: 40, DurationSeconds: 100},
		{ProfileID: "other", MediaItemID: "other-complete", Completed: true},
	}}
	repo := &fakeSignalRepo{canonical: map[string]string{
		"episode-1": "series-1",
	}}
	reader := NewSignalReader(repo, fakeSignalProvider{store: store})

	watched, err := reader.WatchedItemIDSet(context.Background(), 7, "p1")
	if err != nil {
		t.Fatalf("WatchedItemIDSet returned error: %v", err)
	}

	if _, ok := watched["series-1"]; !ok {
		t.Fatalf("expected episode progress to canonicalize to series, got %#v", watched)
	}
	if _, ok := watched["movie-half"]; !ok {
		t.Fatalf("expected half-watched movie in watched set, got %#v", watched)
	}
	if _, ok := watched["movie-low"]; ok {
		t.Fatalf("did not expect low-progress movie in watched set: %#v", watched)
	}
}

func TestSignalReaderWatchedSetIncludesEbookReaderProgress(t *testing.T) {
	store := &fakeSignalStore{}
	repo := &fakeSignalRepo{
		ebookProgress: []WatchProgressRow{
			{MediaItemID: "ebook-half", PositionSeconds: 0.6, DurationSeconds: 1, UpdatedAt: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)},
			{MediaItemID: "ebook-low", PositionSeconds: 0.2, DurationSeconds: 1, UpdatedAt: time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)},
		},
	}
	reader := NewSignalReader(repo, fakeSignalProvider{store: store})

	watched, err := reader.WatchedItemIDSet(context.Background(), 7, "p1")
	if err != nil {
		t.Fatalf("WatchedItemIDSet returned error: %v", err)
	}

	if _, ok := watched["ebook-half"]; !ok {
		t.Fatalf("expected ebook-half in watched set, got %#v", watched)
	}
	if _, ok := watched["ebook-low"]; ok {
		t.Fatalf("did not expect low-progress ebook in watched set: %#v", watched)
	}
}

func TestSignalReaderWatchProgressIncludesEbookReaderProgress(t *testing.T) {
	store := &fakeSignalStore{progress: []userstore.WatchProgress{
		{ProfileID: "p1", MediaItemID: "movie", PositionSeconds: 60, DurationSeconds: 100, UpdatedAt: "2026-06-01T10:00:00Z"},
	}}
	repo := &fakeSignalRepo{
		ebookProgress: []WatchProgressRow{
			{MediaItemID: "ebook", PositionSeconds: 0.42, DurationSeconds: 1, Completed: false, UpdatedAt: time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)},
		},
	}
	reader := NewSignalReader(repo, fakeSignalProvider{store: store})

	progress, err := reader.WatchProgressForUser(context.Background(), 7, "p1")
	if err != nil {
		t.Fatalf("WatchProgressForUser returned error: %v", err)
	}

	if !slices.ContainsFunc(progress, func(row WatchProgressRow) bool {
		return row.MediaItemID == "ebook" && row.PositionSeconds == 0.42 && row.DurationSeconds == 1
	}) {
		t.Fatalf("ebook progress missing from rows: %#v", progress)
	}
}

func TestSignalReaderRecentCompletedUsesStoreUpdatedOrder(t *testing.T) {
	store := &fakeSignalStore{progress: []userstore.WatchProgress{
		{ProfileID: "p1", MediaItemID: "older", Completed: true, UpdatedAt: "2026-05-01T10:00:00Z"},
		{ProfileID: "p1", MediaItemID: "newer", Completed: true, UpdatedAt: "2026-05-02T10:00:00Z"},
		{ProfileID: "p1", MediaItemID: "newest", Completed: true, UpdatedAt: "2026-05-03T10:00:00Z"},
		{ProfileID: "p1", MediaItemID: "unfinished", Completed: false, UpdatedAt: "2026-05-04T10:00:00Z"},
	}}
	reader := NewSignalReader(&fakeSignalRepo{}, fakeSignalProvider{store: store})

	ids, err := reader.RecentCompletedItemIDs(context.Background(), 7, "p1", 2)
	if err != nil {
		t.Fatalf("RecentCompletedItemIDs returned error: %v", err)
	}

	want := []string{"newest", "newer"}
	if !slices.Equal(ids, want) {
		t.Fatalf("recent completed = %#v, want %#v", ids, want)
	}
}

func TestSignalReaderRecentCompletedIncludesEbookReaderProgress(t *testing.T) {
	store := &fakeSignalStore{progress: []userstore.WatchProgress{
		{ProfileID: "p1", MediaItemID: "movie", Completed: true, UpdatedAt: "2026-06-01T10:00:00Z"},
	}}
	repo := &fakeSignalRepo{
		ebookProgress: []WatchProgressRow{
			{MediaItemID: "ebook-done", PositionSeconds: 0.95, DurationSeconds: 1, Completed: true, UpdatedAt: time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)},
			{MediaItemID: "ebook-open", PositionSeconds: 0.4, DurationSeconds: 1, Completed: false, UpdatedAt: time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)},
		},
	}
	reader := NewSignalReader(repo, fakeSignalProvider{store: store})

	ids, err := reader.RecentCompletedItemIDs(context.Background(), 7, "p1", 3)
	if err != nil {
		t.Fatalf("RecentCompletedItemIDs returned error: %v", err)
	}

	want := []string{"ebook-done", "movie"}
	if !slices.Equal(ids, want) {
		t.Fatalf("recent completed = %#v, want %#v", ids, want)
	}
}

func TestSignalReaderRewatchCountsAggregatesCompletedHistory(t *testing.T) {
	store := &fakeSignalStore{history: []userstore.WatchHistoryEntry{
		{ProfileID: "p1", MediaItemID: "rewatched", Completed: true, WatchedAt: "2026-05-01T10:00:00Z"},
		{ProfileID: "p1", MediaItemID: "rewatched", Completed: true, WatchedAt: "2026-05-03T10:00:00Z"},
		{ProfileID: "p1", MediaItemID: "once", Completed: true, WatchedAt: "2026-05-02T10:00:00Z"},
		{ProfileID: "p1", MediaItemID: "unfinished", Completed: false, WatchedAt: "2026-05-04T10:00:00Z"},
	}}
	reader := NewSignalReader(&fakeSignalRepo{}, fakeSignalProvider{store: store})

	counts, err := reader.RewatchCounts(context.Background(), 7, "p1")
	if err != nil {
		t.Fatalf("RewatchCounts returned error: %v", err)
	}
	if len(counts) != 1 {
		t.Fatalf("got %#v, want exactly one rewatch count", counts)
	}
	if counts[0].MediaItemID != "rewatched" || counts[0].Count != 2 {
		t.Fatalf("unexpected rewatch count: %#v", counts[0])
	}
	wantTime := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	if !counts[0].LastWatchedAt.Equal(wantTime) {
		t.Fatalf("last watched = %s, want %s", counts[0].LastWatchedAt, wantTime)
	}
}

func TestSignalReaderFallsBackWhenNoStoreProvider(t *testing.T) {
	repo := &fakeSignalRepo{fallbackRecentCompleted: []string{"from-repo"}}
	reader := NewSignalReader(repo, nil)

	ids, err := reader.RecentCompletedItemIDs(context.Background(), 7, "p1", 3)
	if err != nil {
		t.Fatalf("RecentCompletedItemIDs returned error: %v", err)
	}
	if !slices.Equal(ids, []string{"from-repo"}) {
		t.Fatalf("ids = %#v, want repo fallback", ids)
	}
}

func TestProfileAccessFilterUsesStoredStableProfileRestrictions(t *testing.T) {
	store := &fakeSignalStore{profile: &userstore.Profile{
		ID:                         "p1",
		MaxContentRating:           "PG-13",
		LibraryRestrictionsEnabled: true,
		AllowedLibraryIDs:          []int{2, 5},
	}}
	engine := &Engine{storeProvider: fakeSignalProvider{store: store}}

	filter := engine.profileAccessFilter(context.Background(), 7, "p1")
	if filter.UserID != 7 || filter.ProfileID != "p1" {
		t.Fatalf("unexpected filter identity: %#v", filter)
	}
	if filter.MaxContentRating != "PG-13" {
		t.Fatalf("MaxContentRating = %q, want PG-13", filter.MaxContentRating)
	}
	if !slices.Equal(filter.AllowedLibraryIDs, []int{2, 5}) {
		t.Fatalf("AllowedLibraryIDs = %#v, want [2 5]", filter.AllowedLibraryIDs)
	}
	if filter.DisabledLibraryIDs != nil {
		t.Fatalf("DisabledLibraryIDs should remain request-time only, got %#v", filter.DisabledLibraryIDs)
	}
}
