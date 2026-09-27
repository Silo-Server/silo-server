package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
)

// TestCountVisiblePersonalCollectionMembersDB pins the personal collection
// item_count (#1552) to the members the viewer's items page returns: rows for
// missing items, sub-items, and other accounts never count, and library and
// maturity limits apply as they do in GetByIDsWithAccess.
func TestCountVisiblePersonalCollectionMembersDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var account, otherAccount, shownLib, hiddenLib int
	for i, target := range []*int{&account, &otherAccount} {
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("collection-count-%d-%d", suffix, i)).Scan(target); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	for i, target := range []*int{&shownLib, &hiddenLib} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
			fmt.Sprintf("collection-count-%d-%d", suffix, i)).Scan(target); err != nil {
			t.Fatalf("seed folder: %v", err)
		}
	}
	shown := fmt.Sprintf("collection-count-shown-%d", suffix)
	mature := fmt.Sprintf("collection-count-mature-%d", suffix)
	hidden := fmt.Sprintf("collection-count-hidden-%d", suffix)
	missing := fmt.Sprintf("collection-count-missing-%d", suffix)
	for _, seed := range []struct {
		id      string
		age     int
		library int
	}{{shown, 6, shownLib}, {mature, 16, shownLib}, {hidden, 6, hiddenLib}} {
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, advisory_age) VALUES ($1, 'movie', $1, $2)`, seed.id, seed.age)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, seed.id, seed.library)
	}
	manual := fmt.Sprintf("collection-count-manual-%d", suffix)
	empty := fmt.Sprintf("collection-count-empty-%d", suffix)
	for _, member := range []string{shown, mature, hidden, missing} {
		batchEquivExec(t, pool, `INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id) VALUES ($1, $2, $3)`, account, manual, member)
	}
	batchEquivExec(t, pool, `INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id, sub_item_id) VALUES ($1, $2, $3, 'chapter-1')`, account, manual, shown)
	batchEquivExec(t, pool, `INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id) VALUES ($1, $2, $3)`, otherAccount, manual, shown)
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM users WHERE id = ANY($1)`, []int{account, otherAccount})
		batchEquivExec(t, pool, `DELETE FROM user_collection_revisions WHERE user_id = ANY($1)`, []int{account, otherAccount})
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{shown, mature, hidden})
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{shownLib, hiddenLib})
	})

	repo := NewItemRepository(pool)
	for _, tc := range []struct {
		name   string
		filter AccessFilter
		want   int
	}{
		{"unrestricted", AccessFilter{}, 3},
		{"library allowlist", AccessFilter{AllowedLibraryIDs: []int{shownLib}}, 2},
		{"disabled library", AccessFilter{DisabledLibraryIDs: []int{hiddenLib}}, 2},
		{"advisory age limit", AccessFilter{AllowedLibraryIDs: []int{shownLib}, MaturityLimits: access.MaturityLimits{MaxAdvisoryAge: 8}}, 1},
		{"no libraries", AccessFilter{AllowedLibraryIDs: []int{}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts, err := repo.CountVisiblePersonalCollectionMembers(ctx, account, []string{manual, empty}, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if counts[manual] != tc.want {
				t.Fatalf("count = %d, want %d (%v)", counts[manual], tc.want, counts)
			}
			if _, ok := counts[empty]; ok {
				t.Fatalf("a collection without visible members was counted: %v", counts)
			}
			page, err := repo.GetByIDsWithAccess(ctx, []string{shown, mature, hidden, missing}, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) != tc.want {
				t.Fatalf("items page shows %d members, count says %d", len(page), tc.want)
			}
		})
	}
}

// TestQueryExecutorCountMatchesCursorTotalDB pins Count to the total the smart
// collection items page reports for the same definition and viewer, including
// the definition's item limit.
func TestQueryExecutorCountMatchesCursorTotalDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var lib int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("query-count-%d", suffix)).Scan(&lib); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	var ids []string
	for i, age := range []int{4, 6, 16} {
		id := fmt.Sprintf("query-count-%d-%d", suffix, i)
		ids = append(ids, id)
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, advisory_age) VALUES ($1, 'movie', $1, $2)`, id, age)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, lib)
	}
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = $1`, lib)
	})

	executor := &QueryExecutor{Pool: pool}
	limitTwo := 2
	for _, tc := range []struct {
		name   string
		def    QueryDefinition
		filter AccessFilter
		want   int
	}{
		{"all matches", QueryDefinition{LibraryIDs: []int{lib}, MediaScope: "movie"}, AccessFilter{}, 3},
		{"viewer limit", QueryDefinition{LibraryIDs: []int{lib}, MediaScope: "movie"}, AccessFilter{MaturityLimits: access.MaturityLimits{MaxAdvisoryAge: 8}}, 2},
		{"item limit", QueryDefinition{LibraryIDs: []int{lib}, MediaScope: "movie", Limit: &limitTwo}, AccessFilter{}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := ApplySmartCollectionItemLimit(tc.def.Normalize())
			got, err := executor.Count(ctx, def, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			page, err := executor.PreviewCursorPage(ctx, def, tc.filter, 1, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || page.Total != tc.want {
				t.Fatalf("Count = %d, cursor total = %d, want %d", got, page.Total, tc.want)
			}
		})
	}
}
