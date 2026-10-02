package catalog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type libraryCASFixture struct {
	pool         *pgxpool.Pool
	repo         *LibraryCollectionRepository
	groups       *LibraryCollectionGroupRepository
	libraryID    int
	collectionID string
	groupID      string
}

func newLibraryCASFixture(t *testing.T) libraryCASFixture {
	t.Helper()
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("library-cas-%d", time.Now().UnixNano())).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, libraryID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_order_revisions WHERE library_id=$1`, libraryID)
	})
	repo := NewLibraryCollectionRepository(pool)
	groups := NewLibraryCollectionGroupRepository(pool)
	c, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryID: libraryID, Title: "first", Slug: fmt.Sprintf("cas-%d", libraryID)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = repo.Delete(context.Background(), c.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, c.ID)
	})
	g, err := groups.Create(ctx, CreateLibraryCollectionGroupInput{LibraryID: libraryID, Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	return libraryCASFixture{pool, repo, groups, libraryID, c.ID, g.ID}
}
func (f libraryCASFixture) revision(t *testing.T, group bool) int64 {
	t.Helper()
	var rev int64
	var err error
	if group {
		rev, err = f.repo.CollectionOrderRevision(t.Context(), f.libraryID)
	} else {
		rev, err = f.repo.CollectionRevision(t.Context(), f.collectionID)
	}
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

func TestLibraryCollectionCASConcurrentDB(t *testing.T) {
	for _, operation := range []string{"update", "delete", "items", "collections", "move", "group-update", "group-delete", "groups"} {
		t.Run(operation, func(t *testing.T) {
			f := newLibraryCASFixture(t)
			group := operation != "update" && operation != "delete" && operation != "items"
			rev := f.revision(t, group)
			run := func() error {
				ctx := t.Context()
				switch operation {
				case "update":
					return f.repo.Update(ctx, UpdateLibraryCollectionInput{ID: f.collectionID, Title: new("changed"), ExpectedRevision: &rev})
				case "delete":
					return f.repo.DeleteIfRevision(ctx, f.collectionID, rev)
				case "items":
					return f.repo.ReorderItemsIfRevision(ctx, f.collectionID, []string{}, rev)
				case "collections":
					return f.repo.ReorderCollectionsIfRevision(ctx, f.libraryID, nil, []string{f.collectionID}, rev)
				case "move":
					return f.repo.MoveAndReorder(ctx, MoveAndReorderInput{LibraryID: f.libraryID, TargetGroupID: &f.groupID, OrderedIDs: []string{f.collectionID}, Strict: true, ExpectedRevision: &rev})
				case "group-update":
					_, err := f.groups.Update(ctx, f.groupID, UpdateLibraryCollectionGroupInput{Name: new("changed"), ExpectedRevision: &rev})
					return err
				case "group-delete":
					return f.groups.DeleteIfRevision(ctx, f.groupID, rev)
				case "groups":
					return f.groups.ReorderIfRevision(ctx, f.libraryID, []string{"ungrouped", f.groupID}, rev)
				}
				panic("unknown operation")
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for range 2 {
				wg.Go(func() { <-start; results <- run() })
			}
			close(start)
			wg.Wait()
			close(results)
			wins, refused := 0, 0
			for err := range results {
				if err == nil {
					wins++
				} else if errors.Is(err, ErrLibraryCollectionRevisionMismatch) || errors.Is(err, ErrLibraryCollectionNotFound) || errors.Is(err, ErrLibraryCollectionGroupNotFound) {
					refused++
				} else {
					t.Fatalf("unexpected mutation failure: %v", err)
				}
			}
			if wins != 1 || refused != 1 {
				t.Fatalf("wins=%d refused=%d", wins, refused)
			}
		})
	}
}

func TestLibraryCollectionCASScopesAndNoopDB(t *testing.T) {
	f := newLibraryCASFixture(t)
	other := newLibraryCASFixture(t)
	ctx := t.Context()
	initialOther := other.revision(t, true)
	before := f.revision(t, false)
	if err := f.repo.Update(ctx, UpdateLibraryCollectionInput{ID: f.collectionID, ExpectedRevision: &before}); err != nil {
		t.Fatal(err)
	}
	if after := f.revision(t, false); after <= before {
		t.Fatal("no-op did not consume collection witness")
	}
	if err := f.repo.Update(ctx, UpdateLibraryCollectionInput{ID: f.collectionID, ExpectedRevision: &before}); !errors.Is(err, ErrLibraryCollectionRevisionMismatch) {
		t.Fatalf("stale noop: %v", err)
	}
	rev := f.revision(t, true)
	if err := f.repo.ReorderCollectionsIfRevision(ctx, f.libraryID, nil, []string{other.collectionID}, rev); !errors.Is(err, collectionutil.ErrOrderedIDsMismatch) {
		t.Fatalf("foreign membership: %v", err)
	}
	if err := f.repo.MoveAndReorder(ctx, MoveAndReorderInput{LibraryID: f.libraryID, TargetGroupID: &other.groupID, OrderedIDs: []string{f.collectionID}, ExpectedRevision: &rev}); !errors.Is(err, ErrLibraryCollectionGroupNotFound) {
		t.Fatalf("foreign group: %v", err)
	}
	if got := f.revision(t, true); got != rev {
		t.Fatal("rejected writes consumed witness")
	}
	if err := f.repo.MoveAndReorder(ctx, MoveAndReorderInput{LibraryID: f.libraryID, TargetGroupID: &f.groupID, OrderedIDs: []string{f.collectionID}, Strict: true, ExpectedRevision: &rev}); err != nil {
		t.Fatal(err)
	}
	var groupID *string
	if err := f.pool.QueryRow(ctx, `SELECT group_id FROM library_collection_libraries WHERE collection_id=$1 AND library_id=$2`, f.collectionID, f.libraryID).Scan(&groupID); err != nil || groupID == nil || *groupID != f.groupID {
		t.Fatalf("move result %v: %v", groupID, err)
	}
	rev = f.revision(t, true)
	if err := f.groups.ReorderIfRevision(ctx, f.libraryID, []string{f.groupID, "ungrouped"}, rev); err != nil {
		t.Fatal(err)
	}
	if pos, err := f.groups.GetUngroupedSortOrder(ctx, f.libraryID); err != nil || pos != 1 {
		t.Fatalf("ungrouped position=%d %v", pos, err)
	}
	if got := other.revision(t, true); got != initialOther {
		t.Fatal("unrelated library invalidated")
	}
	// One collection can belong to several libraries. Removing one membership
	// advances both its own witness and the removed library's order witness.
	rev = f.revision(t, false)
	ids := []int{f.libraryID, other.libraryID}
	if err := f.repo.Update(ctx, UpdateLibraryCollectionInput{ID: f.collectionID, LibraryIDs: &ids, ExpectedRevision: &rev}); err != nil {
		t.Fatal(err)
	}
	otherBefore := other.revision(t, true)
	rev = f.revision(t, false)
	ids = []int{f.libraryID}
	if err := f.repo.Update(ctx, UpdateLibraryCollectionInput{ID: f.collectionID, LibraryIDs: &ids, ExpectedRevision: &rev}); err != nil {
		t.Fatal(err)
	}
	if got := other.revision(t, true); got <= otherBefore {
		t.Fatal("removed library witness not advanced")
	}
}

func TestLibraryCollectionMutationErrorClassificationDB(t *testing.T) {
	f := newLibraryCASFixture(t)
	m := libraryCollectionMutation{pool: f.pool, collectionID: f.collectionID}
	rev := f.revision(t, false)
	for _, tc := range []struct {
		name, code string
		expected   int64
		attempts   int
	}{{"deadlock", "40P01", rev, 1}, {"unrelated-serialization", "40001", rev, 3}, {"wildcard", "40001", -1, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := m.run(t.Context(), &tc.expected, func(pgx.Tx) error { calls++; return &pgconn.PgError{Code: tc.code} })
			pgErr, ok := errors.AsType[*pgconn.PgError](err)
			if calls != tc.attempts || !ok || pgErr.Code != tc.code || errors.Is(err, ErrLibraryCollectionRevisionMismatch) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	if err := m.run(ctx, &rev, func(pgx.Tx) error { calls++; return nil }); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled calls=%d err=%v", calls, err)
	}
}

func TestLibraryCollectionAddIfAbsentDB(t *testing.T) {
	f := newLibraryCASFixture(t)
	ctx := t.Context()
	id := fmt.Sprintf("add-absent-%d", f.libraryID)
	seedSortableItem(t, f.pool, id, id, 2020)
	if _, err := f.pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, f.libraryID); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.AddItemIfAbsent(ctx, f.collectionID, id, 204); err != nil {
		t.Fatal(err)
	}
	before := f.revision(t, false)
	if err := f.repo.AddItemIfAbsent(ctx, f.collectionID, id, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.revision(t, false); got != before {
		t.Fatal("duplicate add invalidated witness")
	}
	items, err := f.repo.ListItems(ctx, f.collectionID)
	if err != nil || len(items) != 1 || items[0].Position != 204 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if err := f.repo.AddItem(ctx, f.collectionID, id, 2); err != nil {
		t.Fatal(err)
	}
	items, err = f.repo.ListItems(ctx, f.collectionID)
	if err != nil || items[0].Position != 2 {
		t.Fatal("legacy upsert behavior changed")
	}
}

func TestLibraryCollectionCASProtectedGroupDB(t *testing.T) {
	f := newLibraryCASFixture(t)
	ctx := t.Context()
	g, err := f.groups.Create(ctx, CreateLibraryCollectionGroupInput{LibraryID: f.libraryID, Name: "Users", Kind: models.GroupKindUserCollections})
	if err != nil {
		t.Fatal(err)
	}
	rev := f.revision(t, true)
	if err := f.groups.DeleteIfRevision(ctx, g.ID, rev); err == nil {
		t.Fatal("deleted protected group")
	}
	if got := f.revision(t, true); got != rev {
		t.Fatal("rejected delete changed witness")
	}
}

func TestLibraryCollectionCASSectionInUseDB(t *testing.T) {
	f := newLibraryCASFixture(t)
	ctx := t.Context()
	sectionID := fmt.Sprintf("cas-section-%d", f.libraryID)
	if _, err := f.pool.Exec(ctx, `INSERT INTO page_sections(id,scope,library_id,position,section_type,title,config) VALUES($1,'library',$2,0,'library_collection','Referenced',jsonb_build_object('library_collection_id',$3::text))`, sectionID, f.libraryID, f.collectionID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE id=$1`, sectionID) })
	rev := f.revision(t, false)
	if err := f.repo.DeleteIfRevision(ctx, f.collectionID, rev); !errors.Is(err, ErrLibraryCollectionInUse) {
		t.Fatalf("referenced delete: %v", err)
	}
	if got := f.revision(t, false); got != rev {
		t.Fatal("rejected delete consumed witness")
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM page_sections WHERE id=$1`, sectionID); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.DeleteIfRevision(ctx, f.collectionID, rev); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryCollectionOrderRevisionBulkAndUngroupedDB(t *testing.T) {
	f := newLibraryCASFixture(t)
	ctx := t.Context()
	if _, err := f.groups.Create(ctx, CreateLibraryCollectionGroupInput{LibraryID: f.libraryID, Name: "second"}); err != nil {
		t.Fatal(err)
	}
	before := f.revision(t, true)
	if _, err := f.pool.Exec(ctx, `UPDATE library_collection_groups SET sort_order=sort_order+1 WHERE library_id=$1`, f.libraryID); err != nil {
		t.Fatal(err)
	}
	if after := f.revision(t, true); after != before+1 {
		t.Fatalf("one bulk statement changed witness %d -> %d", before, after)
	}
	before = f.revision(t, true)
	if _, err := f.pool.Exec(ctx, `UPDATE media_folders SET name=name WHERE id=$1`, f.libraryID); err != nil {
		t.Fatal(err)
	}
	if after := f.revision(t, true); after != before {
		t.Fatal("unrelated folder field invalidated order")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE media_folders SET collection_ungrouped_sort_order=collection_ungrouped_sort_order+1 WHERE id=$1`, f.libraryID); err != nil {
		t.Fatal(err)
	}
	if after := f.revision(t, true); after != before+1 {
		t.Fatal("legacy synthetic group position did not invalidate witness")
	}
}
