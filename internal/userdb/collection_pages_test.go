package userdb

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestSQLiteCollectionContinuation(t *testing.T) {
	s := newConformanceStore(t).(*SQLiteUserStore)
	ctx := t.Context()
	c, err := s.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "paged"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 9 {
		if err := s.AddCollectionItem(ctx, c.ID, fmt.Sprintf("item-%02d", i), i/3); err != nil {
			t.Fatal(err)
		}
	}
	opts := userstore.CollectionItemsPageOptions{Limit: 2}
	var got []string
	for {
		page, err := s.ListCollectionItemsPage(ctx, c.ID, opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 2 || page.Revision < 1 {
			t.Fatalf("invalid page: %+v", page)
		}
		for _, item := range page.Items {
			if item.AddedAt == "" {
				t.Fatal("membership timestamp missing")
			}
			got = append(got, item.MediaItemID)
		}
		if !page.HasMore {
			break
		}
		last := page.Items[len(page.Items)-1]
		opts.After = &userstore.CollectionItemPosition{Position: last.Position, MediaItemID: last.MediaItemID}
		opts.Revision = page.Revision
	}
	want := []string{"item-00", "item-01", "item-02", "item-03", "item-04", "item-05", "item-06", "item-07", "item-08"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	before, err := s.CollectionRevision(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE personal_collection_items SET position=99 WHERE collection_id=?`, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := s.CollectionRevision(ctx, c.ID)
	if err != nil || before != after {
		t.Fatalf("rollback changed revision: %d -> %d (%v)", before, after, err)
	}
	// NULL positions from legacy/direct writers are normalized before readers
	// observe the row so indexed tuple seeking retains every membership.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO personal_collection_items VALUES (?, 'null-position', NULL, '2026-01-01')`, c.ID); err != nil {
		t.Fatal(err)
	}
	var position int
	if err := s.db.QueryRowContext(ctx, `SELECT position FROM personal_collection_items WHERE collection_id=? AND media_item_id='null-position'`, c.ID).Scan(&position); err != nil || position != 0 {
		t.Fatalf("NULL normalization: %d %v", position, err)
	}
	// A separate store handle observes the same persisted witness.
	other := NewSQLiteUserStore(s.db)
	for _, query := range []string{
		`INSERT INTO personal_collection_items VALUES (?, 'new-item', -1, '2026-01-01')`,
		`UPDATE personal_collection_items SET position = position + 1 WHERE collection_id = ?`,
		`DELETE FROM personal_collection_items WHERE collection_id = ? AND media_item_id = 'new-item'`,
		`UPDATE personal_collections SET query_definition = '{"changed":true}' WHERE id = ?`,
		`INSERT INTO personal_collection_profiles VALUES (?, 'viewer')`,
		`DELETE FROM personal_collection_profiles WHERE collection_id = ? AND profile_id = 'viewer'`,
	} {
		first, err := s.ListCollectionItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, query, c.ID); err != nil {
			t.Fatal(err)
		}
		_, err = other.ListCollectionItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 2, Revision: first.Revision})
		if !errors.Is(err, userstore.ErrCollectionChanged) {
			t.Fatalf("after %s: %v", query, err)
		}
	}
	if err := s.DeleteCollection(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListCollectionItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 2}); !errors.Is(err, userstore.ErrCollectionNotFound) {
		t.Fatalf("deleted collection: %v", err)
	}
}

func TestSQLiteCollectionContinuationSeekIndex(t *testing.T) {
	s := newConformanceStore(t).(*SQLiteUserStore)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT media_item_id, position FROM personal_collection_items WHERE collection_id = ? AND (position,media_item_id) > (?,?) ORDER BY position,media_item_id LIMIT 3`, "collection", 3, "item")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(detail, "personal_collection_items_continuation_idx (collection_id=? AND (position,media_item_id)>(?,?))") {
			t.Fatalf("not a bounded tuple seek: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteCollectionContinuationLegacyNullMigration(t *testing.T) {
	s := newConformanceStore(t).(*SQLiteUserStore)
	if _, err := s.db.Exec(`DROP TRIGGER personal_collection_items_position_insert;
 INSERT INTO personal_collection_items VALUES ('legacy', 'item', NULL, '2026-01-01');
 PRAGMA user_version=21;`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(s.db); err != nil {
		t.Fatal(err)
	}
	var position int
	if err := s.db.QueryRow(`SELECT position FROM personal_collection_items WHERE collection_id='legacy'`).Scan(&position); err != nil || position != 0 {
		t.Fatalf("legacy NULL position: %d %v", position, err)
	}
	if version, err := userVersion(s.db); err != nil || version != schemaVersion {
		t.Fatalf("version: %d %v", version, err)
	}
}
