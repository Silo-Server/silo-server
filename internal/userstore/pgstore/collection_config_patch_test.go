package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresCollectionSourceConfigConcurrentPatches(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, fmt.Sprintf("collection-patch-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id=$1`, userID) })
	store := newStore(pool, userID)
	const profile = "patch-profile"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "Patch"}); err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: profile, Name: "Patch", CollectionType: "mdblist", SourceConfig: `{"mode":"url","url":"https://mdblist.com/lists/user/list","limit":10,"library_ids":[7]}`})
	if err != nil {
		t.Fatal(err)
	}
	// Native item edits must coexist with Audiobookshelf chapter membership.
	if err := store.AddCollectionItem(ctx, collection.ID, "item", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCollectionItem(ctx, collection.ID, "item", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_personal_collection_items (user_id,collection_id,media_item_id,sub_item_id,position,added_at) VALUES ($1,$2,'item','chapter',9,now())`, userID, collection.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReorderCollectionItems(ctx, collection.ID, []string{"item"}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListCollectionItems(ctx, collection.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("native rows = %v, %v", rows, err)
	}
	if err := store.RemoveCollectionItem(ctx, collection.ID, "item"); err != nil {
		t.Fatal(err)
	}
	var chapterPosition int
	if err := pool.QueryRow(ctx, `SELECT position FROM user_personal_collection_items WHERE user_id=$1 AND collection_id=$2 AND sub_item_id='chapter'`, userID, collection.ID).Scan(&chapterPosition); err != nil || chapterPosition != 9 {
		t.Fatalf("chapter changed: %d, %v", chapterPosition, err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, patch := range []string{`{"limit":25}`, `{"library_ids":[8,9]}`} {
		wg.Go(func() {
			<-start
			errs <- store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: collection.ID, RequestProfileID: profile, SourceConfigPatch: new(patch)})
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetCollection(ctx, collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		URL        string `json:"url"`
		Limit      int    `json:"limit"`
		LibraryIDs []int  `json:"library_ids"`
	}
	if err := json.Unmarshal([]byte(got.SourceConfig), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://mdblist.com/lists/user/list" || cfg.Limit != 25 || len(cfg.LibraryIDs) != 2 || cfg.LibraryIDs[0] != 8 || cfg.LibraryIDs[1] != 9 {
		t.Fatalf("lost config change: %s", got.SourceConfig)
	}
}
