package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestPostgresManualCollectionsHolding(t *testing.T) {
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
	account := func(name string) int {
		t.Helper()
		var uid int
		if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("collection-holding-%s-%d", name, time.Now().UnixNano())).Scan(&uid); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, uid)
			_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_revisions WHERE user_id=$1`, uid)
			_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_order_revisions WHERE user_id=$1`, uid)
		})
		return uid
	}
	// Another login's profile with the same id holds the same title in a
	// manual collection; the account under test must not report it.
	other := newStore(pool, account("other"))
	c, err := other.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Other login", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.AddCollectionItem(ctx, c.ID, "title", 0); err != nil {
		t.Fatal(err)
	}
	storetest.RunManualCollectionsHolding(t, newStore(pool, account("self")))
}
