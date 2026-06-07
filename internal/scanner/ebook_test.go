package scanner

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSupportsEbookFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"book.epub", true},
		{"book.pdf", true},
		{"book.mobi", true},
		{"book.azw", true},
		{"book.azw3", true},
		{"book.fb2", true},
		{"book.txt", false},
		{"book.mp3", false},
	}
	for _, tt := range tests {
		if got := SupportsEbookFile(tt.path); got != tt.want {
			t.Fatalf("SupportsEbookFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestParsedEbookSanitizeBoundsFields(t *testing.T) {
	book := parsedEbook{
		Format:    "EPUB",
		Title:     "  Title  ",
		Authors:   []string{" Alice ", "", "Alice", "Bob"},
		Genres:    []string{" Fiction ", "fiction", ""},
		PageCount: -5,
	}
	book.sanitize()
	if book.Format != "epub" {
		t.Fatalf("format = %q, want epub", book.Format)
	}
	if book.Title != "Title" {
		t.Fatalf("title = %q, want Title", book.Title)
	}
	if len(book.Authors) != 2 || book.Authors[0] != "Alice" || book.Authors[1] != "Bob" {
		t.Fatalf("authors = %#v, want Alice/Bob", book.Authors)
	}
	if len(book.Genres) != 1 || book.Genres[0] != "Fiction" {
		t.Fatalf("genres = %#v, want Fiction", book.Genres)
	}
	if book.PageCount != 0 {
		t.Fatalf("page count = %d, want 0", book.PageCount)
	}
}

func TestParseEbookEPUBReadsOPFMetadata(t *testing.T) {
	path := writeTinyEPUB(t, map[string]string{
		"dc:title":       "EPUB Title",
		"dc:creator":     "Author A",
		"dc:language":    "en",
		"dc:identifier":  "978-1-4028-9462-6",
		"dc:publisher":   "Publisher P",
		"dc:description": "Description D",
		"dc:date":        "2020-01-02",
	})
	book, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}
	if book.Title != "EPUB Title" || len(book.Authors) != 1 || book.Authors[0] != "Author A" {
		t.Fatalf("book metadata = %+v", book)
	}
	if book.ISBN != "9781402894626" || book.Year != 2020 || book.Language != "en" {
		t.Fatalf("book identifiers/year/language = %+v", book)
	}
}

func TestParseEbookEPUBReadsCalibreSeriesMetadata(t *testing.T) {
	path := writeTinyEPUB(t, map[string]string{
		"dc:title":            "Series Book",
		"meta:calibre:series": "Series S",
		"meta:calibre:index":  "3.5",
	})
	book, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}
	if book.Series != "Series S" || book.SeriesIndex != "3.5" {
		t.Fatalf("series metadata = %q/%q, want Series S/3.5", book.Series, book.SeriesIndex)
	}
}

func TestParseEbookFormatOnlyFallbacks(t *testing.T) {
	for _, ext := range []string{".pdf", ".mobi", ".azw", ".azw3"} {
		path := filepath.Join(t.TempDir(), "book"+ext)
		if err := os.WriteFile(path, []byte("placeholder"), 0o644); err != nil {
			t.Fatalf("write %s: %v", ext, err)
		}
		book, err := parseEbookFile(path)
		if err != nil {
			t.Fatalf("parseEbookFile(%s): %v", ext, err)
		}
		if book.Format != ext[1:] {
			t.Fatalf("format for %s = %q, want %q", ext, book.Format, ext[1:])
		}
	}
}

func TestParseEbookFileRejectsUnsupportedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.txt")
	if err := os.WriteFile(path, []byte("not an ebook"), 0o644); err != nil {
		t.Fatalf("write txt: %v", err)
	}
	_, err := parseEbookFile(path)
	if err == nil {
		t.Fatal("parseEbookFile returned nil error, want unsupported format error")
	}
}

func TestParseEbookFileReturnsCorruptInputErrors(t *testing.T) {
	t.Run("epub", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "book.epub")
		if err := os.WriteFile(path, []byte("not a zip"), 0o644); err != nil {
			t.Fatalf("write epub: %v", err)
		}
		if _, err := parseEbookFile(path); err == nil {
			t.Fatal("parseEbookFile corrupt epub returned nil error")
		}
	})

	t.Run("fb2", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "book.fb2")
		if err := os.WriteFile(path, []byte("<FictionBook>"), 0o644); err != nil {
			t.Fatalf("write fb2: %v", err)
		}
		if _, err := parseEbookFile(path); err == nil {
			t.Fatal("parseEbookFile corrupt fb2 returned nil error")
		}
	})
}

func TestScanEbookFolderReturnsErrorWhenEveryReconcileFails(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad.epub"), []byte("not a zip"), 0o644); err != nil {
		t.Fatalf("write corrupt epub: %v", err)
	}

	s := &Scanner{}
	err := s.ScanEbookFolder(context.Background(), &models.MediaFolder{ID: 44, Paths: []string{root}})
	if err == nil {
		t.Fatal("ScanEbookFolder returned nil, want aggregate failure")
	}
	if !strings.Contains(err.Error(), "folder_id=44") {
		t.Fatalf("error = %q, want folder id", err)
	}
}

func TestScanFolderRoutesEbookLibrariesToEbookScanner(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad.epub"), []byte("not a zip"), 0o644); err != nil {
		t.Fatalf("write corrupt epub: %v", err)
	}

	s := &Scanner{}
	result, err := s.ScanFolder(context.Background(), &models.MediaFolder{ID: 45, Type: "ebooks", Paths: []string{root}})
	if err == nil {
		t.Fatal("ScanFolder returned nil error, want ebook scan failure")
	}
	if result != nil {
		t.Fatalf("ScanFolder result = %#v, want nil on error", result)
	}
	if !strings.Contains(err.Error(), "folder_id=45") {
		t.Fatalf("error = %q, want ebook scanner aggregate error", err)
	}
}

func TestResolveEbookMediaItemReusesRootScopedContentID(t *testing.T) {
	finder := &fakeRootContentFinder{contentID: "ebook-root-id"}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolveEbookMediaItem(
		context.Background(),
		finder,
		writer,
		7,
		"/library/Author/Same Title/book.epub",
		&parsedEbook{Title: "Same Title", Year: 2024, Authors: []string{"Author A"}},
	)
	if err != nil {
		t.Fatalf("resolveEbookMediaItem: %v", err)
	}
	if got != "ebook-root-id" {
		t.Fatalf("contentID = %q, want root-scoped id", got)
	}
	if finder.calls != 1 {
		t.Fatalf("root finder calls = %d, want 1", finder.calls)
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("item upserts = %d, want metadata refresh", len(writer.upserts))
	}
	item := writer.upserts[0]
	if item.ContentID != "ebook-root-id" || item.Type != "ebook" || item.Title != "Same Title" {
		t.Fatalf("refreshed item = %+v", item)
	}
}

func TestResolveEbookMediaItemCreatesNewWhenRootHasNoClaim(t *testing.T) {
	finder := &fakeRootContentFinder{}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolveEbookMediaItem(
		context.Background(),
		finder,
		writer,
		7,
		"/library/Other Author/Same Title/book.epub",
		&parsedEbook{
			Title:       "The Same Title",
			Year:        2024,
			Authors:     []string{"Author B"},
			Description: "Description D",
			Genres:      []string{"Fiction"},
			Publisher:   "Publisher P",
			Language:    "en",
		},
	)
	if err != nil {
		t.Fatalf("resolveEbookMediaItem: %v", err)
	}
	if got == "" {
		t.Fatal("contentID is empty")
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(writer.upserts))
	}
	item := writer.upserts[0]
	if item.ContentID != got || item.Type != "ebook" {
		t.Fatalf("upserted item = %+v, contentID %q", item, got)
	}
	if item.Title != "The Same Title" || item.SortTitle != "Same Title, The" || item.Year != 2024 {
		t.Fatalf("item title/sort/year = %+v", item)
	}
	if item.Overview != "Description D" || len(item.Genres) != 1 || item.Genres[0] != "Fiction" {
		t.Fatalf("item descriptive metadata = %+v", item)
	}
	if len(item.Studios) != 1 || item.Studios[0] != "Publisher P" || item.OriginalLanguage != "en" {
		t.Fatalf("item publisher/language = %+v", item)
	}
}

func TestResolveEbookMediaItemDefaultsBlankTitle(t *testing.T) {
	finder := &fakeRootContentFinder{}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolveEbookMediaItem(
		context.Background(),
		finder,
		writer,
		7,
		"/library/untitled.epub",
		&parsedEbook{},
	)
	if err != nil {
		t.Fatalf("resolveEbookMediaItem: %v", err)
	}
	if got == "" {
		t.Fatal("contentID is empty")
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(writer.upserts))
	}
	if writer.upserts[0].Title != "Untitled Ebook" {
		t.Fatalf("title = %q, want default", writer.upserts[0].Title)
	}
}

func TestEbookIdentityConfidenceReflectsMetadataCompleteness(t *testing.T) {
	if got := ebookIdentityConfidence(&parsedEbook{ISBN: "9781402894626"}); got != "high" {
		t.Fatalf("ISBN confidence = %q, want high", got)
	}
	if got := ebookIdentityConfidence(&parsedEbook{Title: "Tagged Book", Authors: []string{"Author"}, Year: 2024}); got != "medium" {
		t.Fatalf("title/author/year confidence = %q, want medium", got)
	}
	if got := ebookIdentityConfidence(&parsedEbook{Title: "Tagged Book"}); got != "low" {
		t.Fatalf("partial confidence = %q, want low", got)
	}
}

func TestParseEbookFB2ReadsDescriptionMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.fb2")
	xml := `<?xml version="1.0" encoding="utf-8"?>
<FictionBook><description><title-info>
<genre>fiction</genre><author><first-name>Alice</first-name><last-name>Author</last-name></author>
<book-title>FB2 Title</book-title><lang>en</lang><sequence name="Series S" number="2"/>
</title-info><publish-info><isbn>9781402894626</isbn><publisher>Publisher P</publisher><year>2021</year></publish-info></description></FictionBook>`
	if err := os.WriteFile(path, []byte(xml), 0o644); err != nil {
		t.Fatalf("write fb2: %v", err)
	}
	book, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}
	if book.Title != "FB2 Title" || book.Series != "Series S" || book.SeriesIndex != "2" {
		t.Fatalf("book metadata = %+v", book)
	}
}

func writeTinyEPUB(t *testing.T, metadata map[string]string) string {
	t.Helper()

	metadataXML := ""
	for tag, value := range metadata {
		switch tag {
		case "meta:calibre:series":
			metadataXML += fmt.Sprintf(`<meta name="calibre:series" content="%s"/>`+"\n", value)
		case "meta:calibre:index":
			metadataXML += fmt.Sprintf(`<meta name="calibre:series_index" content="%s"/>`+"\n", value)
		default:
			metadataXML += fmt.Sprintf("<%s>%s</%s>\n", tag, value, tag)
		}
	}
	return writeTinyEPUBWithOPF(t, fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/">
	<metadata>
		%s
	</metadata>
</package>`, metadataXML))
}

func writeTinyEPUBWithOPF(t *testing.T, opf string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "book.epub")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	defer file.Close()

	archive := zip.NewWriter(file)
	defer archive.Close()

	writeZipFile(t, archive, "META-INF/container.xml", `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
	<rootfiles>
		<rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
	</rootfiles>
</container>`)

	writeZipFile(t, archive, "OEBPS/content.opf", opf)

	return path
}

func writeZipFile(t *testing.T, archive *zip.Writer, name string, contents string) {
	t.Helper()

	writer, err := archive.Create(name)
	if err != nil {
		t.Fatalf("create zip entry %s: %v", name, err)
	}
	if _, err := writer.Write([]byte(contents)); err != nil {
		t.Fatalf("write zip entry %s: %v", name, err)
	}
}
