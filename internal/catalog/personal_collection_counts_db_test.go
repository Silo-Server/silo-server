package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
)

// TestCountVisiblePersonalCollectionMembersDB pins the personal collection
// item_count (#1552) to the members the viewer can see: rows for missing items,
// manga chapters, sub-items, and other accounts never count, and library and
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
	chapter := fmt.Sprintf("collection-count-chapter-%d", suffix)
	for _, seed := range []struct {
		id      string
		age     int
		library int
	}{{shown, 6, shownLib}, {mature, 16, shownLib}, {hidden, 6, hiddenLib}, {chapter, 6, shownLib}} {
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, advisory_age) VALUES ($1, 'movie', $1, $2)`, seed.id, seed.age)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, seed.id, seed.library)
	}
	batchEquivExec(t, pool, `UPDATE media_items SET type = 'ebook' WHERE content_id = $1`, chapter)
	batchEquivExec(t, pool, `INSERT INTO manga_chapters (chapter_content_id, series_content_id) VALUES ($1, $2)`, chapter, shown)
	manual := fmt.Sprintf("collection-count-manual-%d", suffix)
	empty := fmt.Sprintf("collection-count-empty-%d", suffix)
	for _, member := range []string{shown, mature, hidden, missing, chapter} {
		batchEquivExec(t, pool, `INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id) VALUES ($1, $2, $3)`, account, manual, member)
	}
	batchEquivExec(t, pool, `INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id, sub_item_id) VALUES ($1, $2, $3, 'chapter-1')`, account, manual, shown)
	batchEquivExec(t, pool, `INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id) VALUES ($1, $2, $3)`, otherAccount, manual, shown)
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM users WHERE id = ANY($1)`, []int{account, otherAccount})
		batchEquivExec(t, pool, `DELETE FROM user_collection_revisions WHERE user_id = ANY($1)`, []int{account, otherAccount})
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{shown, mature, hidden, chapter})
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

// The list must not repeat the same catalog count for each smart collection,
// and batching distinct definitions must preserve their arguments and limits.
func TestCountPersonalCollectionsBatchedDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := t.Context()
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("batch-count-%d", time.Now().UnixNano())).Scan(&library); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, year := range []int{2000, 2010, 2020} {
		id := fmt.Sprintf("batch-count-%d-%d", library, i)
		ids = append(ids, id)
		batchEquivExec(t, pool, `INSERT INTO media_items(content_id,type,title,year) VALUES($1,'movie',$1,$2)`, id, year)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library)
	}
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id=ANY($1)`, ids)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id=$1`, library)
	})
	viewer := AccessFilter{AllowedLibraryIDs: []int{library}}
	duplicate := PersonalCollectionDefinition{CollectionType: "smart", QueryDefinition: `{"media_scope":"movie","groups":[]}`}
	var duplicates []PersonalCollectionDefinition
	for i := range 50 {
		c := duplicate
		c.ID = fmt.Sprintf("duplicate-%d", i)
		duplicates = append(duplicates, c)
	}
	before := pool.Stat().AcquireCount()
	counts, err := CountPersonalCollections(ctx, pool, 0, duplicates, viewer)
	queries := pool.Stat().AcquireCount() - before
	if err != nil || len(counts) != len(duplicates) || queries != 1 {
		t.Fatalf("duplicate counts: %d results, %d queries, error %v", len(counts), queries, err)
	}
	for _, c := range duplicates {
		if counts[c.ID] != 3 {
			t.Errorf("%s = %d, want 3", c.ID, counts[c.ID])
		}
	}

	var distinct []PersonalCollectionDefinition
	wants := make(map[string]int)
	for i := range 40 {
		year := 1990 + i
		c := PersonalCollectionDefinition{ID: fmt.Sprintf("year-%d", year), CollectionType: "smart", QueryDefinition: fmt.Sprintf(`{"media_scope":"movie","groups":[{"rules":[{"field":"year","op":"gte","value":%d}]}],"limit":2}`, year)}
		distinct = append(distinct, c)
		for _, itemYear := range []int{2000, 2010, 2020} {
			if itemYear >= year {
				wants[c.ID]++
			}
		}
		wants[c.ID] = min(wants[c.ID], 2)
	}
	before = pool.Stat().AcquireCount()
	counts, err = CountPersonalCollections(ctx, pool, 0, distinct, viewer)
	queries = pool.Stat().AcquireCount() - before
	if err != nil || len(counts) != len(distinct) || queries > personalCollectionCountStatements {
		t.Fatalf("distinct counts: %d results, %d queries, error %v", len(counts), queries, err)
	}
	for id, want := range wants {
		if counts[id] != want {
			t.Errorf("%s = %d, want %d", id, counts[id], want)
		}
	}

	// The reuse is per request, and one invalid definition cannot hide valid
	// counts returned alongside it.
	batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id=$1`, ids[0])
	duplicates = append(duplicates, PersonalCollectionDefinition{ID: "invalid", CollectionType: "smart", QueryDefinition: "{"})
	counts, err = CountPersonalCollections(ctx, pool, 0, duplicates, viewer)
	if err == nil || len(counts) != 50 || counts[duplicates[0].ID] != 2 {
		t.Fatalf("next request: %d results, first count %d, error %v", len(counts), counts[duplicates[0].ID], err)
	}
}
