package catalog

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestBrowseNamePrefixUsesSortTitlePostgres checks the alphabetical jump
// against real rows: a title with a leading article is listed under the letter
// its sort_title sorts by, not under the article's letter, and titles without a
// sort_title fall back to their title. Set SILO_TEST_DATABASE_URL to a migrated
// disposable database; every inserted row is removed on completion.
func TestBrowseNamePrefixUsesSortTitlePostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := "name-prefix-" + uuid.NewString()

	var libraryID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`,
		prefix,
	).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id=$1`, libraryID)
	})

	hobbit, hobgoblins, titanic := prefix+"-hobbit", prefix+"-hobgoblins", prefix+"-titanic"
	for _, row := range []struct {
		id, title, sortTitle string
	}{
		{hobbit, "The Hobbit: The Battle of the Five Armies", "Hobbit: The Battle of the Five Armies, The"},
		{hobgoblins, "Hobgoblins", ""},
		{titanic, "Titanic", ""},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, sort_title, year, genres)
			VALUES ($1, 'movie', $2, $3, 2014, '{}'::text[])
		`, row.id, row.title, row.sortTitle); err != nil {
			t.Fatalf("seed media item %s: %v", row.id, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_item_libraries(content_id,media_folder_id,first_seen_at) VALUES($1,$2,NOW())`,
			row.id, libraryID,
		); err != nil {
			t.Fatalf("link media item %s: %v", row.id, err)
		}
	}

	repo := NewBrowseRepository(pool)
	for _, tc := range []struct {
		namePrefix string
		want       []string
	}{
		{namePrefix: "H", want: []string{hobbit, hobgoblins}},
		{namePrefix: "T", want: []string{titanic}},
		{namePrefix: "the", want: nil},
		{namePrefix: "hobbit", want: []string{hobbit}},
	} {
		t.Run(tc.namePrefix, func(t *testing.T) {
			result, err := repo.Browse(ctx, BrowseFilters{
				Type:       "movie",
				LibraryID:  libraryID,
				NamePrefix: tc.namePrefix,
				Sort:       BrowseSortTitle,
				Order:      "asc",
				Limit:      10,
			})
			if err != nil {
				t.Fatalf("browse: %v", err)
			}
			var got []string
			for _, item := range result.Items {
				got = append(got, item.ContentID)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("NamePrefix %q = %v, want %v", tc.namePrefix, got, tc.want)
			}
		})
	}
}
