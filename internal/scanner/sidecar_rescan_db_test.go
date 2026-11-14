package scanner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A rescan that cannot list a video's sidecars must not drop the ones it
// could not see, whichever write the rest of the scan needs. A sidecar the
// scan can confirm is gone is still dropped.
func TestRescanKeepsSidecarsItCouldNotListDB(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny access")
	}
	cases := []struct {
		name string
		// rescan changes what else the rescan must write and returns the
		// ffprobe it runs with.
		rescan func(t *testing.T, moviePath string) string
	}{
		{
			// The video changed, so the scan rewrites the whole row.
			name: "full rewrite",
			rescan: func(t *testing.T, moviePath string) string {
				later := time.Now().Add(time.Hour)
				if err := os.Chtimes(moviePath, later, later); err != nil {
					t.Fatal(err)
				}
				return ""
			},
		},
		{
			// The row needs a probe repair that fails, so the scan writes
			// only the identity and sidecar columns.
			name: "identity and sidecars only",
			rescan: func(t *testing.T, _ string) string {
				ffprobe := filepath.Join(t.TempDir(), "ffprobe")
				writeFakeTool(t, ffprobe, "#!/bin/sh\necho 'movie.mkv: Input/output error' >&2\nexit 1\n")
				return ffprobe
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			pool := newScannerTestPool(t)

			root := t.TempDir()
			movieDir := filepath.Join(root, "Heat (1995)")
			moviePath := filepath.Join(movieDir, "Heat (1995).mkv")
			beside := filepath.Join(movieDir, "Heat (1995).en.srt")
			removed := filepath.Join(movieDir, "Heat (1995).fr.srt")
			subsDir := filepath.Join(movieDir, "Subs")
			inSubs := filepath.Join(subsDir, "Heat (1995).de.srt")
			writeTestFile(t, moviePath, "movie")
			for _, path := range []string{beside, removed, inSubs} {
				writeTestFile(t, path, "1\n00:00:01,000 --> 00:00:02,000\nHi\n")
			}

			var folderID int
			if err := pool.QueryRow(ctx, `
				INSERT INTO media_folders (type, name, enabled)
				VALUES ('movies', 'Sidecar Rescan Test', true)
				RETURNING id
			`).Scan(&folderID); err != nil {
				t.Fatalf("seed folder: %v", err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`, folderID, root); err != nil {
				t.Fatalf("seed folder path: %v", err)
			}
			t.Cleanup(func() {
				_ = os.Chmod(subsDir, 0o755)
				cleanupCtx := context.WithoutCancel(ctx)
				_, _ = pool.Exec(cleanupCtx, `
					DELETE FROM media_items WHERE content_id IN (
						SELECT content_id FROM media_files WHERE media_folder_id = $1
					)`, folderID)
				_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_folders WHERE id = $1`, folderID)
			})
			folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}, Enabled: true}
			repo := NewFileRepository(pool)

			if _, err := NewScanner(repo, "", nil, 1, false, 0).ScanFolder(ctx, folder); err != nil {
				t.Fatalf("first scan: %v", err)
			}
			if got := storedSidecarPaths(t, repo, moviePath); !slices.Equal(got, sortedPaths(beside, removed, inSubs)) {
				t.Fatalf("first scan stored sidecars %v", got)
			}

			// The Subs/ listing fails and a sidecar beside the video is gone.
			if err := os.Remove(removed); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(subsDir, 0o000); err != nil {
				t.Fatal(err)
			}
			ffprobe := tc.rescan(t, moviePath)
			if _, err := NewScanner(repo, ffprobe, nil, 1, false, 0).ScanFolder(ctx, folder); err != nil {
				t.Fatalf("rescan: %v", err)
			}
			if got, want := storedSidecarPaths(t, repo, moviePath), sortedPaths(beside, inSubs); !slices.Equal(got, want) {
				t.Fatalf("rescan stored sidecars %v, want %v", got, want)
			}
		})
	}
}

func storedSidecarPaths(t *testing.T, repo *FileRepository, filePath string) []string {
	t.Helper()
	file, err := repo.GetByPath(t.Context(), filePath)
	if err != nil {
		t.Fatalf("reading %s: %v", filePath, err)
	}
	paths := make([]string, 0, len(file.ExternalSubtitles))
	for _, sub := range file.ExternalSubtitles {
		paths = append(paths, sub.Path)
	}
	slices.Sort(paths)
	return paths
}

func sortedPaths(paths ...string) []string {
	slices.Sort(paths)
	return paths
}
