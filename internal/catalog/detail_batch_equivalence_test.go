package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// batchEquivFileFetcher is a FileVersionFetcher (and the optional batch
// fileVersionBatchFetcher) backed by one in-memory map. Serving both the
// per-item GetByContentID and the batched ListByContentIDs from the same
// backing data is how the real scanner.FileRepository behaves, without
// pulling the scanner package in (which would create an import cycle).
type batchEquivFileFetcher struct {
	files map[string][]*models.MediaFile
}

func (f *batchEquivFileFetcher) GetByContentID(_ context.Context, contentID string) ([]*models.MediaFile, error) {
	return f.files[contentID], nil
}

func (f *batchEquivFileFetcher) GetByEpisodeID(_ context.Context, _ string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (f *batchEquivFileFetcher) ListByContentIDs(_ context.Context, contentIDs []string) (map[string][]*models.MediaFile, error) {
	out := make(map[string][]*models.MediaFile, len(contentIDs))
	for _, id := range contentIDs {
		if files, ok := f.files[id]; ok {
			out[id] = files
		}
	}
	return out, nil
}

// newTestPool connects to SILO_TEST_DATABASE_URL, skipping when it is unset.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newBatchEquivTestPool(t *testing.T) *pgxpool.Pool {
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
	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.media_items')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check media_items table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied base schema")
	}
	return pool
}

func batchEquivExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed (%s): %v", sql, err)
	}
}

func seedCompletionEpisodes(tb testing.TB, count int) (*EpisodeRepository, string, string) {
	tb.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		tb.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := tb.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(pool.Close)
	prefix := fmt.Sprintf("completion-ids-%d", time.Now().UnixNano())
	seriesID, seasonID := prefix+"-series", prefix+"-season"
	var folderID, otherFolderID int
	for _, id := range []*int{&folderID, &otherFolderID} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`, prefix).Scan(id); err != nil {
			tb.Fatal(err)
		}
	}
	tb.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, seriesID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=ANY($1)`, []int{folderID, otherFolderID})
	})
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id,type,title,genres) VALUES ($1,'series','Completion fixture','{}')`, []any{seriesID}},
		{`INSERT INTO seasons (content_id,series_id,season_number) VALUES ($1,$2,1)`, []any{seasonID, seriesID}},
		{`INSERT INTO episodes (content_id,series_id,season_id,season_number,episode_number,title,overview)
    SELECT $1 || '-episode-' || n,$2,$3,1,n,'Episode ' || n,repeat('Synthetic episode overview. ',80)
    FROM generate_series(1,$4::int) n`, []any{prefix, seriesID, seasonID, count}},
		{`INSERT INTO media_files (content_id,episode_id,media_folder_id,file_path)
    SELECT $1,content_id,$2,content_id || '.mkv' FROM episodes WHERE series_id=$1 AND episode_number>1`, []any{seriesID, folderID}},
		// Multiple folders must not duplicate an episode. Missing files deliberately
		// retain membership and must keep the existing completion behavior.
		{`INSERT INTO media_files (content_id,episode_id,media_folder_id,file_path,missing_since)
    SELECT $1,content_id,$2,content_id || '-other.mkv',now() FROM episodes WHERE series_id=$1 AND episode_number=2`, []any{seriesID, otherFolderID}},
		{`UPDATE media_files SET missing_since=now() WHERE content_id=$1 AND episode_id IN
	    (SELECT content_id FROM episodes WHERE series_id=$1 AND episode_number=3)`, []any{seriesID}},
		{`INSERT INTO episode_libraries (episode_id,media_folder_id)
	    SELECT content_id,$2 FROM episodes WHERE series_id=$1 AND episode_number>1`, []any{seriesID, folderID}},
		{`INSERT INTO episode_libraries (episode_id,media_folder_id)
	    SELECT content_id,$2 FROM episodes WHERE series_id=$1 AND episode_number=2`, []any{seriesID, otherFolderID}},
	}
	for _, s := range statements {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			tb.Fatal(err)
		}
	}
	return NewEpisodeRepository(pool), seriesID, seasonID
}
