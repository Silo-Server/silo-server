package recommendations

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type fakeSignalRepo struct {
	canonical map[string]string

	fallbackWatched         map[string]struct{}
	watchedReads            int
	fallbackFavorites       []string
	fallbackMarks           []NotInterestedMark
	fallbackProgress        []WatchProgressRow
	ebookProgress           []WatchProgressRow
	fallbackRecentCompleted []string
	fallbackRewatches       []RewatchCount

	// missing lists canonical IDs RecommendableItemIDs reports as deleted from
	// the catalog; every other ID exists.
	missing map[string]struct{}

	// signalRows answers HasSignalRows, which records each call's
	// includeStoreTables argument.
	signalRows      bool
	signalRowsCalls []bool
}

func (r *fakeSignalRepo) HasSignalRows(_ context.Context, _ int, _ string, includeStoreTables bool) (bool, error) {
	r.signalRowsCalls = append(r.signalRowsCalls, includeStoreTables)
	return r.signalRows, nil
}

func (r *fakeSignalRepo) GetWatchedItemIDSet(context.Context, int, string) (map[string]struct{}, error) {
	r.watchedReads++
	return r.fallbackWatched, nil
}

func (r *fakeSignalRepo) GetFavoriteItemIDs(context.Context, int, string) ([]string, error) {
	return r.fallbackFavorites, nil
}

func (r *fakeSignalRepo) GetNotInterestedMarks(context.Context, int, string) ([]NotInterestedMark, error) {
	return r.fallbackMarks, nil
}

func (r *fakeSignalRepo) GetWatchProgressForUser(context.Context, int, string) ([]WatchProgressRow, error) {
	return r.fallbackProgress, nil
}

func (r *fakeSignalRepo) GetEbookReaderProgressForUser(context.Context, int, string) ([]WatchProgressRow, error) {
	return r.ebookProgress, nil
}

func (r *fakeSignalRepo) GetRecentCompletedItemIDs(_ context.Context, _ int, _ string, limit int) ([]string, error) {
	return r.fallbackRecentCompleted[:min(limit, len(r.fallbackRecentCompleted))], nil
}

func (r *fakeSignalRepo) GetRewatchCounts(context.Context, int, string) ([]RewatchCount, error) {
	return r.fallbackRewatches, nil
}

func (r *fakeSignalRepo) ResolveCanonicalItemIDs(_ context.Context, contentIDs []string) (map[string]string, error) {
	resolved := make(map[string]string, len(contentIDs))
	for _, id := range contentIDs {
		if canonical, ok := r.canonical[id]; ok {
			resolved[id] = canonical
			continue
		}
		resolved[id] = id
	}
	return resolved, nil
}

func (r *fakeSignalRepo) ResolveCanonicalItemIDSet(ctx context.Context, contentIDs []string) (map[string]struct{}, error) {
	resolved, err := r.ResolveCanonicalItemIDs(ctx, contentIDs)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(resolved))
	for _, id := range resolved {
		set[id] = struct{}{}
	}
	return set, nil
}

func (r *fakeSignalRepo) RecommendableItemIDs(_ context.Context, itemIDs []string) (map[string]struct{}, error) {
	existing := make(map[string]struct{}, len(itemIDs))
	for _, id := range itemIDs {
		if _, gone := r.missing[id]; !gone {
			existing[id] = struct{}{}
		}
	}
	return existing, nil
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

	progress  []userstore.WatchProgress
	profile   *userstore.Profile
	favorites []userstore.Favorite
	watchlist []userstore.WatchlistEntry
	marks     []userstore.NotInterestedEntry

	// progressPages counts ListProgressPage calls by status.
	progressPages map[string]int
}

// ListNotInterestedPage returns the profile's marks in one page, as stored.
func (s *fakeSignalStore) ListNotInterestedPage(_ context.Context, profileID string, after *userstore.ListKey, limit int) ([]userstore.NotInterestedEntry, error) {
	if after != nil {
		return nil, nil
	}
	var rows []userstore.NotInterestedEntry
	for _, m := range s.marks {
		if m.ProfileID == profileID && len(rows) < limit {
			rows = append(rows, m)
		}
	}
	return rows, nil
}

func (s *fakeSignalStore) ListFavorites(_ context.Context, profileID string, limit, offset int) ([]userstore.Favorite, error) {
	var rows []userstore.Favorite
	for _, f := range s.favorites {
		if f.ProfileID == profileID {
			rows = append(rows, f)
		}
	}
	return pageOf(rows, limit, offset), nil
}

// ListFavoritesPage pages the profile's favorites in stored order, keyed by
// media item ID; the fake keeps no timestamps to order by.
func (s *fakeSignalStore) ListFavoritesPage(_ context.Context, profileID string, after *userstore.ListKey, limit int) ([]userstore.Favorite, error) {
	var rows []userstore.Favorite
	started := after == nil
	for _, f := range s.favorites {
		if f.ProfileID != profileID {
			continue
		}
		if !started {
			started = f.MediaItemID == after.MediaItemID
			continue
		}
		rows = append(rows, f)
		if len(rows) == limit {
			break
		}
	}
	return rows, nil
}

func (s *fakeSignalStore) ListWatchlist(_ context.Context, profileID string, limit, offset int) ([]userstore.WatchlistEntry, error) {
	var rows []userstore.WatchlistEntry
	for _, w := range s.watchlist {
		if w.ProfileID == profileID {
			rows = append(rows, w)
		}
	}
	return pageOf(rows, limit, offset), nil
}

func pageOf[T any](rows []T, limit, offset int) []T {
	if offset >= len(rows) {
		return nil
	}
	return rows[offset:min(offset+limit, len(rows))]
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

func (s *fakeSignalStore) ListProgressPage(ctx context.Context, profileID, status string, after *userstore.ProgressKey, limit int) ([]userstore.WatchProgress, error) {
	if s.progressPages == nil {
		s.progressPages = map[string]int{}
	}
	s.progressPages[status]++
	rows, err := s.ListProgress(ctx, profileID, status, len(s.progress), 0)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(a, b userstore.WatchProgress) int {
		left, right := parseSignalTime(a.UpdatedAt, time.Time{}), parseSignalTime(b.UpdatedAt, time.Time{})
		if !left.Equal(right) {
			if left.After(right) {
				return -1
			}
			return 1
		}
		if a.MediaItemID > b.MediaItemID {
			return -1
		}
		if a.MediaItemID < b.MediaItemID {
			return 1
		}
		return 0
	})
	result := make([]userstore.WatchProgress, 0, limit)
	for _, row := range rows {
		if after != nil {
			updated, boundary := parseSignalTime(row.UpdatedAt, time.Time{}), parseSignalTime(after.UpdatedAt, time.Time{})
			if updated.After(boundary) || (updated.Equal(boundary) && row.MediaItemID >= after.MediaItemID) {
				continue
			}
		}
		result = append(result, row)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (s *fakeSignalStore) GetProfile(context.Context, string) (*userstore.Profile, error) {
	return s.profile, nil
}

func TestProfileAccessFilterUsesStoredStableProfileRestrictions(t *testing.T) {
	store := &fakeSignalStore{profile: &userstore.Profile{
		ID:                         "p1",
		MaxContentRating:           "PG-13",
		MaxAdvisoryAge:             10,
		LibraryRestrictionsEnabled: true,
		AllowedLibraryIDs:          []int{2, 5},
	}}
	engine := &Engine{storeProvider: fakeSignalProvider{store: store}}

	filter, err := engine.profileAccessFilter(context.Background(), 7, "p1")
	if err != nil {
		t.Fatalf("profileAccessFilter: %v", err)
	}
	if filter.UserID != 7 || filter.ProfileID != "p1" {
		t.Fatalf("unexpected filter identity: %#v", filter)
	}
	if filter.MaxContentRating != "PG-13" {
		t.Fatalf("MaxContentRating = %q, want PG-13", filter.MaxContentRating)
	}
	if filter.MaxAdvisoryAge != 10 {
		t.Fatalf("MaxAdvisoryAge = %d, want 10", filter.MaxAdvisoryAge)
	}
	if !slices.Equal(filter.AllowedLibraryIDs, []int{2, 5}) {
		t.Fatalf("AllowedLibraryIDs = %#v, want [2 5]", filter.AllowedLibraryIDs)
	}
	if filter.DisabledLibraryIDs != nil {
		t.Fatalf("DisabledLibraryIDs should remain request-time only, got %#v", filter.DisabledLibraryIDs)
	}
}

func TestSignalReaderRecentCompletedResolvesTiesAcrossPages(t *testing.T) {
	const updated = "2026-08-10T12:00:00Z"
	progress := make([]userstore.WatchProgress, 0, signalPageSize+1)
	canonical := make(map[string]string, signalPageSize+1)
	for i := 0; i < signalPageSize; i++ {
		id := fmt.Sprintf("z-episode-%04d", i)
		progress = append(progress, userstore.WatchProgress{ProfileID: "p1", MediaItemID: id, Completed: true, UpdatedAt: updated})
		canonical[id] = "series-z"
	}
	// This leaf sorts after the entire first page, but its series wins the canonical tie.
	progress = append(progress, userstore.WatchProgress{ProfileID: "p1", MediaItemID: "a-episode", Completed: true, UpdatedAt: updated})
	canonical["a-episode"] = "series-a"
	reader := NewSignalReader(&fakeSignalRepo{canonical: canonical}, fakeSignalProvider{store: &fakeSignalStore{progress: progress}}).WithUserStoreOutsidePostgres(true)
	got, err := reader.RecentCompletedItemIDs(context.Background(), 7, "p1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"series-a"}; !slices.Equal(got, want) {
		t.Fatalf("recent completed = %v, want %v", got, want)
	}
}

// On the Postgres user store the watched set is one repo query and the
// store's progress is never paged; a store outside Postgres is walked.
func TestWatchedSetReadsTheRepoUnlessTheStoreIsOutsidePostgres(t *testing.T) {
	for _, outside := range []bool{false, true} {
		store := &fakeSignalStore{progress: []userstore.WatchProgress{{ProfileID: "p1", MediaItemID: "watched", Completed: true}}}
		repo := &fakeSignalRepo{fallbackWatched: map[string]struct{}{"watched": {}}}
		set, err := NewSignalReader(repo, fakeSignalProvider{store: store}).WithUserStoreOutsidePostgres(outside).WatchedItemIDSet(t.Context(), 7, "p1")
		if err != nil || !maps.Equal(set, map[string]struct{}{"watched": {}}) {
			t.Fatalf("outside postgres %v: watched set = %v, %v", outside, set, err)
		}
		wantRepoReads, wantWalks := 1, 0
		if outside {
			wantRepoReads, wantWalks = 0, 1
		}
		if repo.watchedReads != wantRepoReads || store.progressPages["all"] != wantWalks {
			t.Fatalf("outside postgres %v: repo queries = %d, progress pages walked = %d; want %d and %d",
				outside, repo.watchedReads, store.progressPages["all"], wantRepoReads, wantWalks)
		}
	}
}

// On the Postgres user store a profile's progress rows are one repo query and
// the store's progress is never paged; a store outside Postgres is walked.
// Ebook reading progress is added either way. The query's times are cut to
// the store's whole seconds.
func TestWatchProgressReadsTheRepoUnlessTheStoreIsOutsidePostgres(t *testing.T) {
	second := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, outside := range []bool{false, true} {
		store := &fakeSignalStore{progress: []userstore.WatchProgress{{ProfileID: "p1", MediaItemID: "in-store", Completed: true, UpdatedAt: second.Format(time.RFC3339)}}}
		repo := &fakeSignalRepo{
			fallbackProgress: []WatchProgressRow{{MediaItemID: "in-postgres", Completed: true, UpdatedAt: second.Add(700 * time.Millisecond)}},
			ebookProgress:    []WatchProgressRow{{MediaItemID: "ebook", PositionSeconds: 1, DurationSeconds: 1, Completed: true}},
		}
		rows, err := NewSignalReader(repo, fakeSignalProvider{store: store}).WithUserStoreOutsidePostgres(outside).WatchProgressForUser(t.Context(), 7, "p1")
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, row := range rows {
			ids = append(ids, row.MediaItemID)
		}
		want, wantWalks := []string{"in-postgres", "ebook"}, 0
		if outside {
			want, wantWalks = []string{"in-store", "ebook"}, 1
		}
		if !slices.Equal(ids, want) || store.progressPages["all"] != wantWalks {
			t.Fatalf("outside postgres %v: rows = %v, progress pages walked = %d; want %v and %d",
				outside, ids, store.progressPages["all"], want, wantWalks)
		}
		if !rows[0].UpdatedAt.Equal(second) {
			t.Fatalf("outside postgres %v: %s updated at %v, want %v", outside, ids[0], rows[0].UpdatedAt, second)
		}
	}
}

// On the Postgres user store the recent completions Because You Watched
// anchors on are one repo query and the store's completed progress is never
// paged: library sections read fifty deep on every load. A store outside
// Postgres is walked.
func TestRecentCompletedReadsTheRepoUnlessTheStoreIsOutsidePostgres(t *testing.T) {
	for _, outside := range []bool{false, true} {
		store := &fakeSignalStore{progress: []userstore.WatchProgress{{ProfileID: "p1", MediaItemID: "in-store", Completed: true, UpdatedAt: "2026-10-01T12:00:00Z"}}}
		repo := &fakeSignalRepo{fallbackRecentCompleted: []string{"in-postgres"}}
		ids, err := NewSignalReader(repo, fakeSignalProvider{store: store}).WithUserStoreOutsidePostgres(outside).RecentCompletedItemIDs(t.Context(), 7, "p1", anchorCandidateLimit)
		if err != nil {
			t.Fatal(err)
		}
		want, wantWalks := []string{"in-postgres"}, 0
		if outside {
			want, wantWalks = []string{"in-store"}, 1
		}
		if !slices.Equal(ids, want) || store.progressPages["completed"] != wantWalks {
			t.Fatalf("outside postgres %v: recent completed = %v, progress pages walked = %d; want %v and %d",
				outside, ids, store.progressPages["completed"], want, wantWalks)
		}
	}
}

// fakeRatedItems lists a profile's ratings from memory.
type fakeRatedItems []string

func (f fakeRatedItems) ListAll(context.Context, int, string) ([]catalog.UserRating, error) {
	out := make([]catalog.UserRating, len(f))
	for i, id := range f {
		out[i] = catalog.UserRating{MediaItemID: id, Score: 8}
	}
	return out, nil
}
