package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUpsertInvalidatesMarkersOnlyForKnownFileReplacement(t *testing.T) {
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
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('tv', $1, true)
		RETURNING id
	`, fmt.Sprintf("Marker replacement test %d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	repo := NewFileRepository(pool)
	seed := func(t *testing.T, name, hash string) models.MediaFile {
		t.Helper()
		file := models.MediaFile{
			MediaFolderID: folderID,
			FilePath:      fmt.Sprintf("/tmp/marker-replacement-%d/%s.mkv", suffix, name),
			FileSize:      1024,
			FileHash:      hash,
			Duration:      3600,
		}
		stored, err := repo.Upsert(ctx, file)
		if err != nil {
			t.Fatalf("seed file: %v", err)
		}
		provider := "plugin:marker-test"
		confidence := 0.9
		algorithm := "marker-test:v1"
		startIntro, endIntro := 30.0, 90.0
		startCredits, endCredits := 3500.0, 3600.0
		startRecap, endRecap := 0.0, 25.0
		startPreview, endPreview := 3570.0, 3590.0
		if wrote, err := repo.UpsertMarkers(ctx, stored.ID, MarkerUpdate{
			IntroStart:        &startIntro,
			IntroEnd:          &endIntro,
			CreditsStart:      &startCredits,
			CreditsEnd:        &endCredits,
			RecapStart:        &startRecap,
			RecapEnd:          &endRecap,
			PreviewStart:      &startPreview,
			PreviewEnd:        &endPreview,
			MarkersSource:     models.MarkerSourcePlugin,
			MarkersProvider:   &provider,
			MarkersConfidence: &confidence,
			MarkersAlgorithm:  algorithm,
		}); err != nil || !wrote {
			t.Fatalf("seed markers = (%v, %v), want write", wrote, err)
		}
		return file
	}

	markersPresent := func(file *models.MediaFile) bool {
		return file.IntroStart != nil && file.IntroEnd != nil &&
			file.CreditsStart != nil && file.CreditsEnd != nil &&
			file.RecapStart != nil && file.RecapEnd != nil &&
			file.PreviewStart != nil && file.PreviewEnd != nil &&
			file.MarkersSource != nil && file.MarkersConfidence != nil &&
			file.IntroMarkersSource != nil && file.IntroMarkersProvider != nil && file.IntroMarkersConfidence != nil && file.IntroMarkersAlgorithm != nil && file.IntroMarkersDetectedAt != nil &&
			file.CreditsMarkersSource != nil && file.CreditsMarkersProvider != nil && file.CreditsMarkersConfidence != nil && file.CreditsMarkersAlgorithm != nil && file.CreditsMarkersDetectedAt != nil &&
			file.RecapMarkersSource != nil && file.RecapMarkersProvider != nil && file.RecapMarkersConfidence != nil && file.RecapMarkersAlgorithm != nil && file.RecapMarkersDetectedAt != nil &&
			file.PreviewMarkersSource != nil && file.PreviewMarkersProvider != nil && file.PreviewMarkersConfidence != nil && file.PreviewMarkersAlgorithm != nil && file.PreviewMarkersDetectedAt != nil
	}
	markersCleared := func(file *models.MediaFile) bool {
		return file.IntroStart == nil && file.IntroEnd == nil &&
			file.CreditsStart == nil && file.CreditsEnd == nil &&
			file.RecapStart == nil && file.RecapEnd == nil &&
			file.PreviewStart == nil && file.PreviewEnd == nil &&
			file.MarkersSource == nil && file.MarkersConfidence == nil &&
			file.IntroMarkersSource == nil && file.IntroMarkersProvider == nil && file.IntroMarkersConfidence == nil && file.IntroMarkersAlgorithm == nil && file.IntroMarkersDetectedAt == nil &&
			file.CreditsMarkersSource == nil && file.CreditsMarkersProvider == nil && file.CreditsMarkersConfidence == nil && file.CreditsMarkersAlgorithm == nil && file.CreditsMarkersDetectedAt == nil &&
			file.RecapMarkersSource == nil && file.RecapMarkersProvider == nil && file.RecapMarkersConfidence == nil && file.RecapMarkersAlgorithm == nil && file.RecapMarkersDetectedAt == nil &&
			file.PreviewMarkersSource == nil && file.PreviewMarkersProvider == nil && file.PreviewMarkersConfidence == nil && file.PreviewMarkersAlgorithm == nil && file.PreviewMarkersDetectedAt == nil
	}

	t.Run("same hash preserves markers", func(t *testing.T) {
		file := seed(t, "same", "hash-same")
		updated, err := repo.Upsert(ctx, file)
		if err != nil {
			t.Fatalf("rescan same generation: %v", err)
		}
		if !markersPresent(updated) {
			t.Fatalf("same-generation rescan cleared markers: %+v", updated)
		}
	})

	t.Run("hash backfill preserves markers", func(t *testing.T) {
		file := seed(t, "backfill", "")
		file.FileHash = "hash-backfilled"
		updated, err := repo.Upsert(ctx, file)
		if err != nil {
			t.Fatalf("backfill hash: %v", err)
		}
		if !markersPresent(updated) {
			t.Fatalf("hash backfill cleared markers: %+v", updated)
		}
	})

	t.Run("different hashes clear markers", func(t *testing.T) {
		file := seed(t, "replaced", "hash-before")
		file.FileHash = "hash-after"
		file.FileSize = 2048
		updated, err := repo.Upsert(ctx, file)
		if err != nil {
			t.Fatalf("replace file generation: %v", err)
		}
		if !markersCleared(updated) {
			t.Fatalf("replacement retained stale markers: %+v", updated)
		}
	})
}
