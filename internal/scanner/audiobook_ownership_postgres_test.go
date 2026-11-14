package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A rescan of a provider-matched book keeps the provider's year, genres and
// credits, even when the file has no year tag and different credits; tags
// only fill what the provider left empty. Without a match the tags rule.
func TestAudiobookRescanKeepsProviderMatchDB(t *testing.T) {
	for _, tc := range []struct {
		name        string
		matched     bool
		wantYear    int
		wantGenres  []string
		wantCredits []string
	}{
		{"provider matched", true, 2008, []string{"Provider Genre"}, []string{"Provider Author", "Tag Narrator"}},
		{"unmatched", false, 0, []string{"Provider Genre", "Tag Genre"}, []string{"Tag Author", "Tag Narrator"}},
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
			if tc.matched {
				if _, err := pool.Exec(ctx, `INSERT INTO media_item_provider_ids (content_id,provider,provider_id,item_type) VALUES ($1,'audible',$1,'audiobook')`, id); err != nil {
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
			if err := s.upsertAudiobookPresentationTx(ctx, tx, folder, id, dir, book); err != nil {
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
