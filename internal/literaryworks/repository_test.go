package literaryworks

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestRepositoryLinkAndFetchSummary(t *testing.T) {
	ctx := context.Background()
	pool := newLiteraryWorksTestPool(t)
	suffix := time.Now().UnixNano()
	workID := fmt.Sprintf("work-test-%d", suffix)
	ebookID := fmt.Sprintf("ebook-test-%d", suffix)
	audioID := fmt.Sprintf("audio-test-%d", suffix)
	folderID := seedLiteraryFolder(t, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{ebookID, audioID})
		_, _ = pool.Exec(ctx, `DELETE FROM literary_works WHERE work_id = $1`, workID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	seedLiteraryMediaItem(t, pool, ebookID, FormatEbook, "Project Hail Mary", folderID)
	seedLiteraryMediaItem(t, pool, audioID, FormatAudiobook, "Project Hail Mary", folderID)

	repo := NewRepository(pool)
	work, err := repo.CreateWork(ctx, CreateWorkParams{
		WorkID:           workID,
		CanonicalTitle:   "Project Hail Mary",
		NormalizedTitle:  "project hail mary",
		PrimaryAuthorKey: "andy weir",
	})
	if err != nil {
		t.Fatal(err)
	}
	if work.WorkID != workID {
		t.Fatalf("work id = %q, want %q", work.WorkID, workID)
	}
	if err := repo.LinkItems(ctx, workID, []LinkItemParams{
		{ContentID: ebookID, FormatType: FormatEbook, LinkSource: LinkManual, Confidence: 1},
		{ContentID: audioID, FormatType: FormatAudiobook, LinkSource: LinkManual, Confidence: 1},
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := repo.GetSummaryForContentID(ctx, ebookID, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil || summary.WorkID != workID || len(summary.Formats) != 2 {
		t.Fatalf("summary = %#v, want work with two formats", summary)
	}
}

func newLiteraryWorksTestPool(t *testing.T) *pgxpool.Pool {
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
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.literary_works')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check literary_works table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied literary works migration")
	}
	return pool
}

func seedLiteraryFolder(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('books', 'Literary Works Test', true)
		RETURNING id
	`).Scan(&id); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	return id
}

func seedLiteraryMediaItem(t *testing.T, pool *pgxpool.Pool, contentID, mediaType, title string, folderID int) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres)
		VALUES ($1, $2, $3, '{}'::text[])
	`, contentID, mediaType, title); err != nil {
		t.Fatalf("seed media item %s: %v", contentID, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id)
		VALUES ($1, $2)
	`, contentID, folderID); err != nil {
		t.Fatalf("seed media item library %s: %v", contentID, err)
	}
}
