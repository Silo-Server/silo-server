package catalog

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// An added_at sort scoped to libraries orders by each item's earliest arrival
// in those libraries, then title, then content ID, and cursor pages walk that
// order without gaps or repeats, whether one library or several are in scope.
func TestAddedAtLibrarySortPagesByArrivalDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("added-sort-%d", time.Now().UnixNano())

	var libA, libB int
	for _, lib := range []*int{&libA, &libB} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
			fmt.Sprintf("%s-%p", prefix, lib)).Scan(lib); err != nil {
			t.Fatalf("seed folder: %v", err)
		}
	}
	base := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	type seed struct {
		name    string
		seenA   int // days before base; -1 when not in library A
		seenB   int
		wantOne int // position in the library-A order; -1 when absent
	}
	seeds := []seed{
		{"e", 0, -1, 0},
		{"b", 2, -1, 1}, // ties with "d" on arrival; title orders them
		{"d", 2, -1, 2},
		{"a", 5, 1, 3}, // also in B, more recently
		{"c", 9, -1, 4},
		{"f", -1, 3, -1},
	}
	var ids []string
	for _, s := range seeds {
		id := prefix + "-" + s.name
		ids = append(ids, id)
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $2)`, id, s.name)
		if s.seenA >= 0 {
			batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, id, libA, base.AddDate(0, 0, -s.seenA))
		}
		if s.seenB >= 0 {
			batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, id, libB, base.AddDate(0, 0, -s.seenB))
		}
	}
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{libA, libB})
	})

	executor := &QueryExecutor{Pool: pool}
	walk := func(libraries []int) []string {
		t.Helper()
		def := ApplySmartCollectionItemLimit(QueryDefinition{
			MediaScope: "movie",
			LibraryIDs: libraries,
			Sort:       QuerySort{Field: "added_at", Order: "desc"},
		}.Normalize())
		var got []string
		var after *QueryCursor
		for range len(seeds) + 1 {
			page, err := executor.PreviewCursorPage(ctx, def, AccessFilter{}, 2, after, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range page.Items {
				got = append(got, item.ContentID)
			}
			if !page.HasMore {
				return got
			}
			after = page.Next
		}
		t.Fatalf("cursor did not finish: %v", got)
		return nil
	}

	wantOne := make([]string, 5)
	for i, s := range seeds {
		if s.wantOne >= 0 {
			wantOne[s.wantOne] = ids[i]
		}
	}
	if got := walk([]int{libA}); !slices.Equal(got, wantOne) {
		t.Fatalf("one library: got %v, want %v", got, wantOne)
	}
	// Across both libraries "a" keeps its earlier arrival in A, so "f" (B
	// only) sorts ahead of it.
	wantBoth := []string{prefix + "-e", prefix + "-b", prefix + "-d", prefix + "-f", prefix + "-a", prefix + "-c"}
	if got := walk([]int{libA, libB}); !slices.Equal(got, wantBoth) {
		t.Fatalf("two libraries: got %v, want %v", got, wantBoth)
	}
}
