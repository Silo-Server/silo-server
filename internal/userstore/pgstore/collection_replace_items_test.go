package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestPostgresReplaceCollectionItemsWritesOnlyTheDifference pins the
// set-based membership replace a list sync uses: titles that stay keep their
// added_at, moved titles get their new position, and a replace that changes
// nothing leaves the collection revision, and so every client cursor, alone.
func TestPostgresReplaceCollectionItemsWritesOnlyTheDifference(t *testing.T) {
	f := newReplaceItemsFixture(t)
	f.replace(t, "a", "b", "c")
	// Age every row so a rewritten added_at cannot pass for a kept one.
	if _, err := f.pool.Exec(t.Context(), `UPDATE user_personal_collection_items SET added_at = '2020-01-01T00:00:00Z' WHERE user_id = $1 AND collection_id = $2`, f.userID, f.collectionID); err != nil {
		t.Fatal(err)
	}
	f.old = f.expect(t, "a", "b", "c")["a"]

	cases := []struct {
		name      string
		ids, kept []string
		advances  bool
	}{
		{"unchanged", []string{"a", "b", "c"}, []string{"a", "b", "c"}, false},
		{"reordered", []string{"c", "a", "b"}, []string{"a", "b", "c"}, true},
		{"grown", []string{"c", "d", "a", "b", "e"}, []string{"a", "b", "c"}, true},
		{"shrunk", []string{"a", "e"}, []string{"a"}, true},
		{"emptied", nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := f.revision(t)
			f.replace(t, tc.ids...)
			added := f.expect(t, tc.ids...)
			for id, at := range added {
				if (at == f.old) != slices.Contains(tc.kept, id) {
					t.Fatalf("added_at of %s = %s, kept = %v", id, at, slices.Contains(tc.kept, id))
				}
			}
			if advanced := f.revision(t) != before; advanced != tc.advances {
				t.Fatalf("revision advanced = %v, want %v", advanced, tc.advances)
			}
		})
	}

	t.Run("duplicate keeps the first occurrence", func(t *testing.T) {
		if err := f.store.ReplaceCollectionItems(t.Context(), f.collectionID, []userstore.CollectionItemReplacement{
			{MediaItemID: "e", Position: 0}, {MediaItemID: "a", Position: 1}, {MediaItemID: "e", Position: 2},
		}); err != nil {
			t.Fatal(err)
		}
		f.expect(t, "e", "a")
	})
}

// TestPostgresReplaceCollectionItemsWaitsOutADelete pins #2844: a list sync's
// replace that waits on a delete of its collection writes no rows under the
// deleted ID once the delete commits, and says the collection is gone.
func TestPostgresReplaceCollectionItemsWaitsOutADelete(t *testing.T) {
	f := newReplaceItemsFixture(t)
	f.replace(t, "a")
	ctx := t.Context()

	// Delete the collection the way deleteCollection does, without committing.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // a no-op after the commit
	for _, stmt := range []string{
		`SELECT 1 FROM user_personal_collections WHERE user_id = $1 AND id = $2 FOR UPDATE`,
		`DELETE FROM user_personal_collection_items WHERE user_id = $1 AND collection_id = $2`,
		`DELETE FROM user_personal_collections WHERE user_id = $1 AND id = $2`,
	} {
		if _, err := tx.Exec(ctx, stmt, f.userID, f.collectionID); err != nil {
			t.Fatal(err)
		}
	}

	replacePool, app := newDeleteSessionPool(t)
	replacer := newStore(replacePool, f.userID)
	done := make(chan error, 1)
	go func() {
		done <- replacer.ReplaceCollectionItems(ctx, f.collectionID, []userstore.CollectionItemReplacement{
			{MediaItemID: "a", Position: 0}, {MediaItemID: "b", Position: 1},
		})
	}()
	waitForBlockedDelete(t, replacePool, app, func() {
		select {
		case err := <-done:
			t.Fatalf("replace finished before the delete committed: %v", err)
		default:
		}
	})
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, userstore.ErrCollectionNotFound) {
		t.Fatalf("replace after the delete = %v, want ErrCollectionNotFound", err)
	}
	var orphans int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM user_personal_collection_items WHERE user_id = $1 AND collection_id = $2`, f.userID, f.collectionID).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("%d item rows under the deleted collection, want 0", orphans)
	}
}

type replaceItemsFixture struct {
	pool         *pgxpool.Pool
	store        *PostgresUserStore
	userID       int
	collectionID string
	old          string
}

func newReplaceItemsFixture(t *testing.T) *replaceItemsFixture {
	t.Helper()
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
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, fmt.Sprintf("collection-replace-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM user_collection_revisions WHERE user_id=$1`, userID)
	})
	store := newStore(pool, userID)
	const profile = "replace-profile"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "Replace"}); err != nil {
		t.Fatal(err)
	}
	c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: profile, Name: "Synced", CollectionType: "mdblist", QueryDefinition: "{}",
		SourceConfig: `{"mode":"mdblist","url":"https://mdblist.com/lists/user/list"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &replaceItemsFixture{pool: pool, store: store, userID: userID, collectionID: c.ID}
}

func (f *replaceItemsFixture) replace(t *testing.T, ids ...string) {
	t.Helper()
	items := make([]userstore.CollectionItemReplacement, len(ids))
	for i, id := range ids {
		items[i] = userstore.CollectionItemReplacement{MediaItemID: id, Position: i}
	}
	if err := f.store.ReplaceCollectionItems(t.Context(), f.collectionID, items); err != nil {
		t.Fatal(err)
	}
}

func (f *replaceItemsFixture) revision(t *testing.T) int64 {
	t.Helper()
	rev, err := f.store.CollectionRevision(t.Context(), f.collectionID)
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

// expect checks the stored order and answers each member's added_at.
func (f *replaceItemsFixture) expect(t *testing.T, want ...string) map[string]string {
	t.Helper()
	items, err := f.store.ListCollectionItems(t.Context(), f.collectionID)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(items))
	added := make(map[string]string, len(items))
	for i, item := range items {
		if item.Position != i {
			t.Fatalf("item %s at position %d, want %d", item.MediaItemID, item.Position, i)
		}
		got = append(got, item.MediaItemID)
		added[item.MediaItemID] = item.AddedAt
	}
	if !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	return added
}
