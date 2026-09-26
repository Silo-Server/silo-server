package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLibraryCollectionContinuationDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := time.Now().UnixNano()
	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("continuation-%d", suffix)).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, libraryID) }()
	repo := NewLibraryCollectionRepository(pool)
	c, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryID: libraryID, Slug: fmt.Sprintf("continuation-%d", suffix), Title: "Page"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = repo.Delete(context.Background(), c.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, c.ID)
	}()
	var inputs []LibraryCollectionItemInput
	for i := range 5 {
		id := fmt.Sprintf("continuation-item-%d-%d", suffix, i)
		seedSortableItem(t, pool, id, id, 2020)
		inputs = append(inputs, LibraryCollectionItemInput{MediaItemID: id})
	}
	if err := repo.ReplaceItems(ctx, c.ID, inputs); err != nil {
		t.Fatal(err)
	}
	first, err := repo.ListItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || !first.HasMore {
		t.Fatalf("first: %+v", first)
	}
	last := first.Items[1]
	next, err := NewLibraryCollectionRepository(pool).ListItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 2, Revision: first.Revision, After: &userstore.CollectionItemPosition{Position: last.Position, MediaItemID: last.MediaItemID}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Items[0].MediaItemID != inputs[2].MediaItemID {
		t.Fatalf("wrong continuation: %+v", next)
	}
	for _, query := range []string{
		`UPDATE library_collection_items SET position=position+1 WHERE collection_id=$1`,
		`DELETE FROM library_collection_items WHERE collection_id=$1 AND position=1`,
		`UPDATE library_collections SET visibility='hidden' WHERE id=$1`,
		`UPDATE library_collection_libraries SET sort_order=sort_order+1 WHERE collection_id=$1`,
	} {
		page, err := repo.ListItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, query, c.ID); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(query, "library_collection_items") {
			after, err := repo.CollectionRevision(ctx, c.ID)
			if err != nil || after != page.Revision+1 {
				t.Fatalf("bulk statement advanced revision %d -> %d (%v)", page.Revision, after, err)
			}
		}
		_, err = repo.ListItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 2, Revision: page.Revision})
		if !errors.Is(err, userstore.ErrCollectionChanged) {
			t.Fatalf("after %s: %v", query, err)
		}
	}
	// The continuation predicate must be an index condition, not a sort or filter
	// that scans all preceding membership rows. Disable sequential scans only to
	// demonstrate the bounded access path on this intentionally tiny fixture.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN SELECT media_item_id FROM library_collection_items WHERE collection_id=$1 AND (COALESCE(position,0),media_item_id)>($2,$3) ORDER BY COALESCE(position,0),media_item_id LIMIT 3`, c.ID, 2, "item")
	if err != nil {
		t.Fatal(err)
	}
	var plan string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan += line + "\n"
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan, "Sort") || !strings.Contains(plan, "Index Cond:") || !strings.Contains(plan, "ROW(COALESCE") {
		t.Fatalf("unbounded continuation plan:\n%s", plan)
	}
}
