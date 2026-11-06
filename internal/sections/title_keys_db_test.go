package sections

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A stored row keeps its title key while its title is the one the server
// wrote, whichever writer touches the row, and loses it to a rename. A
// generated library row's key follows the library's name.
func TestSectionTitleKeysFollowServerTitlesPostgres(t *testing.T) {
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

	name := "title-keys-" + uuid.NewString()[:8]
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', $1, true) RETURNING id`, name).Scan(&library); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM page_sections WHERE library_id = $1
			OR (scope = 'home' AND COALESCE(config->>'generated_library_id', config->>'filter_library_id') = $1::text)`, library)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, library)
	})
	repo := NewRepository(pool)
	find := func(scope string, libraryID *int, sectionType SectionType) *PageSection {
		t.Helper()
		rows, err := repo.ListByScopeAll(ctx, scope, libraryID)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.SectionType != sectionType {
				continue
			}
			if libraryID != nil || IsGeneratedHomeLibraryRecentSection(row, library) {
				return row
			}
		}
		t.Fatalf("no %s %s row", scope, sectionType)
		return nil
	}

	t.Run("default row", func(t *testing.T) {
		if _, err := repo.RestoreDefaults(ctx, "library", &library, DefaultLibrarySectionsForType(&library, "movies")); err != nil {
			t.Fatal(err)
		}
		row := find("library", &library, SectionContinueWatching)
		if got := Resolve([]*PageSection{row}, nil)[0].TitleKey; got != titleKeyContinueWatching {
			t.Fatalf("restored default key = %q", got)
		}
		row.ItemLimit = 40
		if err := repo.Update(ctx, row); err != nil {
			t.Fatal(err)
		}
		if got := find("library", &library, SectionContinueWatching).TitleKey; got != titleKeyContinueWatching {
			t.Fatalf("key after an update that kept the title = %q", got)
		}
		for _, title := range []string{"Keep Watching", titleContinueWatching} {
			row.Title = title
			if err := repo.Update(ctx, row); err != nil {
				t.Fatal(err)
			}
			if got := find("library", &library, SectionContinueWatching); got.TitleKey != "" {
				t.Fatalf("key after renaming to %q = %q", title, got.TitleKey)
			}
		}
	})

	t.Run("generated library row", func(t *testing.T) {
		if _, err := repo.CreateGeneratedHomeLibraryRecentSections(ctx, library, name, "audiobooks"); err != nil {
			t.Fatal(err)
		}
		row := find("home", nil, SectionRecentlyAdded)
		if row.TitleKey != "home.section.recentlyAddedIn" || row.TitleArgs["library"] != name {
			t.Fatalf("generated row key %q args %v", row.TitleKey, row.TitleArgs)
		}
		renamed := name + "-renamed"
		if err := repo.SyncGeneratedHomeLibraryRecentTitles(ctx, library, name, renamed); err != nil {
			t.Fatal(err)
		}
		row = find("home", nil, SectionRecentlyAdded)
		if row.Title != "Recently Added in "+renamed || row.TitleKey != "home.section.recentlyAddedIn" || row.TitleArgs["library"] != renamed {
			t.Fatalf("after library rename: title %q key %q args %v", row.Title, row.TitleKey, row.TitleArgs)
		}
		row.Title = "New audiobooks"
		if err := repo.Update(ctx, row); err != nil {
			t.Fatal(err)
		}
		if row = find("home", nil, SectionRecentlyAdded); row.TitleKey != "" {
			t.Fatalf("renamed generated row key = %q", row.TitleKey)
		}
		// The upgrade backfill keys any row holding a default's text, so a
		// generated row an admin titled "Recently Added" carries that key;
		// renaming the library leaves the admin's title alone.
		if _, err := pool.Exec(ctx, `UPDATE page_sections SET title = $2, title_key = $3 WHERE id = $1`, row.ID, titleRecentlyAdded, defaultTitleKeys[titleRecentlyAdded]); err != nil {
			t.Fatal(err)
		}
		if err := repo.SyncGeneratedHomeLibraryRecentTitles(ctx, library, renamed, name); err != nil {
			t.Fatal(err)
		}
		if row = find("home", nil, SectionRecentlyAdded); row.Title != titleRecentlyAdded || row.TitleKey != defaultTitleKeys[titleRecentlyAdded] {
			t.Fatalf("admin-titled generated row after library rename: title %q key %q", row.Title, row.TitleKey)
		}
	})
}
