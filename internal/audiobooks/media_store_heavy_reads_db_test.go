package audiobooks

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TestABSCollapsedPagesAndNarrowLookupsDB covers the collapsed library page,
// built and paged in SQL: a series is one entry, standing at its first book in
// the requested order and listing every matching book, and the total counts
// entries. It also covers the single-column cover and author photo lookups,
// and the author view refresh running once per interval.
func TestABSCollapsedPagesAndNarrowLookupsDB(t *testing.T) {
	f := seedABSItemsFixture(t)
	ctx := t.Context()
	store, folderID := f.store, int64(f.folderID)
	all := catalog.AccessFilter{}

	type entry struct {
		id      string
		members []string
	}
	collapsed := func(limit, offset int, q abs.ItemsQuery) ([]entry, int) {
		t.Helper()
		got, total, err := store.ListAudiobooksCollapsed(ctx, folderID, limit, offset, all, q)
		if err != nil {
			t.Fatalf("ListAudiobooksCollapsed: %v", err)
		}
		out := make([]entry, len(got))
		for i, e := range got {
			out[i] = entry{e.Item.ContentID, e.SeriesBookIDs}
		}
		return out, total
	}
	equal := func(a, b []entry) bool {
		return slices.EqualFunc(a, b, func(x, y entry) bool { return x.id == y.id && slices.Equal(x.members, y.members) })
	}
	series := []string{f.bravo, f.alpha} // series order
	if got, total := collapsed(0, 0, abs.ItemsQuery{}); total != 3 || !equal(got, []entry{{f.alpha, series}, {f.charlie, nil}, {f.delta, nil}}) {
		t.Errorf("collapsed by title = %v (total %d), want the series at alpha, then charlie and delta", got, total)
	}
	if got, total := collapsed(2, 1, abs.ItemsQuery{Sort: absSortTitle, Desc: true}); total != 3 || !equal(got, []entry{{f.charlie, nil}, {f.bravo, series}}) {
		t.Errorf("second collapsed page by title desc = %v (total %d), want charlie then the series at bravo", got, total)
	}
	genre := abs.ItemsQuery{Filter: abs.Filter{Kind: abs.FilterGenres, Value: "Fantasy"}}
	if got, total := collapsed(0, 0, genre); total != 2 || !equal(got, []entry{{f.alpha, []string{f.alpha}}, {f.charlie, nil}}) {
		t.Errorf("collapsed Fantasy = %v (total %d), want the series with only its Fantasy book", got, total)
	}

	if _, err := f.store.Pool.Exec(ctx, `UPDATE media_items SET poster_path = 'covers/alpha.webp' WHERE content_id = $1`, f.alpha); err != nil {
		t.Fatal(err)
	}
	if poster, err := store.AudiobookPosterPath(ctx, f.alpha); err != nil || poster != "covers/alpha.webp" {
		t.Errorf("poster = %q (err %v), want the stored path", poster, err)
	}
	if _, err := store.AudiobookPosterPath(ctx, "no-such-book"); !errors.Is(err, abs.ErrNotFound) {
		t.Errorf("poster of a missing book: err = %v, want ErrNotFound", err)
	}
	if _, err := f.store.Pool.Exec(ctx, `UPDATE people SET photo_path = 'people/author.webp' WHERE id = $1`, f.authorID); err != nil {
		t.Fatal(err)
	}
	if photo, err := store.AuthorPhotoPath(ctx, strconv.FormatInt(f.authorID, 10)); err != nil || photo != "people/author.webp" {
		t.Errorf("author photo = %q (err %v), want the stored path", photo, err)
	}
	if _, err := f.store.Pool.Exec(ctx, `UPDATE item_people SET kind = 1 WHERE person_id = $1`, f.authorID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthorPhotoPath(ctx, strconv.FormatInt(f.authorID, 10)); !errors.Is(err, abs.ErrNotFound) {
		t.Errorf("photo of a person who wrote no audiobook: err = %v, want ErrNotFound", err)
	}

	// A refresh within the interval of the last one is skipped.
	if _, err := f.store.Pool.Exec(ctx, `DELETE FROM abs_author_counts_refresh`); err != nil {
		t.Fatal(err)
	}
	refreshedAt := func() time.Time {
		t.Helper()
		var at time.Time
		if err := f.store.Pool.QueryRow(context.Background(), `SELECT refreshed_at FROM abs_author_counts_refresh`).Scan(&at); err != nil {
			t.Fatalf("read refresh time: %v", err)
		}
		return at
	}
	if err := store.RefreshAuthorCounts(ctx, time.Hour); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	first := refreshedAt()
	if err := store.RefreshAuthorCounts(ctx, time.Hour); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if again := refreshedAt(); !again.Equal(first) {
		t.Errorf("refresh within the interval ran again: %v then %v", first, again)
	}
}

// TestABSAuthorCountsRefreshOnOneConnectionPoolDB covers a server whose
// database pool allows one connection (database.max_connections accepts 1):
// the refresh holds that connection for its advisory lock, so its own
// statements must run on it rather than wait for a second.
func TestABSAuthorCountsRefreshOnOneConnectionPoolDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// A zero interval always refreshes, so the run reaches every statement.
	if err := (&ABSMediaStore{Pool: pool}).RefreshAuthorCounts(ctx, 0); err != nil {
		t.Fatalf("refresh on a one-connection pool: %v", err)
	}
}

// TestABSAuthorsLeaveOutABookJustAddedToADisabledLibraryDB covers the authors
// list for a profile that disabled a library: once a book in this library
// also joins the disabled one, the very next request must stop counting its
// author, although the precomputed author view still does.
func TestABSAuthorsLeaveOutABookJustAddedToADisabledLibraryDB(t *testing.T) {
	f := seedABSItemsFixture(t)
	ctx := t.Context()
	store := f.store
	var hidden int
	if err := store.Pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', $1, true) RETURNING id`, f.delta+"-hidden").Scan(&hidden); err != nil {
		t.Fatalf("seed disabled library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, hidden)
	})
	if err := store.RefreshAuthorCounts(ctx, 0); err != nil {
		t.Fatalf("refresh author view: %v", err)
	}
	access := catalog.AccessFilter{DisabledLibraryIDs: []int{hidden}}
	author := strconv.FormatInt(f.authorID, 10)
	listed := func() bool {
		t.Helper()
		authors, _, err := store.ListLibraryAuthors(ctx, int64(f.folderID), 0, 0, "name", false, access)
		if err != nil {
			t.Fatalf("ListLibraryAuthors: %v", err)
		}
		return slices.ContainsFunc(authors, func(a abs.AuthorSummary) bool { return a.ID == author })
	}
	if !listed() {
		t.Fatal("author missing before the book joined the disabled library")
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, f.delta, hidden); err != nil {
		t.Fatalf("add book to disabled library: %v", err)
	}
	if listed() {
		t.Error("author of a book in a disabled library still listed on the next request")
	}
}
