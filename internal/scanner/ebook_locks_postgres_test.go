package scanner

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// A rescan of a retagged, unmatched ebook keeps the fields and author credits
// an admin locked, including locked fields left empty, and takes the file's
// values for every field that is not locked.
func TestEbookRescanKeepsLockedFieldsDB(t *testing.T) {
	type stored struct {
		title, sortTitle, overview string
		year                       int
		releaseDate                string
		genres, studios, authors   []string
	}
	// The admin's edits, as PATCH /api/v2/admin/items/{id}/metadata stores them.
	const adminEdit = `UPDATE media_items SET title='The Admin Title', sort_title='', overview='<p>Admin overview</p>',
year=1999, genres='{Admin Genre}', studios='{Admin Studio}', locked_fields=$2::integer[] WHERE content_id=$1`
	const clearedEdit = `UPDATE media_items SET overview='', release_date=NULL, locked_fields=$2::integer[] WHERE content_id=$1`
	retagged := stored{
		title: "The Retagged Title", sortTitle: "Retagged Title, The", overview: "Retagged description",
		year: 2019, releaseDate: "2001-02-03",
		genres: []string{"Admin Genre", "Horror"}, studios: []string{"Admin Studio", "Retag House"},
		authors: []string{"Tagged Author"},
	}
	for _, tc := range []struct {
		name, edit, locked string
		want               stored
	}{
		{"all locked", adminEdit, "{0,1,2,3,5,13}", stored{
			title: "The Admin Title", sortTitle: "", overview: "<p>Admin overview</p>",
			year: 1999, releaseDate: "2001-02-03",
			genres: []string{"Admin Genre"}, studios: []string{"Admin Studio"},
			authors: []string{"First Author"},
		}},
		{"unlocked", adminEdit, "{}", retagged},
		{"cast lock holds authors", adminEdit, "{4}", func() stored {
			s := retagged
			s.authors = []string{"First Author"}
			return s
		}()},
		{"locked empty fields stay empty", clearedEdit, "{1,13}", stored{
			title: "The Retagged Title", sortTitle: "First Title, The", overview: "",
			year: 2001, releaseDate: "",
			genres: []string{"Fiction", "Horror"}, studios: []string{"First House", "Retag House"},
			authors: []string{"Tagged Author"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newBookScanTestPool(t)
			folder := newBookScanTestFolder(t, pool, "ebooks")
			ctx := t.Context()
			s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
			path := filepath.Join(t.TempDir(), "book.epub")
			scan := func(title, author, publisher, date, subject, description string, modified time.Time) {
				t.Helper()
				src := writeTestEPUBWithOPFBytes(t, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
  <metadata>
    <dc:title>`+title+`</dc:title>
    <dc:creator>`+author+`</dc:creator>
    <dc:publisher>`+publisher+`</dc:publisher>
    <dc:date>`+date+`</dc:date>
    <dc:subject>`+subject+`</dc:subject>
    <dc:description>`+description+`</dc:description>
  </metadata>
</package>`))
				if err := os.Rename(src, path); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, modified, modified); err != nil {
					t.Fatal(err)
				}
				var skipped int64
				if err := s.reconcileEbookFile(ctx, folder, path, &skipped, newEbookGroupLocks()); err != nil {
					t.Fatal(err)
				}
			}

			first := time.Now().Add(-time.Hour)
			scan("The First Title", "First Author", "First House", "2001-02-03", "Fiction", "First description", first)
			var id string
			if err := pool.QueryRow(ctx, `SELECT content_id FROM media_item_libraries WHERE media_folder_id=$1`, folder.ID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, tc.edit, id, tc.locked); err != nil {
				t.Fatal(err)
			}

			scan("The Retagged Title", "Tagged Author", "Retag House", "2019-05-06", "Horror", "Retagged description", first.Add(30*time.Minute))

			var got stored
			if err := pool.QueryRow(ctx, `SELECT title, sort_title, overview, year, COALESCE(release_date::text, ''), genres, studios
FROM media_items WHERE content_id=$1`, id).
				Scan(&got.title, &got.sortTitle, &got.overview, &got.year, &got.releaseDate, &got.genres, &got.studios); err != nil {
				t.Fatal(err)
			}
			people, err := s.itemRepo.GetPeople(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range people {
				got.authors = append(got.authors, p.Name)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("after rescan got %+v, want %+v", got, tc.want)
			}
		})
	}
}
