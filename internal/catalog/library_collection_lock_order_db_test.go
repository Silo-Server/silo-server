package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestGroupingWaitsForTheEditorDB: a restore putting an existing
// collection's membership back in its group, creating the group first when
// it is gone, waits for an editor reordering the library, which holds the
// library's collection parents and their membership rows and then needs the
// order revision, instead of taking the order revision first and
// deadlocking with it.
func TestGroupingWaitsForTheEditorDB(t *testing.T) {
	for _, createGroup := range []bool{false, true} {
		t.Run(fmt.Sprintf("create group %v", createGroup), func(t *testing.T) { checkGroupingWaitsForTheEditor(t, createGroup) })
	}
}

// checkGroupingWaitsForTheEditor runs one case of
// TestGroupingWaitsForTheEditorDB: the restore's item locks the collection
// first (LockLibraryCollectionInTx), then, when createGroup, creates the
// group the backup's membership names, then regroups.
func checkGroupingWaitsForTheEditor(t *testing.T, createGroup bool) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies','grouping lock order',true) RETURNING id`).Scan(&library); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, library) }()
	setup, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = setup.Rollback(context.Background()) }()
	slug := fmt.Sprintf("grouping-%d", time.Now().UnixNano())
	collection, err := CreateLibraryCollectionInTx(ctx, setup, CreateLibraryCollectionInput{LibraryID: library, LibraryIDs: []int{library},
		Slug: slug, Title: "Grouping", CollectionType: "manual", Visibility: "visible"})
	if err != nil {
		t.Fatal(err)
	}
	groupID := ""
	if !createGroup {
		group, err := CreateLibraryCollectionGroupInTx(ctx, setup, CreateLibraryCollectionGroupInput{LibraryID: library, Name: "Group", Slug: slug})
		if err != nil {
			t.Fatal(err)
		}
		groupID = group.ID
	}
	if err := setup.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// The editor's first steps (MoveAndReorder): the library's parents, then
	// their membership rows.
	editor, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = editor.Rollback(context.Background()) }()
	if err := lockLibraryCollectionParents(ctx, editor, library); err != nil {
		t.Fatal(err)
	}
	if _, err := editor.Exec(ctx, `SELECT 1 FROM library_collection_libraries WHERE library_id=$1 FOR UPDATE`, library); err != nil {
		t.Fatal(err)
	}
	restored := make(chan error, 1)
	var pid uint32
	restore, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pid = restore.Conn().PgConn().PID()
	go func() {
		err := regroupAsRestore(ctx, restore, collection, library, slug, groupID, createGroup)
		restore.Release()
		restored <- err
	}()
	// Wait until the restore waits on a lock the editor holds. A backend
	// that is not waiting has no wait event: NULL, read as false.
	waitCtx, cancelWait := context.WithTimeout(ctx, 30*time.Second)
	defer cancelWait()
	for {
		var waiting bool
		if err := pool.QueryRow(waitCtx, `SELECT coalesce(wait_event_type = 'Lock', false) FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-restored:
			t.Fatalf("the restore finished while the editor held the library: %v", err)
		case <-waitCtx.Done():
			t.Fatal("the restore never waited for the editor")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// The editor's write takes the order revision through its trigger.
	if _, err := editor.Exec(ctx, `UPDATE library_collection_libraries SET sort_order = sort_order + 1 WHERE library_id=$1`, library); err != nil {
		t.Fatalf("the editor's reorder = %v, want it to finish before the restore", err)
	}
	if err := editor.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-restored:
		if err != nil {
			t.Fatalf("the restore after the editor = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the restore did not finish after the editor committed")
	}
	var grouped string
	if err := pool.QueryRow(ctx, `SELECT coalesce(g.slug, '') FROM library_collection_libraries l LEFT JOIN library_collection_groups g ON g.id = l.group_id
		WHERE l.collection_id=$1 AND l.library_id=$2`, collection, library).Scan(&grouped); err != nil || grouped != slug {
		t.Fatalf("membership group = %q, %v; want %s", grouped, err, slug)
	}
}

// regroupAsRestore is a restore item's transaction: the collection's parent
// first, then the group, created when createGroup, then the regroup.
func regroupAsRestore(ctx context.Context, conn *pgxpool.Conn, collection string, library int, slug, groupID string, createGroup bool) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if err := LockLibraryCollectionInTx(ctx, tx, collection); err != nil {
			return err
		}
		if createGroup {
			group, err := CreateLibraryCollectionGroupInTx(ctx, tx, CreateLibraryCollectionGroupInput{LibraryID: library, Name: "Group", Slug: slug})
			if err != nil {
				return err
			}
			groupID = group.ID
		}
		_, err := GroupUngroupedMembershipInTx(ctx, tx, collection, library, groupID)
		return err
	})
}
