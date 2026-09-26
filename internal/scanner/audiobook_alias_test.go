package scanner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAudiobookAliasesShareIdentity(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := t.Context()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	base := t.TempDir()
	physical := filepath.Join(base, "storage")
	root := filepath.Join(base, "library")
	for _, path := range []string{physical, root} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	track := filepath.Join(physical, "part1.mp3")
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "anullsrc", "-t", "1", "-map_metadata", "-1", track).CombinedOutput(); err != nil {
		t.Fatalf("generate audio: %v: %s", err, out)
	}
	data, err := os.ReadFile(track)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(physical, "part2.mp3"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
	for _, alias := range []string{a, b} {
		if err := os.Symlink(physical, alias); err != nil {
			t.Fatal(err)
		}
	}
	folderID := seedDeadRootTestFolder(t, pool, "audiobooks", "Alias identity test")
	if _, err := pool.Exec(ctx, `INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1,$2)`, folderID, root); err != nil {
		t.Fatal(err)
	}
	folder := &models.MediaFolder{ID: folderID, Type: "audiobooks", Paths: []string{root}, Enabled: true}
	s := NewScanner(NewFileRepository(pool), ffprobe, nil, 8, true, 0)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_files WHERE media_folder_id=$1)`, folderID)
	})
	var original string
	check := func(wantPath string) {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(DISTINCT content_id) FROM media_files WHERE media_folder_id=$1 AND missing_since IS NULL`, folderID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("live identities = %d, want 1", count)
		}
		var id string
		if err := pool.QueryRow(ctx, `SELECT content_id FROM media_files WHERE media_folder_id=$1 AND missing_since IS NULL LIMIT 1`, folderID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if original == "" {
			original = id
		} else if id != original {
			t.Fatalf("identity changed: %s -> %s", original, id)
		}
		files, err := s.fileRepo.GetByContentIDPresentation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 2 {
			t.Fatalf("playback has %d files, want two parts without aliases", len(files))
		}
		for i, file := range files {
			if filepath.Dir(file.FilePath) != wantPath || file.PresentationPartIndex != i+1 || file.PresentationPartTotal != 2 {
				t.Fatalf("unexpected playback part: %+v", file)
			}
		}
	}
	scan := func() {
		t.Helper()
		if err := s.ScanAudiobookFolder(ctx, folder, true); err != nil {
			t.Fatal(err)
		}
	}
	scan()
	check(a)
	// Simulate a book indexed with the old logical canonical-root value.
	if _, err := pool.Exec(ctx, `UPDATE media_files SET canonical_root_path=observed_root_path WHERE media_folder_id=$1`, folderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_item_roots SET canonical_root_path=$2 WHERE media_folder_id=$1`, folderID, a); err != nil {
		t.Fatal(err)
	}
	scan()
	check(a)
	// A scoped scan through the other alias must still reuse the identity and
	// switch the complete presentation, even while A remains readable.
	if err := s.ScanAudiobookFolder(ctx, scopedFolderPaths(folder, []string{b}), true); err != nil {
		t.Fatal(err)
	}
	check(b)
	scan()
	check(b)
	// Removing the preferred alias must not change identity or lose a part.
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	scan()
	check(a)
	// Reappearing aliases must not create another identity or duplicate parts.
	if err := os.Symlink(physical, b); err != nil {
		t.Fatal(err)
	}
	scan()
	check(a)
}

func TestAudiobookAliasesConcurrentIdentity(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := t.Context()
	folderID := seedDeadRootTestFolder(t, pool, "audiobooks", "Concurrent aliases")
	root := t.TempDir()
	physical := filepath.Join(root, "book")
	if err := os.Mkdir(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(physical, "track.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(root, "A"), filepath.Join(root, "B")}
	for _, path := range paths {
		if err := os.Symlink(physical, path); err != nil {
			t.Fatal(err)
		}
	}
	s := NewScanner(NewFileRepository(pool), "", nil, 8, false, 0)
	folder := &models.MediaFolder{ID: folderID, Type: "audiobooks"}
	type result struct {
		id  string
		err error
	}
	results := make(chan result, len(paths))
	start := make(chan struct{})
	for _, path := range paths {
		go func() {
			<-start
			file := filepath.Join(path, "track.mp3")
			info, err := os.Stat(file)
			if err != nil {
				results <- result{err: err}
				return
			}
			book := &parsedAudiobook{Title: "Book", Files: []parsedAudiobookFile{{Path: file, Size: info.Size(), ModifiedAt: normalizeFileModifiedAt(info.ModTime())}}}
			id, err := s.upsertAudiobookMediaItem(ctx, folderID, path, book)
			if err == nil {
				tx, txErr := pool.Begin(ctx)
				if txErr != nil {
					err = txErr
				} else {
					err = s.upsertAudiobookPresentationTx(ctx, tx, folder, id, path, book)
					if err == nil {
						err = tx.Commit(ctx)
					} else {
						_ = tx.Rollback(ctx)
					}
				}
			}
			results <- result{id, err}
		}()
	}
	close(start)
	ids := make(map[string]bool)
	for range paths {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		ids[r.id] = true
	}
	for id := range ids {
		t.Cleanup(func() {
			_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM media_items WHERE content_id=$1`, id)
		})
	}
	if len(ids) != 1 {
		t.Fatalf("concurrent aliases created %d identities", len(ids))
	}
	for id := range ids {
		files, err := s.fileRepo.GetByContentIDPresentation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 {
			t.Fatalf("concurrent aliases left %d playable copies", len(files))
		}
	}
}

func TestClaimAudiobookIdentityRefreshesExistingClaim(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := t.Context()
	folderID := seedDeadRootTestFolder(t, pool, "audiobooks", "Claim timestamp test")
	s := NewScanner(NewFileRepository(pool), "", nil, 1, false, 0)
	physical := t.TempDir()
	book := &parsedAudiobook{Title: "Book"}
	id, err := s.claimAudiobookIdentity(ctx, folderID, physical, "", book, book.Title)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM media_items WHERE content_id=$1`, id)
	})
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `UPDATE media_item_roots SET first_seen_at=$3, last_seen_at=$3 WHERE media_folder_id=$1 AND canonical_root_path=$2`, folderID, physical, old); err != nil {
		t.Fatal(err)
	}
	got, err := s.claimAudiobookIdentity(ctx, folderID, physical, "", book, book.Title)
	if err != nil {
		t.Fatal(err)
	}
	var storedID string
	var first, last time.Time
	if err := pool.QueryRow(ctx, `SELECT content_id, first_seen_at, last_seen_at FROM media_item_roots WHERE media_folder_id=$1 AND canonical_root_path=$2`, folderID, physical).Scan(&storedID, &first, &last); err != nil {
		t.Fatal(err)
	}
	if got != id || storedID != id {
		t.Fatalf("claim identity changed: returned=%s stored=%s want=%s", got, storedID, id)
	}
	if !first.Equal(old) {
		t.Fatalf("first_seen_at changed: %v", first)
	}
	if !last.After(old) {
		t.Fatalf("last_seen_at was not refreshed: %v", last)
	}
}

// Force the review's interleaving without sleeps: resolve A, resolve B, commit
// B's presentation, then A's. Metadata must follow the committed presentation.
func TestAudiobookAliasMetadataFollowsPresentation(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := t.Context()
	folderID := seedDeadRootTestFolder(t, pool, "audiobooks", "Alias metadata ordering")
	folder := &models.MediaFolder{ID: folderID, Type: "audiobooks"}
	root := t.TempDir()
	physical := filepath.Join(root, "recording")
	if err := os.Mkdir(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(physical, "track.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(root, "Title A"), filepath.Join(root, "Title B")}
	s := NewScanner(NewFileRepository(pool), "", nil, 2, false, 0)
	books := make([]*parsedAudiobook, 2)
	var id string
	for i, path := range paths {
		if err := os.Symlink(physical, path); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(path, "track.mp3")
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		books[i] = &parsedAudiobook{Files: []parsedAudiobookFile{{Path: file, Size: info.Size(), ModifiedAt: normalizeFileModifiedAt(info.ModTime())}}}
		books[i].applyFilesystemFallbacks(path, []string{file})
		got, err := s.upsertAudiobookMediaItem(ctx, folderID, path, books[i])
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			id = got
			t.Cleanup(func() {
				_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM media_items WHERE content_id=$1`, id)
			})
		} else if got != id {
			t.Fatalf("identity changed: %s -> %s", id, got)
		}
	}
	commit := func(i int, rollback bool) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		if err := s.upsertAudiobookPresentationTx(ctx, tx, folder, id, paths[i], books[i]); err != nil {
			t.Fatal(err)
		}
		if !rollback {
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(i int) {
		t.Helper()
		item, err := s.itemRepo.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		files, err := s.fileRepo.GetByContentIDPresentation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 || filepath.Dir(files[0].FilePath) != paths[i] {
			t.Fatalf("unexpected playable alias: %+v", files)
		}
		if item.Title != books[i].Title {
			t.Fatalf("playable alias %s has catalog title %q, want %q", paths[i], item.Title, books[i].Title)
		}
	}
	commit(1, false)
	check(1)
	commit(0, false)
	check(0)
	// A failed replacement must roll back its metadata along with its files.
	commit(1, true)
	check(0)
	_, skip, err := s.audiobookFolderShouldSkip(ctx, folder, paths[0], nil)
	if err != nil || !skip {
		t.Fatalf("unchanged alias skip=%v err=%v", skip, err)
	}
}
