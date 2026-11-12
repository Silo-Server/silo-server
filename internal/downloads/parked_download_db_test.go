package downloads

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestParkedDownloadIsNotServedOrPreparedFromItsOldFileDB covers a download
// the scanner parked on a retitle's stand-in: it still names the artifact
// prepared from the file row it left, which now belongs to another title. It
// must neither be served that artifact nor prepare it again, while a download
// on the artifact's own row keeps being served.
func TestParkedDownloadIsNotServedOrPreparedFromItsOldFileDB(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	var standInID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, missing_since, stand_in_for_file_id)
		SELECT media_folder_id, file_path || '.left', NOW(), id FROM media_files WHERE id = $1
		RETURNING id`, fileID).Scan(&standInID); err != nil {
		t.Fatalf("seed stand-in: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE id = $1`, standInID) })
	artifact, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, fmt.Sprintf("parked-%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatalf("queue artifact: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET status = 'ready', file_size = 100, completed_at = NOW() WHERE id = $1`, artifact.ID); err != nil {
		t.Fatalf("mark artifact ready: %v", err)
	}
	missing := time.Now()
	serve := func(file *models.MediaFile, dl *Download) error {
		s := &Service{fileRepo: fakeFileResolver{file: file}, artifacts: NewArtifactManager(repo, nil, nil, nil, "node", nil, nil)}
		_, err := s.resolveDownloadBytesTarget(ctx, dl, catalog.AccessFilter{}, false, false)
		return err
	}

	own := &Download{ID: "own", MediaFileID: fileID, Format: FormatTranscode, ArtifactID: artifact.ID}
	if err := serve(&models.MediaFile{ID: fileID}, own); err != nil {
		t.Fatalf("download on the artifact's own file was refused: %v", err)
	}
	parked := &Download{ID: "parked", MediaFileID: standInID, Format: FormatTranscode, ArtifactID: artifact.ID}
	if err := serve(&models.MediaFile{ID: standInID, MissingSince: &missing}, parked); !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatalf("parked download served its old file's artifact: err = %v, want not found", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET status = 'expired' WHERE id = $1`, artifact.ID); err != nil {
		t.Fatalf("expire artifact: %v", err)
	}
	if _, _, err := repo.PrepareDownloadAgain(ctx, parked); !errors.Is(err, ErrDownloadNotActive) {
		t.Fatalf("preparing a parked download again: err = %v, want not active", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM download_artifacts WHERE id = $1`, artifact.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != ArtifactExpired {
		t.Fatalf("artifact of the retitled row was requeued for a parked download: status %s", status)
	}
}
