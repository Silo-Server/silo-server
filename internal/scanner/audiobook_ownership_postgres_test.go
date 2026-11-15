package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// audiobookMatchSeeds put a test book in each match state; $1 is its content ID.
var audiobookMatchSeeds = map[string][]string{
	"provider id": {`INSERT INTO media_item_provider_ids (content_id,provider,provider_id,item_type) VALUES ($1,'audible',$1,'audiobook')`},
	"outcome": {
		`INSERT INTO media_item_provider_ids (content_id,provider,provider_id,item_type) VALUES ($1,'asin',$1,'audiobook')`,
		`INSERT INTO audiobook_enrichment_state (content_id,attempts,outcome) VALUES ($1,1,'success')`,
	},
	"failed": {
		`INSERT INTO media_item_provider_ids (content_id,provider,provider_id,item_type) VALUES ($1,'asin',$1,'audiobook')`,
		`INSERT INTO audiobook_enrichment_state (content_id,attempts,outcome) VALUES ($1,5,'failed')`,
	},
	// Match Item writes status 'matched' with the IDs it applied.
	"admin": {
		`INSERT INTO media_item_provider_ids (content_id,provider,provider_id,item_type) VALUES ($1,'asin',$1,'audiobook')`,
		`UPDATE media_items SET status = 'matched' WHERE content_id = $1`,
	},
}

// A rescan of a provider-matched book keeps the provider's year, genres and
// credits, even when the file has no year tag and different credits; tags
// only fill what the provider left empty. A match whose only stored ID is the
// tagged ASIN counts through its success outcome, or through the matched
// status Match Item writes. Without a match the tags rule.
func TestAudiobookRescanKeepsProviderMatchDB(t *testing.T) {
	for _, tc := range []struct {
		name        string
		matchedBy   string // "", "provider id", "outcome", "admin" or "failed"
		wantYear    int
		wantGenres  []string
		wantCredits []string
	}{
		{"provider matched", "provider id", 2008, []string{"Provider Genre"}, []string{"Provider Author", "Tag Narrator"}},
		{"matched through the tagged asin", "outcome", 2008, []string{"Provider Genre"}, []string{"Provider Author", "Tag Narrator"}},
		{"matched by an admin with only the asin", "admin", 2008, []string{"Provider Genre"}, []string{"Provider Author", "Tag Narrator"}},
		{"unmatched", "", 0, []string{"Provider Genre", "Tag Genre"}, []string{"Tag Author", "Tag Narrator"}},
		// Providers that kept failing are no match: the tags still rule.
		{"providers failed", "failed", 0, []string{"Provider Genre", "Tag Genre"}, []string{"Tag Author", "Tag Narrator"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newBookScanTestPool(t)
			folder := newBookScanTestFolder(t, pool, "audiobooks")
			ctx := t.Context()
			dir := t.TempDir()
			path := filepath.Join(dir, "book.m4b")
			if err := os.WriteFile(path, []byte("book"), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			id := fmt.Sprintf("provider-owned-%d", folder.ID)
			if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,status,year,genres)
VALUES ($1,'audiobook','Emma','',2008,'{Provider Genre}'::text[])`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, id, folder.ID); err != nil {
				t.Fatal(err)
			}
			for _, stmt := range audiobookMatchSeeds[tc.matchedBy] {
				if _, err := pool.Exec(ctx, stmt, id); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE content_id=$1`, id)
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, id)
			})

			s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
			authorID, err := s.personRepo.FindOrCreate(ctx, models.Person{Name: "Provider Author"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.itemRepo.ReplacePeople(ctx, id, []models.ItemPerson{{Person: models.Person{ID: authorID}, Kind: models.PersonKindAuthor}}); err != nil {
				t.Fatal(err)
			}

			// The file has no year tag and its own author, narrator and genre.
			book := &parsedAudiobook{Title: "Emma", Author: "Tag Author", Narrator: "Tag Narrator", Genres: []string{"Tag Genre"},
				Files: []parsedAudiobookFile{{Path: path, Duration: 36000, Size: info.Size(), ModifiedAt: normalizeFileModifiedAt(info.ModTime())}}}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if err := s.upsertAudiobookPresentationTx(ctx, tx, folder, id, dir, canonicalTestPath(t, dir), book, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.upsertAudiobookPeople(ctx, id, book); err != nil {
				t.Fatal(err)
			}

			var year int
			var genres []string
			if err := pool.QueryRow(ctx, `SELECT year, genres FROM media_items WHERE content_id=$1`, id).Scan(&year, &genres); err != nil {
				t.Fatal(err)
			}
			if year != tc.wantYear || !slices.Equal(genres, tc.wantGenres) {
				t.Errorf("year, genres = %d, %v; want %d, %v", year, genres, tc.wantYear, tc.wantGenres)
			}
			people, err := s.itemRepo.GetPeople(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, p := range people {
				got = append(got, p.Name)
			}
			if !slices.Equal(got, tc.wantCredits) {
				t.Errorf("credits = %v, want %v", got, tc.wantCredits)
			}
		})
	}
}

// A renamed folder keeps its item, and with it the listening progress: a
// provider-matched book whose provider year differs from the tag year, and a
// book whose files predate base_year (migration 078 left them at 0) and whose
// item year still equals the tag year.
func TestAudiobookRenamedFolderFindsMatchedItemDB(t *testing.T) {
	for _, tc := range []struct {
		name     string
		itemYear int
		fileYear int
	}{
		{"provider year differs from the tag", 2008, 2005},
		{"files from before base_year", 2005, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			renamedFolderKeepsItem(t, tc.itemYear, tc.fileYear)
		})
	}
}

func renamedFolderKeepsItem(t *testing.T, itemYear, fileYear int) {
	t.Helper()
	pool := newBookScanTestPool(t)
	folder := newBookScanTestFolder(t, pool, "audiobooks")
	ctx := t.Context()
	id := fmt.Sprintf("renamed-matched-%d", folder.ID)
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,status,year,genres)
VALUES ($1,'audiobook','Emma','',$2,'{}'::text[])`, id, itemYear); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE content_id=$1`, id)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, id)
	})
	for _, stmt := range []string{
		`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`,
		`INSERT INTO media_item_provider_ids (content_id,provider,provider_id,item_type) VALUES ($1,'audible',$1,'audiobook')`,
	} {
		args := []any{id}
		if strings.Contains(stmt, "media_folder_id") {
			args = append(args, folder.ID)
		}
		if _, err := pool.Exec(ctx, stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
	var credits []models.ItemPerson
	for _, c := range []struct {
		name string
		kind models.PersonKind
	}{{"Jane Austen", models.PersonKindAuthor}, {"Juliet Stevenson", models.PersonKindNarrator}} {
		personID, err := s.personRepo.FindOrCreate(ctx, models.Person{Name: c.name})
		if err != nil {
			t.Fatal(err)
		}
		credits = append(credits, models.ItemPerson{Person: models.Person{ID: personID}, Kind: c.kind})
	}
	if err := s.itemRepo.ReplacePeople(ctx, id, credits); err != nil {
		t.Fatal(err)
	}
	// The tag year is 2005; the file stored fileYear when it was scanned.
	oldDir := t.TempDir()
	if _, err := s.fileRepo.Upsert(ctx, models.MediaFile{ContentID: id, MediaFolderID: folder.ID, FilePath: filepath.Join(oldDir, "book.m4b"),
		ObservedRootPath: oldDir, CanonicalRootPath: oldDir, BaseTitle: "Emma", BaseYear: fileYear, BaseType: "audiobook", Duration: 36000}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_files SET missing_since = NOW() WHERE content_id = $1`, id); err != nil {
		t.Fatal(err)
	}

	newDir := t.TempDir()
	path := filepath.Join(newDir, "book.m4b")
	if err := os.WriteFile(path, []byte("book"), 0o600); err != nil {
		t.Fatal(err)
	}
	book := &parsedAudiobook{Title: "Emma", Author: "Jane Austen", Narrator: "Juliet Stevenson", Year: 2005,
		Files: []parsedAudiobookFile{{Path: path, Duration: 36000}}}
	got, err := s.upsertAudiobookMediaItem(ctx, folder.ID, newDir, canonicalTestPath(t, newDir), book)
	if err != nil {
		t.Fatal(err)
	}
	if got != id {
		t.Fatalf("renamed folder got content ID %s, want the matched item %s", got, id)
	}
}

// A book reached through a link that moves to another book while the scan
// reads it is left alone for this scan: the files it read belong to the other
// book, and their metadata must not replace this book's.
func TestAudiobookRootMovedWhileReadKeepsItsItemDB(t *testing.T) {
	pool := newBookScanTestPool(t)
	folder := newBookScanTestFolder(t, pool, "audiobooks")
	ctx := t.Context()
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	mtime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, dir := range []string{first, second} {
		part := filepath.Join(dir, "01.mp3")
		writeTestFile(t, part, "audio")
		if err := os.Chtimes(part, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "book")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_item_roots WHERE media_folder_id=$1`, folder.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_files WHERE media_folder_id=$1)`, folder.ID)
	})
	probe := func(title, before string) string {
		path := filepath.Join(t.TempDir(), "ffprobe")
		writeFakeTool(t, path, "#!/bin/sh\n"+before+"cat <<'JSON'\n"+
			`{"format":{"format_name":"mp3","duration":"60.0","size":"5","bit_rate":"64000","tags":{"title":"`+title+`","artist":"Synthetic Author"}},"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3","channels":2}],"chapters":[]}`+
			"\nJSON\n")
		return path
	}
	s := NewScanner(NewFileRepository(pool), probe("First Book", ""), nil, 1, false, 0)
	var skipped int64
	if err := s.reconcileAudiobookFolder(ctx, folder, link, nil, nil, time.Time{}, &skipped, nil); err != nil {
		t.Fatal(err)
	}
	// Make the first book's file look changed, so the next scan reads it, and
	// move the link to the second book while that read runs.
	if _, err := pool.Exec(ctx, `UPDATE media_files SET file_size = 1 WHERE media_folder_id = $1`, folder.ID); err != nil {
		t.Fatal(err)
	}
	s.ffprobePath = probe("Second Book", fmt.Sprintf("ln -sfn '%s' '%s'\n", second, link))
	if err := s.reconcileAudiobookFolder(ctx, folder, link, nil, nil, time.Time{}, &skipped, nil); err == nil {
		t.Fatal("scan of a book whose link moved while it was read succeeded")
	}
	var titles []string
	rows, err := pool.Query(ctx, `SELECT DISTINCT mi.title FROM media_items mi JOIN media_files mf ON mf.content_id = mi.content_id WHERE mf.media_folder_id = $1`, folder.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatal(err)
		}
		titles = append(titles, title)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(titles, []string{"First Book"}) {
		t.Fatalf("titles = %v, want [First Book]", titles)
	}
}
