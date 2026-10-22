package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLibraryCollectionReplaceItemsWritesOnlyTheDifferenceDB pins the
// set-based membership replace a library list sync uses: titles that stay
// keep their created_at, moved titles get their new position and source rank,
// and a replace that changes nothing touches neither the collection row nor
// its revision. Set SILO_TEST_DATABASE_URL to a migrated database to run it.
func TestLibraryCollectionReplaceItemsWritesOnlyTheDifferenceDB(t *testing.T) {
	f := newLibraryReplaceItemsFixture(t)
	f.replace(t, "a", "b", "c")
	if _, err := f.pool.Exec(context.Background(), `UPDATE library_collection_items SET created_at = $2 WHERE collection_id = $1`, f.collectionID, f.old); err != nil {
		t.Fatalf("age items: %v", err)
	}

	cases := []struct {
		name         string
		names, kept  []string
		changesState bool
	}{
		{"unchanged", []string{"a", "b", "c"}, []string{"a", "b", "c"}, false},
		{"reordered", []string{"c", "a", "b"}, []string{"a", "b", "c"}, true},
		{"grown", []string{"c", "d", "a", "b", "e"}, []string{"a", "b", "c"}, true},
		{"shrunk", []string{"a", "e"}, []string{"a"}, true},
		{"emptied", nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := f.state(t)
			f.replace(t, tc.names...)
			f.expect(t, tc.kept, tc.names...)
			after := f.state(t)
			if tc.changesState {
				if after.revision == before.revision || !after.updatedAt.After(before.updatedAt) {
					t.Fatalf("replace left the collection at %+v (was %+v)", after, before)
				}
			} else if after != before {
				t.Fatalf("unchanged replace moved the collection from %+v to %+v", before, after)
			}
		})
	}

	t.Run("duplicate keeps the first occurrence", func(t *testing.T) {
		if err := f.repo.ReplaceItems(context.Background(), f.collectionID, []LibraryCollectionItemInput{
			{MediaItemID: f.id("e"), SourceRank: 10}, {MediaItemID: f.id("a"), SourceRank: 20}, {MediaItemID: f.id("e"), SourceRank: 30},
		}); err != nil {
			t.Fatalf("replace items: %v", err)
		}
		f.expect(t, nil, "e", "a")
	})
}

// TestLibraryCollectionReplaceItemsRacesMemberDeleteDB pins the lock order
// of a replace against a concurrent media item delete. The delete's cascade
// locks the member row before its trigger takes the collection revision, so a
// replace that takes the revision first and then waits on that member
// deadlocks. Collection d sorts first, and a third transaction holds its
// revision, which keeps the delete parked between those two locks.
func TestLibraryCollectionReplaceItemsRacesMemberDeleteDB(t *testing.T) {
	f := newLibraryReplaceItemsFixture(t)
	ctx := context.Background()
	f.replace(t, "a", "b", "c")

	other := fmt.Sprintf("00000000000000000000-%d", f.suffix)
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO library_collections (id, library_id, slug, title, collection_type)
		SELECT $1, library_id, $1, $1, 'manual' FROM library_collections WHERE id = $2
	`, other, f.collectionID); err != nil {
		t.Fatalf("create second collection: %v", err)
	}
	t.Cleanup(func() { _ = f.repo.Delete(context.Background(), other) })
	if err := f.repo.ReplaceItems(ctx, other, []LibraryCollectionItemInput{{MediaItemID: f.id("a")}}); err != nil {
		t.Fatalf("seed second collection: %v", err)
	}

	holder, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	defer holder.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if _, err := holder.Exec(ctx, `UPDATE library_collection_revisions SET revision = revision + 1 WHERE collection_id = $1`, other); err != nil {
		t.Fatalf("hold second revision: %v", err)
	}

	deleteConn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire delete connection: %v", err)
	}
	defer deleteConn.Release()
	deletePID := deleteConn.Conn().PgConn().PID()
	deleteErr := make(chan error, 1)
	go func() {
		_, err := deleteConn.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, f.id("a"))
		deleteErr <- err
	}()
	f.waitForLockWaiters(t, 1, deletePID)

	// b leaves, so the DELETE writes and its trigger takes the revision; a
	// moves, so the UPDATE needs the row the media item delete holds.
	replaceErr := make(chan error, 1)
	go func() {
		replaceErr <- f.repo.ReplaceItems(ctx, f.collectionID, []LibraryCollectionItemInput{
			{MediaItemID: f.id("c"), SourceRank: 10}, {MediaItemID: f.id("a"), SourceRank: 20},
		})
	}()
	f.waitForLockWaiters(t, 2, deletePID)
	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("release second revision: %v", err)
	}

	isDeadlock := func(err error) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.Code == "40P01"
	}
	if err := <-deleteErr; err != nil {
		t.Fatalf("media item delete failed: %v", err)
	}
	// The replace may still fail on the foreign key for the deleted title, as
	// it did before the set-based replace; it must not deadlock.
	if err := <-replaceErr; isDeadlock(err) {
		t.Fatalf("replace deadlocked against the media item delete: %v", err)
	}
}

// waitForLockWaiters waits until n backends, including pid, wait on a lock.
func (f *libraryReplaceItemsFixture) waitForLockWaiters(t *testing.T, n int, pid uint32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		var pidWaits bool
		if err := f.pool.QueryRow(context.Background(), `
			SELECT COUNT(*), COALESCE(BOOL_OR(pid = $1), FALSE)
			FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
		`, int32(pid)).Scan(&waiting, &pidWaits); err != nil {
			t.Fatalf("read lock waiters: %v", err)
		}
		if waiting >= n && pidWaits {
			return
		}
		time.Sleep(10 * time.Millisecond) //nolint:forbidigo // polls pg_stat_activity, the observable lock state
	}
	t.Fatalf("fewer than %d backends waited on a lock", n)
}

type libraryReplaceItemsFixture struct {
	pool         *pgxpool.Pool
	repo         *LibraryCollectionRepository
	suffix       int64
	collectionID string
	old          time.Time
}

type libraryReplaceItemsState struct {
	revision  int64
	updatedAt time.Time
}

func newLibraryReplaceItemsFixture(t *testing.T) *libraryReplaceItemsFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	f := &libraryReplaceItemsFixture{pool: pool, repo: NewLibraryCollectionRepository(pool), suffix: time.Now().UnixNano(), old: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	var libraryID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('movies', $1, TRUE)
		RETURNING id
	`, fmt.Sprintf("replace-repo-%d", f.suffix)).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, libraryID)
	})
	for i, name := range []string{"a", "b", "c", "d", "e"} {
		seedSortableItem(t, pool, f.id(name), name, 2000+i)
	}
	collection, err := f.repo.Create(ctx, CreateLibraryCollectionInput{LibraryID: libraryID, Slug: fmt.Sprintf("replace-repo-%d", f.suffix), Title: "Replaced", CollectionType: "mdblist"})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	t.Cleanup(func() { _ = f.repo.Delete(context.Background(), collection.ID) })
	f.collectionID = collection.ID
	return f
}

func (f *libraryReplaceItemsFixture) id(name string) string {
	return fmt.Sprintf("replace-repo-%s-%d", name, f.suffix)
}

// replace stores names in order, ranked 10, 20, 30… so a rank change is
// visible apart from a position change.
func (f *libraryReplaceItemsFixture) replace(t *testing.T, names ...string) {
	t.Helper()
	inputs := make([]LibraryCollectionItemInput, len(names))
	for i, name := range names {
		inputs[i] = LibraryCollectionItemInput{MediaItemID: f.id(name), Position: i, SourceRank: (i + 1) * 10}
	}
	if err := f.repo.ReplaceItems(context.Background(), f.collectionID, inputs); err != nil {
		t.Fatalf("replace items: %v", err)
	}
}

func (f *libraryReplaceItemsFixture) state(t *testing.T) libraryReplaceItemsState {
	t.Helper()
	var s libraryReplaceItemsState
	if err := f.pool.QueryRow(context.Background(), `
		SELECT v.revision, c.updated_at
		FROM library_collections c JOIN library_collection_revisions v ON v.collection_id = c.id
		WHERE c.id = $1
	`, f.collectionID).Scan(&s.revision, &s.updatedAt); err != nil {
		t.Fatalf("read collection state: %v", err)
	}
	return s
}

// expect checks the stored order, ranks, and which members kept the aged
// created_at.
func (f *libraryReplaceItemsFixture) expect(t *testing.T, kept []string, names ...string) {
	t.Helper()
	items, err := f.repo.ListItems(context.Background(), f.collectionID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	got := make([]string, 0, len(items))
	for i, item := range items {
		got = append(got, item.MediaItemID)
		if item.Position != i || item.SourceRank != (i+1)*10 {
			t.Fatalf("item %s at position %d rank %d, want %d rank %d", item.MediaItemID, item.Position, item.SourceRank, i, (i+1)*10)
		}
		wasKept := slices.ContainsFunc(kept, func(name string) bool { return f.id(name) == item.MediaItemID })
		if item.CreatedAt.Equal(f.old) != wasKept {
			t.Fatalf("item %s created_at %s, kept = %v", item.MediaItemID, item.CreatedAt, wasKept)
		}
	}
	want := make([]string, len(names))
	for i, name := range names {
		want[i] = f.id(name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
}
