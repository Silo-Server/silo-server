package scanner

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/literaryworks"
)

func TestBookScansLinkEarlierEditionsWhenOtherFormatArrives(t *testing.T) {
	for _, ebookFirst := range []bool{false, true} {
		name := "two-audiobooks-before-ebook"
		if ebookFirst {
			name = "two-ebooks-before-audiobook"
		}
		t.Run(name, func(t *testing.T) {
			pool := newBookScanTestPool(t)
			ebooks := newBookScanTestFolder(t, pool, "ebooks")
			audio := newBookScanTestFolder(t, pool, "audiobooks")
			title := fmt.Sprintf("Multiple Editions %d", ebooks.ID)
			s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
			s.SetLiteraryWorkLinker(literaryworks.NewService(literaryworks.NewRepository(pool)))
			var skipped int64
			scanEbook := func(edition int) {
				// Distinct valid ISBNs keep ebook editions as separate catalog items.
				isbn := fmt.Sprintf("978%09d", ebooks.ID*10+edition)
				checksum := 0
				for i, digit := range isbn {
					checksum += int(digit-'0') * (1 + 2*(i%2))
				}
				isbn += fmt.Sprint((10 - checksum%10) % 10)
				path := writeTestEPUBWithOPFBytes(t, []byte(fmt.Sprintf(`<package xmlns:dc="http://purl.org/dc/elements/1.1/"><metadata><dc:title>%s</dc:title><dc:creator>Multiple Editions Author</dc:creator><dc:identifier>%s</dc:identifier></metadata></package>`, title, isbn)))
				if err := s.reconcileEbookFile(t.Context(), ebooks, path, &skipped, newEbookGroupLocks()); err != nil {
					t.Fatal(err)
				}
			}
			scanAudio := func(_ int) {
				path := t.TempDir()
				if _, err := exec.LookPath("ffmpeg"); err != nil {
					t.Skip("ffmpeg unavailable")
				}
				if out, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "anullsrc", "-t", "0.1", "-metadata", "title="+title, "-metadata", "artist=Multiple Editions Author", filepath.Join(path, "book.m4b")).CombinedOutput(); err != nil {
					t.Fatalf("generate audio: %v: %s", err, out)
				}
				if err := s.reconcileAudiobookFolder(t.Context(), audio, path, nil, &skipped); err != nil {
					t.Fatal(err)
				}
			}
			first, last := scanAudio, scanEbook
			if ebookFirst {
				first, last = scanEbook, scanAudio
			}
			first(1)
			first(2)
			var items, members, works int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM media_item_libraries WHERE media_folder_id=ANY($1)`, []int{ebooks.ID, audio.ID}).Scan(&items); err != nil || items != 2 {
				t.Fatalf("earlier editions: items=%d, err=%v, want two distinct items", items, err)
			}
			last(3)
			if err := pool.QueryRow(t.Context(), `SELECT count(*), count(DISTINCT wi.work_id) FROM literary_work_items wi JOIN media_item_libraries ml USING(content_id) WHERE ml.media_folder_id=ANY($1)`, []int{ebooks.ID, audio.ID}).Scan(&members, &works); err != nil {
				t.Fatal(err)
			}
			if members != 3 || works != 1 {
				t.Fatalf("linked members=%d works=%d, want all 3 editions in one work", members, works)
			}
		})
	}
}
