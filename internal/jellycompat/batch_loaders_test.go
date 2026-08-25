package jellycompat

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestApplyCompatLibraryAccessUsesItemLevelDisabledExclusion(t *testing.T) {
	access := catalog.AccessFilter{DisabledLibraryIDs: []int{9}}
	var conditions []string
	var args []any
	argIdx := 2
	if !applyCompatLibraryAccess(&access, nil, "mi.content_id", &conditions, &args, &argIdx) {
		t.Fatal("disabled-only scope must remain queryable")
	}

	sql := strings.Join(conditions, " AND ")
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id)") {
		t.Fatalf("disabled-only scope must require positive membership, got %s", sql)
	}
	if !strings.Contains(sql, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($2))") {
		t.Fatalf("dual-membership items must be rejected with NOT EXISTS, got %s", sql)
	}
	if len(args) != 1 || argIdx != 3 {
		t.Fatalf("args = %v, argIdx = %d; want one disabled-list arg and 3", args, argIdx)
	}
}

func TestApplyCompatLibraryAccessNarrowsRequestedLibraryBeforeDeny(t *testing.T) {
	libraryID := 7
	access := catalog.AccessFilter{
		AllowedLibraryIDs:  []int{7, 8},
		DisabledLibraryIDs: []int{9},
	}
	var conditions []string
	var args []any
	argIdx := 1
	if !applyCompatLibraryAccess(&access, &libraryID, "si.content_id", &conditions, &args, &argIdx) {
		t.Fatal("requested allowed library must remain queryable")
	}

	if !reflect.DeepEqual(access.AllowedLibraryIDs, []int{7}) {
		t.Fatalf("allowed libraries = %v, want requested library only", access.AllowedLibraryIDs)
	}
	sql := strings.Join(conditions, " AND ")
	if !strings.Contains(sql, "mil.media_folder_id = ANY($1)") ||
		!strings.Contains(sql, "NOT EXISTS") ||
		!strings.Contains(sql, "mil.media_folder_id = ANY($2)") {
		t.Fatalf("requested library and global deny must be independent predicates, got %s", sql)
	}
}

type stubLibraryMembershipChecker struct {
	membership map[string]bool
	err        error
}

func (s stubLibraryMembershipChecker) GetItemsInLibrary(context.Context, []string, int) (map[string]bool, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.membership, nil
}

// countingItemRepo is an in-memory itemRepoForBatchLoader fake. It records
// invocation counts so tests can assert that the compatPool() == nil fallback
// uses the batched GetByIDsWithAccess instead of a per-item EnsureAccessible
// loop (audit 2026-05-01 §3.3).
type countingItemRepo struct {
	itemsByID                map[string]*models.MediaItem
	getByIDsCalls            int
	getByIDsWithAccessCalls  int
	getItemsInLibraryCalls   int
	libraryMembership        map[int]map[string]bool
	getByIDsWithAccessAccess catalog.AccessFilter
	getByIDsWithAccessIDs    []string
}

func (r *countingItemRepo) GetByIDs(_ context.Context, contentIDs []string) ([]*models.MediaItem, error) {
	r.getByIDsCalls++
	out := make([]*models.MediaItem, 0, len(contentIDs))
	for _, id := range contentIDs {
		if it, ok := r.itemsByID[id]; ok {
			out = append(out, it)
		}
	}
	return out, nil
}

func (r *countingItemRepo) GetByIDsWithAccess(_ context.Context, contentIDs []string, access catalog.AccessFilter) ([]*models.MediaItem, error) {
	r.getByIDsWithAccessCalls++
	r.getByIDsWithAccessAccess = access
	r.getByIDsWithAccessIDs = append([]string(nil), contentIDs...)
	out := make([]*models.MediaItem, 0, len(contentIDs))
	for _, id := range contentIDs {
		if it, ok := r.itemsByID[id]; ok {
			out = append(out, it)
		}
	}
	return out, nil
}

func (r *countingItemRepo) GetItemsInLibrary(_ context.Context, contentIDs []string, libraryID int) (map[string]bool, error) {
	r.getItemsInLibraryCalls++
	result := make(map[string]bool, len(contentIDs))
	allowed, ok := r.libraryMembership[libraryID]
	if !ok {
		return result, nil
	}
	for _, id := range contentIDs {
		if allowed[id] {
			result[id] = true
		}
	}
	return result, nil
}

// countingEpisodeRepo is an in-memory episodeRepoForBatchLoader fake.
type countingEpisodeRepo struct {
	episodesByID  map[string]*models.Episode
	hasFilesByID  map[string]bool
	getByIDsCalls int
}

func (r *countingEpisodeRepo) GetByIDs(_ context.Context, contentIDs []string) ([]*models.Episode, error) {
	r.getByIDsCalls++
	out := make([]*models.Episode, 0, len(contentIDs))
	for _, id := range contentIDs {
		if ep, ok := r.episodesByID[id]; ok {
			out = append(out, ep)
		}
	}
	return out, nil
}

func (r *countingEpisodeRepo) HasFilesByIDs(_ context.Context, contentIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(contentIDs))
	for _, id := range contentIDs {
		if r.hasFilesByID[id] {
			out[id] = true
		}
	}
	return out, nil
}

func (r *countingEpisodeRepo) ListBySeason(context.Context, string, int) ([]*models.Episode, error) {
	return nil, errors.New("ListBySeason not used in fallback test")
}

func (r *countingEpisodeRepo) ListBySeries(context.Context, string) ([]*models.Episode, error) {
	return nil, errors.New("ListBySeries not used in fallback test")
}

func (r *countingEpisodeRepo) ListAdjacentInSeries(context.Context, string, int, int) ([]*models.Episode, error) {
	return nil, errors.New("ListAdjacentInSeries not used in fallback test")
}

func TestFilterContentIDsForLibrary_AppliesMembershipAndPreservesOrder(t *testing.T) {
	libraryID := 7

	filtered, err := filterContentIDsForLibrary(
		context.Background(),
		stubLibraryMembershipChecker{membership: map[string]bool{"episode-2": true, "movie-1": true}},
		[]string{"movie-1", "episode-2", "movie-1", "", "episode-3"},
		&libraryID,
	)
	if err != nil {
		t.Fatalf("filterContentIDsForLibrary returned error: %v", err)
	}

	want := []string{"movie-1", "episode-2"}
	if !reflect.DeepEqual(filtered, want) {
		t.Fatalf("filterContentIDsForLibrary = %v, want %v", filtered, want)
	}
}

func TestFilterContentIDsForLibrary_PropagatesMembershipErrors(t *testing.T) {
	libraryID := 7
	wantErr := errors.New("boom")

	_, err := filterContentIDsForLibrary(
		context.Background(),
		stubLibraryMembershipChecker{err: wantErr},
		[]string{"movie-1"},
		&libraryID,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("filterContentIDsForLibrary error = %v, want %v", err, wantErr)
	}
}

// TestFetchCompatItemsByContentIDsFallback_UsesBatchedAccessQuery pins the
// audit fix: when compatPool() returns nil (e.g. browseRepo is unset in a
// DB-less test config), the fallback must push library/rating gating into
// itemRepo.GetByIDsWithAccess instead of fetching items then looping
// EnsureAccessible per item (audit 2026-05-01 §3.3, Pattern C).
func TestFetchCompatItemsByContentIDsFallback_UsesBatchedAccessQuery(t *testing.T) {
	repo := &countingItemRepo{
		itemsByID: map[string]*models.MediaItem{
			"a": {ContentID: "a", Type: "movie", Title: "A"},
			"b": {ContentID: "b", Type: "movie", Title: "B"},
		},
	}
	h := &ItemsHandler{
		itemRepo: repo,
		// No accessFilter resolver: resolveAccessFilter returns a zero filter.
	}

	got, err := h.fetchCompatItemsByContentIDsFallback(
		context.Background(),
		&Session{},
		[]string{"a", "b"},
		nil,
	)
	if err != nil {
		t.Fatalf("fetchCompatItemsByContentIDsFallback returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 items in result; got %d (%v)", len(got), got)
	}
	if repo.getByIDsWithAccessCalls != 1 {
		t.Errorf("expected exactly 1 batched GetByIDsWithAccess call; got %d", repo.getByIDsWithAccessCalls)
	}
	if repo.getByIDsCalls != 0 {
		t.Errorf("expected zero plain GetByIDs calls in the fallback; got %d", repo.getByIDsCalls)
	}
	if !reflect.DeepEqual(repo.getByIDsWithAccessIDs, []string{"a", "b"}) {
		t.Errorf("expected GetByIDsWithAccess to receive both content IDs; got %v", repo.getByIDsWithAccessIDs)
	}
}

// TestFetchCompatItemsByContentIDsFallback_NarrowsAccessToLibraryArg verifies
// that the libraryID argument is pushed into access.AllowedLibraryIDs so
// GetByIDsWithAccess can gate it in a single SQL statement instead of pre-
// filtering with GetItemsInLibrary then re-checking via EnsureAccessible.
func TestFetchCompatItemsByContentIDsFallback_NarrowsAccessToLibraryArg(t *testing.T) {
	repo := &countingItemRepo{
		itemsByID: map[string]*models.MediaItem{
			"a": {ContentID: "a", Type: "movie", Title: "A"},
		},
	}
	h := &ItemsHandler{itemRepo: repo}
	libraryID := 7

	if _, err := h.fetchCompatItemsByContentIDsFallback(
		context.Background(),
		&Session{},
		[]string{"a"},
		&libraryID,
	); err != nil {
		t.Fatalf("fetchCompatItemsByContentIDsFallback returned error: %v", err)
	}
	if repo.getByIDsWithAccessCalls != 1 {
		t.Fatalf("expected exactly 1 GetByIDsWithAccess call; got %d", repo.getByIDsWithAccessCalls)
	}
	if !reflect.DeepEqual(repo.getByIDsWithAccessAccess.AllowedLibraryIDs, []int{libraryID}) {
		t.Errorf("expected libraryID to be pushed into access.AllowedLibraryIDs; got %v", repo.getByIDsWithAccessAccess.AllowedLibraryIDs)
	}
}

// TestFetchCompatItemsByContentIDsFallback_LibraryOutsideAllowlistShortCircuits
// confirms that when the caller-supplied libraryID is not in the access
// allowlist, the fallback returns an empty result without hitting the
// repository.
func TestFetchCompatItemsByContentIDsFallback_LibraryOutsideAllowlistShortCircuits(t *testing.T) {
	repo := &countingItemRepo{itemsByID: map[string]*models.MediaItem{}}
	h := &ItemsHandler{
		itemRepo: repo,
		accessFilter: func(context.Context, int, string) catalog.AccessFilter {
			return catalog.AccessFilter{AllowedLibraryIDs: []int{1, 2}}
		},
	}
	disallowed := 99

	got, err := h.fetchCompatItemsByContentIDsFallback(
		context.Background(),
		&Session{},
		[]string{"a"},
		&disallowed,
	)
	if err != nil {
		t.Fatalf("fetchCompatItemsByContentIDsFallback returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result when libraryID is outside the access allowlist; got %v", got)
	}
	if repo.getByIDsWithAccessCalls != 0 {
		t.Errorf("expected zero GetByIDsWithAccess calls; got %d", repo.getByIDsWithAccessCalls)
	}
}

// TestFetchCompatItemsByContentIDsFallback_BatchesPresign pins Fix #2b for the
// batch-loader path: after collecting the page it must presign in one batch per
// populated image type, not one singular call per item. The resolver batch
// count stays bounded by image type as the item count grows.
func TestFetchCompatItemsByContentIDsFallback_BatchesPresign(t *testing.T) {
	resolver := &countingCompatImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	repo := &countingItemRepo{
		itemsByID: map[string]*models.MediaItem{
			"a": {ContentID: "a", Type: "movie", Title: "A", PosterPath: "plug://poster-a", BackdropPath: "plug://backdrop-a"},
			"b": {ContentID: "b", Type: "movie", Title: "B", PosterPath: "plug://poster-b", BackdropPath: "plug://backdrop-b"},
			"c": {ContentID: "c", Type: "movie", Title: "C", PosterPath: "plug://poster-c", BackdropPath: "plug://backdrop-c"},
		},
	}
	h := &ItemsHandler{itemRepo: repo, detailSvc: detailSvc}

	got, err := h.fetchCompatItemsByContentIDsFallback(
		context.Background(),
		&Session{},
		[]string{"a", "b", "c"},
		nil,
	)
	if err != nil {
		t.Fatalf("fetchCompatItemsByContentIDsFallback returned error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 items in result; got %d", len(got))
	}
	if resolver.singleCalls != 0 {
		t.Errorf("single image resolver calls = %d, want 0 (batched path only)", resolver.singleCalls)
	}
	// 3 items × poster+backdrop → only 2 batched resolver calls, not 2×3.
	if resolver.batchCalls != 2 {
		t.Errorf("batch image resolver calls = %d, want 2 (poster+backdrop, independent of item count)", resolver.batchCalls)
	}
	if got["a"].PosterURL != "batch:card:plug://poster-a" {
		t.Errorf("item a PosterURL = %q", got["a"].PosterURL)
	}
}

// TestFetchCompatEpisodeTargetsByContentIDsFallback_UsesBatchedSeriesAccess
// pins the episode-fallback fix: series-level access checks must be batched
// through itemRepo.GetByIDsWithAccess instead of iterating EnsureAccessible
// per series (audit 2026-05-01 §3.3, Pattern C).
func TestFetchCompatEpisodeTargetsByContentIDsFallback_UsesBatchedSeriesAccess(t *testing.T) {
	itemRepo := &countingItemRepo{
		itemsByID: map[string]*models.MediaItem{
			"series-1": {ContentID: "series-1", Type: "series", Title: "Show"},
		},
	}
	episodeRepo := &countingEpisodeRepo{
		episodesByID: map[string]*models.Episode{
			"ep-1": {ContentID: "ep-1", SeriesID: "series-1", Title: "Pilot"},
			"ep-2": {ContentID: "ep-2", SeriesID: "series-1", Title: "Two"},
		},
	}
	h := &ItemsHandler{
		itemRepo:    itemRepo,
		episodeRepo: episodeRepo,
	}

	got, err := h.fetchCompatEpisodeTargetsByContentIDsFallback(
		context.Background(),
		&Session{},
		[]string{"ep-1", "ep-2"},
		nil,
	)
	if err != nil {
		t.Fatalf("fetchCompatEpisodeTargetsByContentIDsFallback returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 episodes in result; got %d (%v)", len(got), got)
	}
	if itemRepo.getByIDsWithAccessCalls != 1 {
		t.Errorf("expected exactly 1 batched GetByIDsWithAccess for series access; got %d", itemRepo.getByIDsWithAccessCalls)
	}
	if itemRepo.getByIDsCalls != 0 {
		t.Errorf("expected zero plain GetByIDs calls in the episode fallback; got %d", itemRepo.getByIDsCalls)
	}
}

// Overlay callers already receive duration-enriched list items from the
// content service, so fetching episode metadata must not repeat the duration
// query. Callers that render target.Item opt into the enriched wrapper.
func TestFetchCompatEpisodeTargetsByContentIDs_ResolvesDurationOnlyWhenRequested(t *testing.T) {
	itemRepo := &countingItemRepo{
		itemsByID: map[string]*models.MediaItem{
			"series-1": {ContentID: "series-1", Type: "series", Title: "Show"},
		},
	}
	episodeRepo := &countingEpisodeRepo{
		episodesByID: map[string]*models.Episode{
			"ep-1": {ContentID: "ep-1", SeriesID: "series-1", Title: "Pilot"},
		},
	}
	durations := &countingProbedDurationSource{episodes: map[string]int{"ep-1": 1500}}
	h := &ItemsHandler{
		itemRepo:    itemRepo,
		episodeRepo: episodeRepo,
		durationSrc: durations,
	}

	metadataOnly, err := h.fetchCompatEpisodeTargetsByContentIDs(
		context.Background(),
		&Session{},
		[]string{"ep-1"},
		nil,
	)
	if err != nil {
		t.Fatalf("fetch metadata-only episode targets: %v", err)
	}
	if durations.episodeCalls != 0 {
		t.Fatalf("metadata-only duration calls = %d, want 0", durations.episodeCalls)
	}
	if got := metadataOnly["ep-1"].Item.DurationSeconds; got != 0 {
		t.Fatalf("metadata-only duration = %d, want 0", got)
	}

	withDurations, err := h.fetchCompatEpisodeTargetsByContentIDsWithDurations(
		context.Background(),
		&Session{},
		[]string{"ep-1"},
		nil,
	)
	if err != nil {
		t.Fatalf("fetch duration-enriched episode targets: %v", err)
	}
	if durations.episodeCalls != 1 {
		t.Fatalf("duration-enriched calls = %d, want 1", durations.episodeCalls)
	}
	if got := withDurations["ep-1"].Item.DurationSeconds; got != 1500 {
		t.Fatalf("duration-enriched duration = %d, want 1500", got)
	}
}
