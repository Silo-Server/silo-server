package api

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// TestAdminSubtitleHandlerListsStoredSubtitlesWithoutSubtitleStorageDB: the
// stored-subtitle list reads the database, so a deployment that runs without
// subtitle storage (no S3 public client, therefore no manager) must still be
// wired with its pool. Before this wiring the handler was left pool-less and
// every visit to the admin subtitles page answered 500 in a millisecond without
// reaching Postgres.
func TestAdminSubtitleHandlerListsStoredSubtitlesWithoutSubtitleStorageDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	var userID, folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, "subtitle-list-"+suffix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('movies', $1) RETURNING id`, "subtitle-list-"+suffix).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID) })
	contentID := "subtitle-list-" + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', 'Listed Movie')`, contentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	var fileID int64
	if err := pool.QueryRow(ctx, `INSERT INTO media_files (content_id, media_folder_id, file_path) VALUES ($1, $2, $3) RETURNING id`,
		contentID, folderID, "/media/"+suffix+"/movie.mkv").Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID) })
	if _, err := pool.Exec(ctx, `INSERT INTO downloaded_subtitles
		(media_file_id, provider, language, format, release_name, s3_key, downloaded_by, timing_offset_ms, timing_scale)
		VALUES ($1, 'upload', 'en', 'srt', 'movie.en.srt', $2, $3, 250, 1.02)`,
		fileID, "subtitles/"+suffix+"/en.srt", userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM downloaded_subtitles WHERE media_file_id = $1`, fileID) })

	h := newAdminSubtitleHandler(Dependencies{DB: pool}, subtitles.NewPgRepository(pool, nil), nil)
	if h == nil {
		t.Fatal("newAdminSubtitleHandler returned nil for a database repo")
	}
	page, err := h.ListAdminSubtitlesPage(ctx, handlers.AdminSubtitleListFilter{MediaFileID: int(fileID)}, nil, 25)
	if err != nil {
		t.Fatalf("list stored subtitles: %v", err)
	}
	if page.Total != 1 || page.Uploads != 1 || page.ProviderDownloads != 0 || len(page.Items) != 1 {
		t.Fatalf("page = total %d, uploads %d, provider downloads %d, items %d; want 1, 1, 0, 1",
			page.Total, page.Uploads, page.ProviderDownloads, len(page.Items))
	}
	item := page.Items[0]
	if item.MediaTitle != "Listed Movie" || item.UploaderUsername == "" || item.FilePath == "" {
		t.Fatalf("item = %+v; want the joined media title, uploader and path", item)
	}
	if item.Timing.OffsetMS != 250 || item.Timing.Scale != 1.02 {
		t.Fatalf("timing = %+v; want the stored correction", item.Timing)
	}
}
