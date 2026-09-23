package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestScanFolderMarkerObjectReads counts the object-store reads a first scan
// of a movie library makes for intro/credits markers. The files are sparse
// and zero-filled, so each costs no disk and hashes to its own size.
func TestScanFolderMarkerObjectReads(t *testing.T) {
	const files = 1000
	for _, tc := range []struct {
		name       string
		withMarker bool
		wantGets   int64
	}{
		{name: "empty prefix", wantGets: 0},
		{name: "markers present", withMarker: true, wantGets: files},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newDeadRootTestPool(t)
			ctx := context.Background()
			folderID := seedDeadRootTestFolder(t, pool, "movies", "Marker prefix "+tc.name)
			root := t.TempDir()
			var markedPath string
			for i := range files {
				path := filepath.Join(root, fmt.Sprintf("Marker Movie %04d (2001).mkv", i))
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate(int64(oshashMinFileSize + i))
				if closeErr := f.Close(); err == nil {
					err = closeErr
				}
				if err != nil {
					t.Fatal(err)
				}
				if i == files/2 {
					markedPath = path
				}
			}
			store := newCountingMarkerStore()
			if tc.withMarker {
				hash, err := ComputeOSHash(markedPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Put(ctx, "markers/"+hash+".json", []byte(`{"IntroStart":5,"IntroEnd":65}`)); err != nil {
					t.Fatal(err)
				}
			}

			s := NewScanner(NewFileRepository(pool), "", store, 4, false, 0)
			result, err := s.ScanFolder(ctx, &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if result.New != files {
				t.Fatalf("scan added %d files, want %d", result.New, files)
			}
			gets, lists := store.gets.Load(), store.lists.Load()
			t.Logf("%s: %d new files, %d marker GETs, %d LISTs", tc.name, result.New, gets, lists)
			if gets != tc.wantGets || lists != 1 {
				t.Fatalf("got %d GETs and %d LISTs, want %d GETs and 1 LIST", gets, lists, tc.wantGets)
			}

			if tc.withMarker {
				var introEnd *float64
				var source *string
				if err := pool.QueryRow(ctx,
					`SELECT intro_end, intro_markers_source FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
					folderID, markedPath,
				).Scan(&introEnd, &source); err != nil {
					t.Fatal(err)
				}
				if introEnd == nil || *introEnd != 65 || source == nil || *source != models.MarkerSourceS3 {
					t.Fatalf("marked file intro_end=%v source=%v, want 65 from s3", introEnd, source)
				}
			}
		})
	}
}
