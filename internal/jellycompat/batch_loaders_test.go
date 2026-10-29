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

func (r *countingEpisodeRepo) ListBySeriesIDs(context.Context, []string) (map[string][]*models.Episode, error) {
	return nil, errors.New("ListBySeriesIDs not used in fallback test")
}

func (r *countingEpisodeRepo) ListIDsBySeriesIDs(context.Context, []string) (map[string][]string, error) {
	return nil, nil
}

func (r *countingEpisodeRepo) ListIDsBySeasonIDs(context.Context, []string) (map[string][]string, error) {
	return nil, nil
}

func (r *countingEpisodeRepo) ListAdjacentInSeries(context.Context, string, int, int) ([]*models.Episode, error) {
	return nil, errors.New("ListAdjacentInSeries not used in fallback test")
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

	got, err := h.fetchCompatItemsByContentIDs(
		context.Background(),
		&Session{},
		[]string{"a"},
		&disallowed,
	)
	if err != nil {
		t.Fatalf("fetchCompatItemsByContentIDs returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result when libraryID is outside the access allowlist; got %v", got)
	}
	if repo.getByIDsWithAccessCalls != 0 {
		t.Errorf("expected zero GetByIDsWithAccess calls; got %d", repo.getByIDsWithAccessCalls)
	}
}
