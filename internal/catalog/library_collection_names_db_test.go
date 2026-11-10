package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

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
	defer pool.Close()
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
		{title: " server   PICKS ", slug: "server-picks", libraries: []int{movies}, unique: true, wantTakenIn: moviesName},
		// Shares Movies, though its first library (the slug's) is Shows.
		{title: "Server Picks", slug: "server-picks", libraries: []int{shows, movies}, unique: true, wantTakenIn: moviesName},
		{title: "Server Picks", slug: "server-picks", libraries: []int{shows}, unique: true, wantSlug: "server-picks"},
		// Imports, templates and restores keep their own rules for titles.
		{title: "Server Picks", slug: "server-picks", libraries: []int{movies}, wantSlug: "server-picks-2"},
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

	// A collection whose libraries no longer include its slug's library moves
	// to the first one, with a free "<slug>-<n>" when Sci.Fi has its slug there.
	scope := []int{shows}
	if err := repo.Update(ctx, UpdateLibraryCollectionInput{ID: created["scifi"], LibraryIDs: &scope, UniqueTitle: true}); err != nil {
		t.Fatalf("move Sci-Fi to Shows: %v", err)
	}
	got, err := repo.GetByID(ctx, created["scifi"])
	if err != nil {
		t.Fatal(err)
	}
	if got.LibraryID != shows || got.Slug != "sci-fi-2" {
		t.Fatalf("move Sci-Fi to Shows: library %d slug %q, want %d %q", got.LibraryID, got.Slug, shows, "sci-fi-2")
	}
}
