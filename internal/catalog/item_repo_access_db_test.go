package catalog

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestDualLibraryItemDeniedByDisabledLibraryDB is the behavioral regression
// test for review finding C3: an item linked to BOTH a non-disabled and a
// disabled library must be denied by every direct-authorization path when the
// viewer's scope disables one of those libraries. The old single-join query
// shape satisfied the disabled predicate via the non-disabled membership row
// and leaked the item.
func TestDualLibraryItemDeniedByDisabledLibraryDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()

	suffix := time.Now().UnixNano()
	item := fmt.Sprintf("dual-lib-item-%d", suffix)

	var enabledLib, disabledLib int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("dual-lib-a-%d", suffix),
	).Scan(&enabledLib); err != nil {
		t.Fatalf("seed enabled folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("dual-lib-b-%d", suffix),
	).Scan(&disabledLib); err != nil {
		t.Fatalf("seed disabled folder: %v", err)
	}
	batchEquivExec(t, pool, `
		INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'movie', 'Dual Library Item', ARRAY['Dual Library Leak'])`, item)
	batchEquivExec(t, pool, `
		INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2), ($1, $3)`,
		item, enabledLib, disabledLib)
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = $1`, item)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{enabledLib, disabledLib})
	})

	itemRepo := NewItemRepository(pool)
	libraryRepo := NewLibraryItemRepository(pool)
	browseRepo := NewBrowseRepository(pool)

	scopes := []struct {
		name   string
		filter AccessFilter
	}{
		{"disabled list", AccessFilter{DisabledLibraryIDs: []int{disabledLib}}},
		{"allowlist excluding disabled", AccessFilter{AllowedLibraryIDs: []int{enabledLib}, DisabledLibraryIDs: []int{disabledLib}}},
	}

	browseResult, err := browseRepo.Browse(ctx, BrowseFilters{
		ContentIDs:         []string{item},
		DisabledLibraryIDs: []int{disabledLib},
		Limit:              20,
	})
	if err != nil {
		t.Fatalf("Browse dual-library scope: %v", err)
	}
	if len(browseResult.Items) != 0 || browseResult.Total != 0 {
		t.Fatalf("Browse returned disabled dual-library item: %+v", browseResult)
	}

	genres, err := browseRepo.ListGenres(ctx, BrowseFilters{
		ContentIDs:         []string{item},
		DisabledLibraryIDs: []int{disabledLib},
	})
	if err != nil {
		t.Fatalf("ListGenres dual-library scope: %v", err)
	}
	if len(genres) != 0 {
		t.Fatalf("ListGenres exposed disabled dual-library metadata: %v", genres)
	}

	recent, err := browseRepo.BrowseRecentlyAddedAcrossLibraries(ctx, BrowseFilters{
		ContentIDs:         []string{item},
		DisabledLibraryIDs: []int{disabledLib},
		Sort:               "recently_added",
		Limit:              20,
	}, []int{enabledLib})
	if err != nil {
		t.Fatalf("BrowseRecentlyAddedAcrossLibraries dual-library scope: %v", err)
	}
	if len(recent.Items) != 0 {
		t.Fatalf("recently-added fanout returned disabled dual-library item: %+v", recent.Items)
	}
	for _, scope := range scopes {
		t.Run(scope.name, func(t *testing.T) {
			if err := itemRepo.EnsureAccessible(ctx, item, scope.filter); !errors.Is(err, ErrItemNotFound) {
				t.Errorf("EnsureAccessible = %v, want ErrItemNotFound", err)
			}
			ids, err := itemRepo.EnsureAccessibleIDs(ctx, []string{item}, scope.filter)
			if err != nil {
				t.Fatalf("EnsureAccessibleIDs error: %v", err)
			}
			if ids[item] {
				t.Error("EnsureAccessibleIDs allowed the dual-library item")
			}
			batch, err := itemRepo.GetByIDsWithAccess(ctx, []string{item}, scope.filter)
			if err != nil {
				t.Fatalf("GetByIDsWithAccess error: %v", err)
			}
			if len(batch) != 0 {
				t.Error("GetByIDsWithAccess returned the dual-library item")
			}
			filtered, err := libraryRepo.FilterAccessibleContentIDs(ctx, []string{item}, scope.filter.AllowedLibraryIDs, scope.filter.DisabledLibraryIDs, "")
			if err != nil {
				t.Fatalf("FilterAccessibleContentIDs error: %v", err)
			}
			if filtered[item] {
				t.Error("FilterAccessibleContentIDs allowed the dual-library item")
			}
		})
	}

	// Sanity check: with no library restriction the item stays accessible.
	if err := itemRepo.EnsureAccessible(ctx, item, AccessFilter{}); err != nil {
		t.Fatalf("EnsureAccessible without restriction = %v, want nil", err)
	}
	// And a disabled list that does not include either linked library keeps it
	// accessible through the membership EXISTS.
	if err := itemRepo.EnsureAccessible(ctx, item, AccessFilter{DisabledLibraryIDs: []int{disabledLib + 100000}}); err != nil {
		t.Fatalf("EnsureAccessible with unrelated disabled library = %v, want nil", err)
	}
	controlBrowse, err := browseRepo.Browse(ctx, BrowseFilters{
		ContentIDs:         []string{item},
		DisabledLibraryIDs: []int{disabledLib + 100000},
		Limit:              20,
	})
	if err != nil {
		t.Fatalf("Browse with unrelated disabled library: %v", err)
	}
	if len(controlBrowse.Items) != 1 || controlBrowse.Total != 1 {
		t.Fatalf("Browse with unrelated disabled library = %+v, want one visible item", controlBrowse)
	}
}
