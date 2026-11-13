package scanner

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestMatchLinkRefusedAfterRetitleDB checks that a match's link write made
// from a snapshot of a file read before a rename retitled the row is refused:
// the row stays unlinked at its new path, so its rematch decides its title.
// The movie worker can hold such a snapshot for minutes (it loads a claimed
// batch up front), and the scanner's re-enqueue cannot stop it.
func TestMatchLinkRefusedAfterRetitleDB(t *testing.T) {
	pool := newScannerTestPool(t)
	base := 10_000_000 + time.Now().UnixNano()%800_000*100
	cases := []struct {
		name, folderType, from, to string
	}{
		{"movie", "movies", "Movie (2020)/Movie (2020).mkv", "Other Film (2011)/Other Film (2011).mkv"},
		{"series episode", "series", "Show/Season 1/Show S01E01.mkv", "Other Show/Season 1/Other Show S01E01.mkv"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			root := t.TempDir()
			folderID := seedDeadRootTestFolder(t, pool, tc.folderType, fmt.Sprintf("link-fence-%d", time.Now().UnixNano()))
			folder := &models.MediaFolder{ID: folderID, Type: tc.folderType, Paths: []string{root}}
			repo := NewFileRepository(pool)
			s := NewScanner(repo, "", nil, 2, false, time.Hour)
			s.SetMetadataQueueProducer(&relinkQueue{})

			from, to := filepath.Join(root, tc.from), filepath.Join(root, tc.to)
			writeRelinkTestMedia(t, from, 1)
			// Another title keeps the library from walking empty.
			writeRelinkTestMedia(t, filepath.Join(root, "Keeper (2010)", "Keeper (2010).mkv"), 9)
			if _, err := s.ScanFolder(ctx, folder); err != nil {
				t.Fatalf("first scan: %v", err)
			}
			original, err := repo.GetByPath(ctx, from)
			if err != nil {
				t.Fatal(err)
			}
			// A matched title holds the row, as a series job's episode linker
			// reads it: linked to the show, no episode yet.
			contentID, episodeID := seedRelinkItem(t, pool, tc.folderType, strconv.FormatInt(base+int64(i)*3+1, 10))
			if _, err := pool.Exec(ctx, `UPDATE media_files SET content_id = $2 WHERE id = $1`, original.ID, contentID); err != nil {
				t.Fatal(err)
			}
			snapshot, err := repo.GetByID(ctx, original.ID)
			if err != nil {
				t.Fatal(err)
			}

			relocateRelinkFile(t, s, folder, from, to, relinkCase{})
			moved, err := repo.GetByPath(ctx, to)
			if err != nil {
				t.Fatalf("GetByPath(new path): %v", err)
			}
			if moved.ID != snapshot.ID || moved.ContentID != "" {
				t.Fatalf("rename did not retitle the row: id %d (was %d), content %q", moved.ID, snapshot.ID, moved.ContentID)
			}

			if err := repo.UpdateContentID(ctx, snapshot.ID, snapshot.FilePath, contentID); !errors.Is(err, ErrFileMoved) {
				t.Fatalf("stale content link = %v, want ErrFileMoved", err)
			}
			if episodeID != "" {
				if err := repo.UpdateEpisodeLink(ctx, snapshot.ID, snapshot.FilePath, episodeID, 1, 1); !errors.Is(err, ErrFileMoved) {
					t.Fatalf("stale episode link = %v, want ErrFileMoved", err)
				}
			}
			after, err := repo.GetByID(ctx, snapshot.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.FilePath != to || after.ContentID != "" || after.EpisodeID != "" {
				t.Fatalf("row after stale links: path %q content %q episode %q; want %q, unlinked", after.FilePath, after.ContentID, after.EpisodeID, to)
			}
		})
	}
}
