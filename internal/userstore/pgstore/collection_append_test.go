package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestPostgresCollectionAtomicAppendDB(t *testing.T) {
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
	var uid int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("append-c24-%d", time.Now().UnixNano())).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	t.Logf("append cleanup account=%d", uid)
	defer func() {
		cleanup := context.Background()
		for _, table := range []string{"users", "user_collection_revisions", "user_collection_order_revisions"} {
			column := "user_id"
			if table == "users" {
				column = "id"
			}
			if _, err := pool.Exec(cleanup, "DELETE FROM "+table+" WHERE "+column+"=$1", uid); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	}()
	s := newStore(pool, uid)
	c, err := s.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "append", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendCollectionItem(ctx, c.ID, "empty-first"); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListCollectionItems(ctx, c.ID)
	if err != nil || len(items) != 1 || items[0].Position != 0 {
		t.Fatalf("empty append: %+v %v", items, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_personal_collection_items(user_id,collection_id,media_item_id,position,added_at)
 SELECT $1,$2,'off-page-'||n,n*3,NOW() FROM generate_series(1,300) n`, uid, c.ID); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListCollectionItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 200})
	if err != nil || !page.HasMore || len(page.Items) != 200 {
		t.Fatalf("partial page: %+v %v", page, err)
	}
	assertPersonalAppendTail(t, s, c.ID)
	if err := newStore(pool, uid+1000000).AppendCollectionItem(ctx, c.ID, "foreign"); !errors.Is(err, userstore.ErrCollectionNotFound) {
		t.Fatalf("cross-account append: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE user_personal_collections SET collection_type='smart' WHERE user_id=$1 AND id=$2`, uid, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendCollectionItem(ctx, c.ID, "wrong-type"); !errors.Is(err, userstore.ErrCollectionNotManual) {
		t.Fatalf("smart append: %v", err)
	}
}

func assertPersonalAppendTail(t *testing.T, s *PostgresUserStore, collectionID string) {
	t.Helper()
	ctx := t.Context()
	pool, uid := s.pool, s.userID
	const concurrent = 16
	start := make(chan struct{})
	results := make(chan error, concurrent)
	var wg sync.WaitGroup
	for i := range concurrent {
		wg.Go(func() {
			<-start
			results <- s.AppendCollectionItem(ctx, collectionID, fmt.Sprintf("appended-%02d", i))
		})
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count, distinct, first, last int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT position),min(position),max(position) FROM user_personal_collection_items WHERE user_id=$1 AND collection_id=$2 AND media_item_id LIKE 'appended-%'`, uid, collectionID).Scan(&count, &distinct, &first, &last); err != nil {
		t.Fatal(err)
	}
	if count != concurrent || distinct != concurrent || first != 901 || last != 900+concurrent {
		t.Fatalf("gapped tail/concurrent append: count=%d distinct=%d first=%d last=%d", count, distinct, first, last)
	}
	before, err := s.CollectionRevision(ctx, collectionID)
	if err != nil {
		t.Fatal(err)
	}
	var added time.Time
	if err := pool.QueryRow(ctx, `SELECT added_at FROM user_personal_collection_items WHERE user_id=$1 AND collection_id=$2 AND media_item_id='off-page-300'`, uid, collectionID).Scan(&added); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendCollectionItem(ctx, collectionID, "off-page-300"); err != nil {
		t.Fatal(err)
	}
	after, err := s.CollectionRevision(ctx, collectionID)
	if err != nil || before != after {
		t.Fatalf("duplicate changed revision: %d -> %d, %v", before, after, err)
	}
	var position int
	var addedAfter time.Time
	if err := pool.QueryRow(ctx, `SELECT position,added_at FROM user_personal_collection_items WHERE user_id=$1 AND collection_id=$2 AND media_item_id='off-page-300'`, uid, collectionID).Scan(&position, &addedAfter); err != nil || position != 900 || !added.Equal(addedAfter) {
		t.Fatalf("duplicate changed member: %d %v -> %v %v", position, added, addedAfter, err)
	}
	if err := s.AddCollectionItem(ctx, collectionID, "explicit", 17); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT position FROM user_personal_collection_items WHERE user_id=$1 AND collection_id=$2 AND media_item_id='explicit'`, uid, collectionID).Scan(&position); err != nil || position != 17 {
		t.Fatalf("explicit position changed: %d %v", position, err)
	}
}
