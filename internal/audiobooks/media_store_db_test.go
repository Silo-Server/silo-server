package audiobooks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TestABSStoreHidesBooksWithoutLibraryDB covers a book that has left every
// library but still exists: one held through the file removal grace, or under
// an offline root. ABS author, series, and search reads, and the by-ID reads
// that resume, collection, and playlist listings hydrate through, must not
// return it, even for a viewer without library restrictions. Its progress must
// not use up the progress list's limit either.
func TestABSStoreHidesBooksWithoutLibraryDB(t *testing.T) {
	f := seedHiddenBookFixture(t)
	pool, series, listed, held, authorID, userID := f.pool, f.series, f.listed, f.held, f.authorID, f.userID
	ctx := t.Context()
	store := &ABSMediaStore{Items: catalog.NewItemRepository(pool), Pool: pool}
	unrestricted := catalog.AccessFilter{}
	author, err := store.GetAuthorByID(ctx, strconv.FormatInt(authorID, 10), unrestricted)
	if err != nil {
		t.Fatalf("GetAuthorByID: %v", err)
	}
	seriesBooks, err := store.GetSeriesByName(ctx, series, unrestricted)
	if err != nil {
		t.Fatalf("GetSeriesByName: %v", err)
	}
	found, err := store.SearchAudiobooks(ctx, 0, "Book", 50, unrestricted)
	if err != nil {
		t.Fatalf("SearchAudiobooks: %v", err)
	}
	byID, err := store.GetAudiobooksByIDs(ctx, []string{listed, held}, unrestricted)
	if err != nil {
		t.Fatalf("GetAudiobooksByIDs: %v", err)
	}
	single, err := store.GetAudiobookByID(ctx, held, unrestricted)
	if err != nil {
		t.Fatalf("GetAudiobookByID: %v", err)
	}
	if single != nil {
		t.Errorf("GetAudiobookByID returned held book %s", held)
	}
	progress, err := (&ABSProgressStore{Pool: pool}).ListProgressForAudiobooks(ctx, strconv.Itoa(userID), "", 1)
	if err != nil {
		t.Fatalf("ListProgressForAudiobooks: %v", err)
	}
	if len(progress) != 1 || progress[0].ContentID != listed {
		t.Errorf("newest progress with limit 1 = %+v, want the book in a library", progress)
	}
	searched := found[:0]
	for _, item := range found {
		if item.ContentID == listed || item.ContentID == held {
			searched = append(searched, item)
		}
	}
	for name, got := range map[string]int{
		"author books": len(author.Books), "series books": len(seriesBooks.Books), "search results": len(searched), "by-ID books": len(byID),
	} {
		if got != 1 {
			t.Errorf("%s = %d, want only the book in a library", name, got)
		}
	}
	listedBooks := append(append(author.Books, seriesBooks.Books...), searched...)
	for _, book := range byID {
		listedBooks = append(listedBooks, book)
	}
	for _, book := range listedBooks {
		if book.ContentID == held {
			t.Errorf("held book %s listed", held)
		}
	}
}

// hiddenBookFixture is one audiobook in a library and one in none, sharing an
// author and a series, with the out-of-library book's progress the newest.
type hiddenBookFixture struct {
	pool         *pgxpool.Pool
	authorID     int64
	series       string
	listed, held string
	userID       int
}

func seedHiddenBookFixture(t *testing.T) hiddenBookFixture {
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
	authorID := suffix % 1_000_000_000_000
	series := fmt.Sprintf("Hidden Series %d", suffix)
	listed := fmt.Sprintf("abs-listed-%d", suffix)
	held := fmt.Sprintf("abs-held-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', $1, true) RETURNING id`,
		fmt.Sprintf("abs-hidden-%d", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{listed, held})
		_, _ = pool.Exec(ctx, `DELETE FROM people WHERE id = $1`, authorID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username = $1`, fmt.Sprintf("abs-hidden-%d", suffix))
	})
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO people (id, name) VALUES ($1, $2)`, []any{authorID, fmt.Sprintf("Hidden Author %d", suffix)}},
		{`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'audiobook', 'Listed Book', 'local', '{}'::text[]), ($2, 'audiobook', 'Held Book', 'local', '{}'::text[])`, []any{listed, held}},
		{`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, []any{listed, folderID}},
		{`INSERT INTO item_people (id, content_id, person_id, kind) VALUES ($1, $2, $3, 7), ($4, $5, $3, 7)`, []any{authorID, listed, authorID, authorID + 1, held}},
		{`INSERT INTO audiobook_series (content_id, series_name, series_index) VALUES ($1, $3, 1), ($2, $3, 2)`, []any{listed, held, series}},
	}
	for _, stmt := range stmts {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("abs-hidden-%d", suffix)).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at)
		VALUES ($1, '', $2, 60, 3600, now() - interval '1 hour'), ($1, '', $3, 60, 3600, now())`, userID, listed, held); err != nil {
		t.Fatalf("seed progress: %v", err)
	}
	return hiddenBookFixture{pool: pool, authorID: authorID, series: series, listed: listed, held: held, userID: userID}
}
