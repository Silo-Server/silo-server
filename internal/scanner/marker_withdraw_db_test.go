package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// WithdrawScannerMarker clears only the scanner result of the named
// algorithm, leaves the file's other segments alone, and refuses a file
// whose identity changed.
func TestWithdrawScannerMarkerPostgres(t *testing.T) {
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
	var folderID, fileID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('series', 'Marker withdrawal test') RETURNING id`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		if _, err := pool.Exec(cleanup, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, folderID); err != nil {
			t.Error(err)
		}
	})
	if err := pool.QueryRow(ctx, `INSERT INTO media_files (media_folder_id, file_path, duration, file_hash, file_size) VALUES ($1, $2, 1500, 'cut', 1500) RETURNING id`, folderID, fmt.Sprintf("/marker-withdraw-%d.mkv", time.Now().UnixNano())).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	repo := NewFileRepository(pool)
	read := func() *models.MediaFile {
		t.Helper()
		file, err := repo.GetByID(ctx, fileID)
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	write := func(source, algorithm string) {
		t.Helper()
		confidence := 0.95
		update := MarkerUpdate{
			MarkersSource: source, MarkersConfidence: &confidence, MarkersAlgorithm: algorithm,
			IntroStart: new(10.0), IntroEnd: new(60.0), CreditsStart: new(1410.0), CreditsEnd: new(1500.0),
		}
		if wrote, err := repo.UpsertMarkers(ctx, fileID, update); err != nil || !wrote {
			t.Fatalf("write %s %s credits: wrote=%v err=%v", source, algorithm, wrote, err)
		}
	}
	withdraw := func(algorithm string, expected *models.MediaFile) bool {
		t.Helper()
		withdrawn, err := repo.WithdrawScannerMarker(ctx, fileID, "credits", algorithm, expected)
		if err != nil {
			t.Fatalf("withdraw %s: %v", algorithm, err)
		}
		return withdrawn
	}

	write(models.MarkerSourceScanner, "credits-chapter:v1")
	if withdraw("credits-audio:v1", read()) {
		t.Fatal("withdrew credits another detector's algorithm names")
	}
	if file := read(); file.CreditsStart == nil {
		t.Fatal("a refused withdrawal cleared the credits")
	}
	stale := read()
	stale.FileHash = "other-cut"
	if _, err := repo.WithdrawScannerMarker(ctx, fileID, "credits", "credits-chapter:v1", stale); !errors.Is(err, ErrStaleMarkerUpdate) {
		t.Fatalf("withdraw for another file identity: err=%v, want ErrStaleMarkerUpdate", err)
	}
	if !withdraw("credits-chapter:v1", read()) {
		t.Fatal("did not withdraw the scanner chapter credits")
	}
	file := read()
	if file.CreditsStart != nil || file.CreditsEnd != nil || file.CreditsMarkersAlgorithm != nil {
		t.Fatalf("credits after withdrawal = %v-%v %v, want none", file.CreditsStart, file.CreditsEnd, file.CreditsMarkersAlgorithm)
	}
	if file.IntroStart == nil || *file.IntroStart != 10 {
		t.Fatalf("intro after credits withdrawal = %v, want it kept", file.IntroStart)
	}
	for _, segment := range file.MarkerSegments {
		if segment.Kind == "credits" {
			t.Fatalf("credits occurrence %+v left after withdrawal", segment)
		}
	}

	// A manual edit outranks the scanner result it replaces.
	write(models.MarkerSourceManual, "credits-chapter:v1")
	if withdraw("credits-chapter:v1", read()) {
		t.Fatal("withdrew manual credits")
	}
}
