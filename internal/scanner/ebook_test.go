package scanner

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"testing"
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
		ASIN:      " b0-0 ab ",
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
	if book.ASIN != "B00AB" {
		t.Fatalf("asin = %q, want B00AB", book.ASIN)
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

	metadataXML := ""
	for tag, value := range metadata {
		metadataXML += fmt.Sprintf("<%s>%s</%s>\n", tag, value, tag)
	}
	writeZipFile(t, archive, "OEBPS/content.opf", fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/">
	<metadata>
		%s
	</metadata>
</package>`, metadataXML))

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
