package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLibraryCollectionNamesDB: an admin collection's title is unique among
// the collections of each library it shows in, ignoring case and extra
// spaces, and its slug, which the server derives from the title, never fails
// a save: titles that share a slug, or have none ("Sci-Fi" and "Sci Fi", two
// Japanese titles), take the first free "<slug>-<n>".
func TestLibraryCollectionNamesDB(t *testing.T) {
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
	// Registered first, so it runs after the cleanups that delete the fixtures.
	t.Cleanup(pool.Close)
	stamp := time.Now().UnixNano()
	library := func(name string) int {
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("%s %d", name, stamp)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, id) })
		return id
	}
	movies, shows := library("Movies"), library("Shows")
	moviesName := fmt.Sprintf("Movies %d", stamp)
	repo := NewLibraryCollectionRepository(pool)

	created := map[string]string{}
	for _, tc := range []struct {
		key, title, slug string
		libraries        []int
		unique           bool
		wantSlug         string
		wantTakenIn      string
	}{
		{key: "picks", title: "Server Picks", slug: "server-picks", libraries: []int{movies}, unique: true, wantSlug: "server-picks"},
		{title: "\t server   PICKS\n", slug: "server-picks", libraries: []int{movies}, unique: true, wantTakenIn: moviesName},
		// No-break and ideographic spaces read as spaces, as the service compares titles.
		{title: "Server\u00a0Picks", libraries: []int{movies}, unique: true, wantTakenIn: moviesName},
		{title: "Server\u3000Picks", libraries: []int{movies}, unique: true, wantTakenIn: moviesName},
		// Shares Movies, though its first library (the slug's) is Shows.
		{title: "Server Picks", slug: "server-picks", libraries: []int{shows, movies}, unique: true, wantTakenIn: moviesName},
		{title: "Server Picks", slug: "server-picks", libraries: []int{shows}, unique: true, wantSlug: "server-picks"},
		// Imports, templates and restores keep their own rules for titles.
		{key: "import", title: "Server Picks", slug: "server-picks", libraries: []int{movies}, wantSlug: "server-picks-2"},
		{key: "scifi", title: "Sci-Fi", slug: "sci-fi", libraries: []int{movies}, unique: true, wantSlug: "sci-fi"},
		{key: "scifi2", title: "Sci Fi", slug: "sci-fi", libraries: []int{movies}, unique: true, wantSlug: "sci-fi-2"},
		{title: "日本映画", libraries: []int{movies}, unique: true, wantSlug: "collection"},
		{title: "アニメ映画", libraries: []int{movies}, unique: true, wantSlug: "collection-2"},
		{title: "Sci.Fi", slug: "sci-fi", libraries: []int{shows}, unique: true, wantSlug: "sci-fi"},
	} {
		c, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryIDs: tc.libraries, Slug: tc.slug, Title: tc.title, UniqueTitle: tc.unique})
		if tc.wantTakenIn != "" {
			var taken *LibraryCollectionTitleTakenError
			if !errors.As(err, &taken) || taken.LibraryName != tc.wantTakenIn {
				t.Fatalf("create %q in %v: err = %v, want the title taken in %q", tc.title, tc.libraries, err, tc.wantTakenIn)
			}
			continue
		}
		if err != nil {
			t.Fatalf("create %q in %v: %v", tc.title, tc.libraries, err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM library_collections WHERE id=$1`, c.ID) })
		if c.Slug != tc.wantSlug {
			t.Fatalf("create %q in %v: slug %q, want %q", tc.title, tc.libraries, c.Slug, tc.wantSlug)
		}
		if tc.key != "" {
			created[tc.key] = c.ID
		}
	}

	// A rename to a title another collection of its libraries has is refused.
	rename := "SCI-FI"
	err = repo.Update(ctx, UpdateLibraryCollectionInput{ID: created["scifi2"], Title: &rename, UniqueTitle: true})
	if taken := (*LibraryCollectionTitleTakenError)(nil); !errors.As(err, &taken) {
		t.Fatalf("rename to %q: err = %v, want the title taken", rename, err)
	}

	// moveToShows moves a collection to Shows alone, sending slug when it is
	// not empty, and checks the slug it ends up with there.
	scope := []int{shows}
	moveToShows := func(key, slug, wantSlug string) {
		t.Helper()
		in := UpdateLibraryCollectionInput{ID: created[key], LibraryIDs: &scope}
		if slug != "" {
			in.Slug = &slug
		}
		if err := repo.Update(ctx, in); err != nil {
			t.Fatalf("move %s to Shows sending slug %q: %v", key, slug, err)
		}
		if got, err := repo.GetByID(ctx, created[key]); err != nil || got.LibraryID != shows || got.Slug != wantSlug {
			t.Fatalf("move %s to Shows sending slug %q: got %+v, %v, want slug %q", key, slug, got, err, wantSlug)
		}
	}

	// A collection whose libraries no longer include its slug's library moves
	// to the first one, with a free "<slug>-<n>" when Sci.Fi has its slug there.
	moveToShows("scifi", "", "sci-fi-2")

	// A slug sent with a scope change is checked where the collection lands:
	// one taken only in Movies is free in Shows, one taken in Shows is refused
	// rather than suffixed.
	moveToShows("scifi2", "server-picks-2", "server-picks-2")
	takenInShows := "sci-fi"
	err = repo.Update(ctx, UpdateLibraryCollectionInput{ID: created["import"], LibraryIDs: &scope, Slug: &takenInShows})
	if !IsLibraryCollectionSlugConflict(err) {
		t.Fatalf("move the import to Shows as %q: err = %v, want a slug conflict", takenInShows, err)
	}

	// A client that sends the whole collection echoes the slug it read; the
	// move still takes a free one where the slug is taken.
	moveToShows("picks", "server-picks", "server-picks-3")
}

// TestLibraryCollectionNameRacesDB: a create waits for a library delete
// rather than deadlocking with it, and a move that takes a free slug where it
// lands skips one another writer claimed after the move's SERIALIZABLE
// snapshot began, which the snapshot can't see.
func TestLibraryCollectionNameRacesDB(t *testing.T) {
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
	t.Cleanup(pool.Close)
	stamp := time.Now().UnixNano()
	library := func(t *testing.T, name string) int {
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("%s %d", name, stamp)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, id) })
		return id
	}
	cleanup := func(t *testing.T, id string) {
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM library_collections WHERE id=$1`, id) })
	}
	repo := NewLibraryCollectionRepository(pool)
	hold := func(t *testing.T) (pgx.Tx, func(done <-chan error)) { return holdLocks(ctx, t, pool) }

	t.Run("create during a library delete", func(t *testing.T) {
		// A library delete holds the folder row, then its cascade bumps the
		// library's order revision.
		deleted := library(t, "Deleted")
		del, waitBlocked := hold(t)
		if _, err := del.Exec(ctx, `SELECT 1 FROM media_folders WHERE id=$1 FOR UPDATE`, deleted); err != nil {
			t.Fatal(err)
		}
		created := make(chan error, 1)
		var createdID string
		go func() {
			c, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryIDs: []int{deleted}, Title: "Picks", Slug: "picks", UniqueTitle: true})
			if c != nil {
				createdID = c.ID
			}
			created <- err
		}()
		waitBlocked(created)
		if _, err := del.Exec(ctx, `INSERT INTO library_collection_order_revisions(library_id,revision) VALUES($1,2) ON CONFLICT(library_id) DO UPDATE SET revision=library_collection_order_revisions.revision+1`, deleted); err != nil {
			t.Fatalf("library delete's order bump: %v", err)
		}
		if err := del.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-created; err != nil {
			t.Fatalf("create racing a library delete: %v", err)
		}
		cleanup(t, createdID)
	})

	t.Run("move after its slug is claimed", func(t *testing.T) {
		// Another collection takes the moving one's slug in the destination while
		// the move waits on its row.
		from, to := library(t, "From"), library(t, "To")
		moving, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryIDs: []int{from}, Title: "Picks", Slug: "picks"})
		if err != nil {
			t.Fatal(err)
		}
		cleanup(t, moving.ID)
		there, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryIDs: []int{to}, Title: "Zed", Slug: "zed"})
		if err != nil {
			t.Fatal(err)
		}
		cleanup(t, there.ID)
		claim, waitBlocked := hold(t)
		if _, err := claim.Exec(ctx, `SELECT 1 FROM library_collections WHERE id=$1 FOR UPDATE`, moving.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := claim.Exec(ctx, `UPDATE library_collections SET slug='picks' WHERE id=$1`, there.ID); err != nil {
			t.Fatal(err)
		}
		moved := make(chan error, 1)
		go func() {
			anyRevision := int64(-1)
			moved <- repo.Update(ctx, UpdateLibraryCollectionInput{ID: moving.ID, ExpectedRevision: &anyRevision, LibraryIDs: &[]int{to}})
		}()
		waitBlocked(moved)
		if err := claim.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-moved; err != nil {
			t.Fatalf("move after another collection took its slug: %v", err)
		}
		if got, err := repo.GetByID(ctx, moving.ID); err != nil || got.LibraryID != to || got.Slug != "picks-2" {
			t.Fatalf("moved collection: %+v, %v, want slug picks-2 in %d", got, err, to)
		}
	})
}

// holdLocks opens a transaction for another writer and returns it with a
// function that waits until some backend waits on its locks, failing if the
// writer under test reports on done first.
func holdLocks(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (pgx.Tx, func(done <-chan error)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return tx, func(done <-chan error) {
		t.Helper()
		for blocked := false; !blocked; {
			select {
			case err := <-done:
				t.Fatalf("finished before the other writer let go: %v", err)
			default:
			}
			if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if !blocked {
				time.Sleep(10 * time.Millisecond) //nolint:forbidigo // the poll interval; the wait is on pg_blocking_pids, an observable lock wait
			}
		}
	}
}
