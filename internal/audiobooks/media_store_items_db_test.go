package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// absItemsFixture is four audiobooks in one library: alpha and bravo in one
// series spelled two ways, one author on delta, and progress for profiles "a"
// and "b" on one account.
type absItemsFixture struct {
	store                        *ABSMediaStore
	folderID                     int
	uid                          string
	alpha, bravo, charlie, delta string
	series                       string
	authorID                     int64
	suffix                       int64
	now                          time.Time
}

func seedABSItemsFixture(t *testing.T) absItemsFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	name := fmt.Sprintf("abs-items-%d", suffix)
	id := func(s string) string { return fmt.Sprintf("%s-%s", name, s) }
	alpha, bravo, charlie, delta := id("alpha"), id("bravo"), id("charlie"), id("delta")
	books := []string{alpha, bravo, charlie, delta}
	series := fmt.Sprintf("Zq Saga %d", suffix)
	authorID := suffix % 1_000_000_000_000

	var folderID, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', $1, true) RETURNING id`, name).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, name).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, books)
		_, _ = pool.Exec(ctx, `DELETE FROM people WHERE id = $1`, authorID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	now := time.Now()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		// Titles sort alpha < bravo < charlie < delta; the added dates run the
		// other way, so the addedAt sort is distinguishable from title order.
		{`INSERT INTO media_items (content_id, type, title, status, genres, year, original_language) VALUES
			($1, 'audiobook', 'Alpha', 'local', '{Fantasy}', 2001, 'fr'),
			($2, 'audiobook', 'Bravo', 'local', '{Mystery}', 1999, ''),
			($3, 'audiobook', 'Charlie', 'local', '{Fantasy,Mystery}', 2010, ''),
			($4, 'audiobook', 'Delta', 'local', '{}', 0, '')`, []any{alpha, bravo, charlie, delta}},
		{`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
			SELECT b, $2, $3::timestamptz - (ord * interval '1 hour') FROM unnest($1::text[]) WITH ORDINALITY AS t(b, ord)`, []any{books, folderID, now}},
		{`INSERT INTO people (id, name) VALUES ($1, $2)`, []any{authorID, fmt.Sprintf("Quillon Vexbury %d", suffix)}},
		{`INSERT INTO item_people (id, content_id, person_id, kind) VALUES ($1, $2, $1, 7)`, []any{authorID, delta}},
		// One series spelled two ways.
		{`INSERT INTO audiobook_series (content_id, series_name, series_index) VALUES ($1, $3, 2), ($2, lower($3), 1)`, []any{alpha, bravo, series}},
		// Profile "a" is listening to alpha and finished bravo; profile "b"
		// on the same account is listening to charlie.
		{`INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at) VALUES
			($1, 'a', $2, 60, 3600, false, now()),
			($1, 'a', $3, 0, 3600, true, now() - interval '1 hour'),
			($1, 'b', $4, 60, 3600, false, now())`, []any{userID, alpha, bravo, charlie}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	return absItemsFixture{
		store: &ABSMediaStore{Items: catalog.NewItemRepository(pool), Pool: pool}, folderID: folderID, uid: strconv.Itoa(userID),
		alpha: alpha, bravo: bravo, charlie: charlie, delta: delta, series: series, authorID: authorID, suffix: suffix, now: now,
	}
}

// TestABSLibraryItemsFiltersAndSortDB covers the ABS library items query:
// every filter kind returns the matching books, progress filters read only the
// viewer's profile, and sort and desc order the page.
func TestABSLibraryItemsFiltersAndSortDB(t *testing.T) {
	f := seedABSItemsFixture(t)
	ctx := t.Context()
	store, folderID, uid, series := f.store, f.folderID, f.uid, f.series
	alpha, bravo, charlie, delta := f.alpha, f.bravo, f.charlie, f.delta
	all := catalog.AccessFilter{}
	books := []string{alpha, bravo, charlie, delta}
	list := func(q abs.ItemsQuery) []string {
		t.Helper()
		q.UserID, q.ProfileID = uid, "a"
		items, total, err := store.ListAudiobooks(ctx, int64(folderID), 0, 0, all, q)
		if err != nil {
			t.Fatalf("ListAudiobooks(%+v): %v", q, err)
		}
		got := contentIDsOf(items)
		if total != len(got) {
			t.Errorf("ListAudiobooks(%+v) total = %d, want %d", q, total, len(got))
		}
		return got
	}
	filter := func(kind abs.FilterKind, value string) abs.ItemsQuery {
		return abs.ItemsQuery{Filter: abs.Filter{Kind: kind, Value: value}}
	}

	for _, tc := range []struct {
		name string
		q    abs.ItemsQuery
		want []string
	}{
		{"no filter", abs.ItemsQuery{}, books},
		{"genre", filter(abs.FilterGenres, "Fantasy"), []string{alpha, charlie}},
		{"language", filter(abs.FilterLanguages, "fr"), []string{alpha}},
		{"tags match nothing", filter(abs.FilterTags, "Fantasy"), nil},
		{"in progress", filter(abs.FilterProgress, abs.ProgressInProgress), []string{alpha}},
		{"finished", filter(abs.FilterProgress, abs.ProgressFinished), []string{bravo}},
		{"not finished", filter(abs.FilterProgress, abs.ProgressNotFinished), []string{alpha, charlie, delta}},
		{"not started", filter(abs.FilterProgress, abs.ProgressNotStarted), []string{charlie, delta}},
		{"series by ID", filter(abs.FilterSeries, abs.SeriesID(series)), []string{alpha, bravo}},
		{"no series", filter(abs.FilterSeries, abs.SentinelNoSeries), []string{charlie, delta}},
		{"title desc", abs.ItemsQuery{Sort: absSortTitle, Desc: true}, []string{delta, charlie, bravo, alpha}},
		{"added desc", abs.ItemsQuery{Sort: "addedAt", Desc: true}, []string{alpha, bravo, charlie, delta}},
		{"added asc", abs.ItemsQuery{Sort: "addedAt"}, []string{delta, charlie, bravo, alpha}},
		{"published year", abs.ItemsQuery{Sort: "media.metadata.publishedYear"}, []string{bravo, alpha, charlie, delta}},
		{"sequence in series", abs.ItemsQuery{Filter: abs.Filter{Kind: abs.FilterSeries, Value: series}, Sort: "sequence"}, []string{bravo, alpha}},
		{"progress", abs.ItemsQuery{Sort: absSortProgress, Desc: true}, []string{alpha, bravo, charlie, delta}},
		{"author name", abs.ItemsQuery{Sort: "media.metadata.authorNameLF"}, []string{delta, alpha, bravo, charlie}},
		{"unsupported sort", abs.ItemsQuery{Sort: "size", Desc: true}, books},
	} {
		if got := list(tc.q); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}

	// Without a route library, a copy in a library the viewer cannot see does
	// not decide where a book sorts. Alpha joined a hidden library long ago.
	var hiddenFolder int
	if err := store.Pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', $1, true) RETURNING id`, alpha).Scan(&hiddenFolder); err != nil {
		t.Fatalf("seed hidden folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, hiddenFolder)
	})
	if _, err := store.Pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, now() - interval '10 years')`, alpha, hiddenFolder); err != nil {
		t.Fatalf("seed hidden membership: %v", err)
	}
	visible := catalog.AccessFilter{AllowedLibraryIDs: []int{folderID}}
	oldest, _, err := store.ListAudiobooks(ctx, 0, 0, 0, visible, abs.ItemsQuery{Sort: "addedAt"})
	if got := contentIDsOf(oldest); err != nil || !slices.Equal(got, []string{delta, charlie, bravo, alpha}) {
		t.Errorf("added order across the viewer's libraries = %v (err %v), want delta first and alpha by its visible date", got, err)
	}

	page, _, err := store.ListAudiobooks(ctx, int64(folderID), 1, 0, all, abs.ItemsQuery{Sort: "addedAt", Desc: true})
	if err != nil || len(page) != 1 || page[0].AddedAt == nil || !page[0].AddedAt.Equal(f.now.Add(-time.Hour).Truncate(time.Microsecond)) {
		t.Errorf("newest book added date = %+v (err %v), want %v", page, err, f.now.Add(-time.Hour))
	}
}

// TestABSSeriesIdentityAndContinueListeningDB covers a series spelled two
// ways being one series in the list, detail, and search, visible only through
// books the viewer may see, and Continue Listening reading only the viewer's
// profile.
func TestABSSeriesIdentityAndContinueListeningDB(t *testing.T) {
	f := seedABSItemsFixture(t)
	ctx := t.Context()
	store, folderID, uid, series := f.store, f.folderID, f.uid, f.series
	alpha, bravo := f.alpha, f.bravo
	all := catalog.AccessFilter{}

	continuing, err := store.ListContinueListening(ctx, uid, "a", int64(folderID), 10, all)
	if err != nil || !slices.Equal(contentIDsOf(continuing), []string{alpha}) {
		t.Errorf("continue listening for profile a = %v (err %v), want only its own book", contentIDsOf(continuing), err)
	}
	// An empty profile ID reads the account's profile-less rows, not every
	// profile's.
	if continuing, err := store.ListContinueListening(ctx, uid, "", int64(folderID), 10, all); err != nil || len(continuing) != 0 {
		t.Errorf("continue listening without a profile = %v (err %v), want none", contentIDsOf(continuing), err)
	}

	got, err := store.GetSeriesByName(ctx, abs.SeriesID(series), all)
	if err != nil || got.ID != abs.SeriesID(series) || !slices.Equal(contentIDsOf(got.Books), []string{bravo, alpha}) {
		t.Errorf("series detail = %+v (err %v), want both spellings in sequence order", got, err)
	}
	listed, _, err := store.ListLibrarySeries(ctx, int64(folderID), 0, 0, all)
	if err != nil || len(listed) != 1 || listed[0].ID != abs.SeriesID(series) || listed[0].NumBooks != 2 {
		t.Errorf("series list = %+v (err %v), want one series of two books", listed, err)
	}
	hidden := catalog.AccessFilter{AllowedLibraryIDs: []int{-1}}
	if _, err := store.GetSeriesByName(ctx, series, hidden); !errors.Is(err, abs.ErrNotFound) {
		t.Errorf("series detail outside the viewer's libraries: err = %v, want ErrNotFound", err)
	}

	foundSeries, err := store.SearchLibrarySeries(ctx, int64(folderID), fmt.Sprintf("saga %d", f.suffix), 5, all)
	if err != nil || len(foundSeries) != 1 || foundSeries[0].NumBooks != 2 {
		t.Errorf("series search = %+v (err %v), want the series once", foundSeries, err)
	}
	foundAuthors, err := store.SearchLibraryAuthors(ctx, int64(folderID), "vexbury", 5, all)
	if err != nil || len(foundAuthors) != 1 || foundAuthors[0].ID != strconv.FormatInt(f.authorID, 10) {
		t.Errorf("author search = %+v (err %v), want the author", foundAuthors, err)
	}
	if found, err := store.SearchLibraryAuthors(ctx, int64(folderID), "vexbury", 5, hidden); err != nil || len(found) != 0 {
		t.Errorf("author search outside the viewer's libraries = %+v (err %v), want none", found, err)
	}
}

func contentIDsOf(items []*models.MediaItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ContentID)
	}
	return out
}
