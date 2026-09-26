package pgstore

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

func TestPostgresCollectionContinuation(t *testing.T) {
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
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("collection-page-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_revisions WHERE user_id=$1`, userID)
	}()
	s := newStore(pool, userID)
	c, err := s.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "paging"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 7 {
		if err := s.AddCollectionItem(ctx, c.ID, fmt.Sprintf("item-%d", i), i/2); err != nil {
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
	if fmt.Sprint(got) != "[item-0 item-1 item-2 item-3 item-4 item-5 item-6]" {
		t.Fatalf("unexpected order %v", got)
	}
	if _, err := newStore(pool, userID+1000000).ListCollectionItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 2}); !errors.Is(err, userstore.ErrCollectionNotFound) {
		t.Fatalf("cross account read: %v", err)
	}

	bulkBefore, err := s.CollectionRevision(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_personal_collection_items(user_id,collection_id,media_item_id,position,added_at) SELECT $1,$2,'bulk-' || n,n,NOW() FROM generate_series(1,1000) n`, userID, c.ID); err != nil {
		t.Fatal(err)
	}
	bulkAfter, err := s.CollectionRevision(ctx, c.ID)
	if err != nil || bulkAfter != bulkBefore+1 {
		t.Fatalf("bulk insert revision %d -> %d (%v)", bulkBefore, bulkAfter, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM user_personal_collection_items WHERE user_id=$1 AND collection_id=$2 AND media_item_id LIKE 'bulk-%'`, userID, c.ID); err != nil {
		t.Fatal(err)
	}
	bulkDeleted, err := s.CollectionRevision(ctx, c.ID)
	if err != nil || bulkDeleted != bulkAfter+1 {
		t.Fatalf("bulk delete revision %d -> %d (%v)", bulkAfter, bulkDeleted, err)
	}
	before, err := s.CollectionRevision(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE user_personal_collection_items SET position=99 WHERE user_id=$1 AND collection_id=$2`, userID, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.CollectionRevision(ctx, c.ID)
	if err != nil || before != after {
		t.Fatalf("rollback changed witness: %d -> %d (%v)", before, after, err)
	}
	for _, query := range []string{
		`INSERT INTO user_personal_collection_items(user_id,collection_id,media_item_id,position,added_at) VALUES($1,$2,'new-item',-1,NOW())`,
		`UPDATE user_personal_collection_items SET position=position+1 WHERE user_id=$1 AND collection_id=$2`,
		`DELETE FROM user_personal_collection_items WHERE user_id=$1 AND collection_id=$2 AND media_item_id='new-item'`,
		`UPDATE user_personal_collections SET query_definition='{"changed":true}' WHERE user_id=$1 AND id=$2`,
		`INSERT INTO user_personal_collection_profiles(user_id,collection_id,profile_id) VALUES($1,$2,'viewer')`,
		`DELETE FROM user_personal_collection_profiles WHERE user_id=$1 AND collection_id=$2 AND profile_id='viewer'`,
	} {
		first, err := s.ListCollectionItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, query, userID, c.ID); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(query, "user_personal_collection_items") {
			after, err := s.CollectionRevision(ctx, c.ID)
			if err != nil || after != first.Revision+1 {
				t.Fatalf("bulk statement advanced revision %d -> %d (%v)", first.Revision, after, err)
			}
		}
		_, err = newStore(pool, userID).ListCollectionItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 2, Revision: first.Revision})
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
