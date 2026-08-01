package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestReplaceVirtualCandidatesIsScopedToLibrary(t *testing.T) {
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

	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("virtual-candidate-scope-%d", suffix)
	basePath := fmt.Sprintf("virtual://movie/tt%d?profile=1080p", suffix)
	firstPath := basePath + "&result=first"
	secondPath := basePath + "&result=second"
	var folderA, folderB int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders(type,name,enabled)
		VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("Candidate A %d", suffix)).Scan(&folderA); err != nil {
		t.Fatalf("seed first folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders(type,name,enabled)
		VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("Candidate B %d", suffix)).Scan(&folderB); err != nil {
		t.Fatalf("seed second folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE content_id=$1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_item_libraries WHERE content_id=$1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=$1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=ANY($1::int[])`, []int{folderA, folderB})
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items(content_id,type,title,status,genres)
		VALUES($1,'movie','Candidate Scope','matched','{}'::text[])`, contentID); err != nil {
		t.Fatalf("seed candidate item: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries(content_id,media_folder_id)
		VALUES($1,$2),($1,$3)`, contentID, folderA, folderB); err != nil {
		t.Fatalf("seed candidate library links: %v", err)
	}

	repo := NewFileRepository(pool)
	source := func(folderID int) *models.MediaFile {
		return &models.MediaFile{
			ContentID:                  contentID,
			MediaFolderID:              folderID,
			FilePath:                   basePath,
			VirtualOwnerInstallationID: 91,
		}
	}
	first := []VirtualCandidate{{URI: firstPath, Label: "1080p"}}
	if err := repo.ReplaceVirtualCandidates(ctx, source(folderA), first); err != nil {
		t.Fatalf("replace first-library candidates: %v", err)
	}
	if err := repo.ReplaceVirtualCandidates(ctx, source(folderB), first); err != nil {
		t.Fatalf("replace second-library candidates: %v", err)
	}
	if err := repo.ReplaceVirtualCandidates(ctx, source(folderA), []VirtualCandidate{{URI: secondPath, Label: "1080p"}}); err != nil {
		t.Fatalf("refresh first-library candidates: %v", err)
	}

	var firstA, firstB, secondA int
	if err := pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER(WHERE media_folder_id=$2 AND file_path=$4),
		  count(*) FILTER(WHERE media_folder_id=$3 AND file_path=$4),
		  count(*) FILTER(WHERE media_folder_id=$2 AND file_path=$5)
		FROM media_files
		WHERE content_id=$1 AND virtual_owner_installation_id=91`,
		contentID, folderA, folderB, firstPath, secondPath,
	).Scan(&firstA, &firstB, &secondA); err != nil {
		t.Fatalf("inspect library-scoped candidates: %v", err)
	}
	if firstA != 0 || firstB != 1 || secondA != 1 {
		t.Fatalf("candidate rows firstA=%d firstB=%d secondA=%d, want 0/1/1", firstA, firstB, secondA)
	}
}
