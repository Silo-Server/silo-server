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
		INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', 'Dual Library Item')`, item)
	batchEquivExec(t, pool, `
		INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2), ($1, $3)`,
		item, enabledLib, disabledLib)
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = $1`, item)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{enabledLib, disabledLib})
	})

	itemRepo := NewItemRepository(pool)
	libraryRepo := NewLibraryItemRepository(pool)

	scopes := []struct {
		name   string
		filter AccessFilter
	}{
		{"disabled list", AccessFilter{DisabledLibraryIDs: []int{disabledLib}}},
		{"allowlist excluding disabled", AccessFilter{AllowedLibraryIDs: []int{enabledLib}, DisabledLibraryIDs: []int{disabledLib}}},
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
}
