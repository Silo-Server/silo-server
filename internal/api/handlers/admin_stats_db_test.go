package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestAdminStatsLeaveOutFilesAndItemsHeldByRemovalGraceDB covers a moved or
// renamed file the scanner could not relink: its old row is marked missing,
// and a title that only that row kept loses its library membership but is
// held by the file removal grace. The dashboard counts only what the library
// shows: titles in a library and files that are present.
//
// The totals are server-wide, so the test seeds and reads in one repeatable
// read transaction: other tests writing the shared database in parallel do
// not move them between the two reads, and the rollback cleans up.
func TestAdminStatsLeaveOutFilesAndItemsHeldByRemovalGraceDB(t *testing.T) {
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
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	before, err := queryAdminCountStats(ctx, tx)
	if err != nil {
		t.Fatalf("stats before seed: %v", err)
	}

	suffix := time.Now().UnixNano()
	movedID := fmt.Sprintf("local-moved-%d", suffix)
	heldID := fmt.Sprintf("local-held-%d", suffix)
	seriesID := fmt.Sprintf("local-held-series-%d", suffix)
	var folderID int
	if err := tx.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("held-stats-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, advisory_age) VALUES
			($1, 'movie', 'Moved', 'unmatched', 12),
			($2, 'movie', 'Held', 'unmatched', 12),
			($3, 'series', 'Held Series', 'unmatched', NULL)`, movedID, heldID, seriesID); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, movedID, folderID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	root := fmt.Sprintf("/held-stats-%d", suffix)
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, missing_since) VALUES
			($1, $4, $5 || '/new/Moved.mkv', 1000, NULL),
			($1, $4, $5 || '/old/Moved.mkv', 1000, NOW() - INTERVAL '1 hour'),
			($2, $4, $5 || '/Held.mkv', 2000, NOW() - INTERVAL '1 hour'),
			($3, $4, $5 || '/Held Series/S01E01.mkv', 4000, NOW() - INTERVAL '1 hour')`,
		movedID, heldID, seriesID, folderID, root); err != nil {
		t.Fatalf("seed files: %v", err)
	}

	after, err := queryAdminCountStats(ctx, tx)
	if err != nil {
		t.Fatalf("stats after seed: %v", err)
	}
	for _, c := range []struct {
		name        string
		got, before int64
		want        int64
	}{
		{"total items", int64(after.TotalItems), int64(before.TotalItems), 1},
		{"total movies", int64(after.TotalMovies), int64(before.TotalMovies), 1},
		{"total shows", int64(after.TotalShows), int64(before.TotalShows), 0},
		{"advisory titles", int64(after.AdvisoryTitles), int64(before.AdvisoryTitles), 1},
		{"total files", int64(after.TotalFiles), int64(before.TotalFiles), 1},
		{"movie files", int64(after.TotalMovieFiles), int64(before.TotalMovieFiles), 1},
		{"show files", int64(after.TotalShowFiles), int64(before.TotalShowFiles), 0},
		{"storage bytes", after.TotalStorageBytes, before.TotalStorageBytes, 1000},
	} {
		if delta := c.got - c.before; delta != c.want {
			t.Errorf("%s grew by %d, want %d", c.name, delta, c.want)
		}
	}
}
