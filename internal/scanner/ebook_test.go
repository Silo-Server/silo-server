package scanner

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSupportsEbookFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"book.epub", true},
		{"book.PDF", true},
		{"book.mobi", true},
		{"book.azw", true},
		{"book.azw3", true},
		{"book.FB2", true},
		{"book.mp3", false},
		{"movie.mkv", false},
	}
	for _, tc := range cases {
		if got := SupportsEbookFile(tc.path); got != tc.want {
			t.Errorf("SupportsEbookFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestParseEbookEPUBMetadata(t *testing.T) {
	path := writeTestEPUB(t, []string{"ISBN: 978-0-306-40615-7"})

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Format != "epub" {
		t.Fatalf("Format = %q, want epub", got.Format)
	}
	if got.Title != "The Test Ebook" {
		t.Fatalf("Title = %q, want The Test Ebook", got.Title)
	}
	if strings.Join(got.Authors, ", ") != "Ada Writer, Ben Author" {
		t.Fatalf("Authors = %v, want Ada Writer and Ben Author", got.Authors)
	}
	if got.ISBN != "9780306406157" {
		t.Fatalf("ISBN = %q, want normalized ISBN", got.ISBN)
	}
	if got.Year != 2024 {
		t.Fatalf("Year = %d, want 2024", got.Year)
	}
	if got.Publisher != "Silo Press" {
		t.Fatalf("Publisher = %q, want Silo Press", got.Publisher)
	}
	wantPublishedAt := time.Date(2024, 3, 10, 0, 0, 0, 0, time.UTC)
	if !got.PublishedAt.Equal(wantPublishedAt) {
		t.Fatalf("PublishedAt = %v, want %v", got.PublishedAt, wantPublishedAt)
	}
	if got.Language != "en" {
		t.Fatalf("Language = %q, want en", got.Language)
	}
	if strings.Join(got.Genres, ", ") != "Fiction, Adventure" {
		t.Fatalf("Genres = %v, want Fiction and Adventure", got.Genres)
	}
	if got.Series != "Test Series" || got.SeriesIndex != "2" {
		t.Fatalf("Series = %q/%q, want Test Series/2", got.Series, got.SeriesIndex)
	}
}

func TestParseEbookEPUBSkipsUUIDIdentifierBeforeISBN(t *testing.T) {
	path := writeTestEPUB(t, []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"ISBN: 978-0-306-40615-7",
	})

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}
	if got.ISBN != "9780306406157" {
		t.Fatalf("ISBN = %q, want normalized ISBN after skipping UUID", got.ISBN)
	}
}

func TestParseEbookFB2Metadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.fb2")
	if err := os.WriteFile(path, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<FictionBook>
  <description>
    <title-info>
      <genre>science fiction</genre>
      <author><first-name>Ada</first-name><last-name>Writer</last-name></author>
      <book-title>FB2 Test Ebook</book-title>
      <date value="2022-04-05">5 April 2022</date>
      <lang>en</lang>
      <sequence name="FB2 Series" number="3"/>
    </title-info>
    <publish-info>
      <publisher>FB2 Press</publisher>
      <year>2022</year>
      <isbn>978-0-306-40615-7</isbn>
    </publish-info>
  </description>
</FictionBook>`), 0o644); err != nil {
		t.Fatalf("write fb2: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Format != "fb2" || got.Title != "FB2 Test Ebook" {
		t.Fatalf("Format/Title = %q/%q, want fb2/FB2 Test Ebook", got.Format, got.Title)
	}
	if strings.Join(got.Authors, ", ") != "Ada Writer" {
		t.Fatalf("Authors = %v, want Ada Writer", got.Authors)
	}
	if got.ISBN != "9780306406157" {
		t.Fatalf("ISBN = %q, want normalized ISBN", got.ISBN)
	}
	if got.Publisher != "FB2 Press" {
		t.Fatalf("Publisher = %q, want FB2 Press", got.Publisher)
	}
	if got.Year != 2022 {
		t.Fatalf("Year = %d, want 2022", got.Year)
	}
	wantPublishedAt := time.Date(2022, 4, 5, 0, 0, 0, 0, time.UTC)
	if !got.PublishedAt.Equal(wantPublishedAt) {
		t.Fatalf("PublishedAt = %v, want %v", got.PublishedAt, wantPublishedAt)
	}
	if got.Language != "en" {
		t.Fatalf("Language = %q, want en", got.Language)
	}
	if strings.Join(got.Genres, ", ") != "science fiction" {
		t.Fatalf("Genres = %v, want science fiction", got.Genres)
	}
	if got.Series != "FB2 Series" || got.SeriesIndex != "3" {
		t.Fatalf("Series = %q/%q, want FB2 Series/3", got.Series, got.SeriesIndex)
	}
}

func writeTestEPUB(t *testing.T, identifiers []string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "book.epub")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	zw := zip.NewWriter(file)
	add := func(name, body string) {
		t.Helper()
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	add("META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)
	var identifierXML strings.Builder
	for _, identifier := range identifiers {
		identifierXML.WriteString("    <dc:identifier>")
		identifierXML.WriteString(identifier)
		identifierXML.WriteString("</dc:identifier>\n")
	}
	add("OPS/content.opf", `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
  <metadata>
    <dc:title>The Test Ebook</dc:title>
    <dc:creator>Ada Writer</dc:creator>
    <dc:creator>Ben Author</dc:creator>
`+identifierXML.String()+`
    <dc:publisher>Silo Press</dc:publisher>
    <dc:date>2024-03-10</dc:date>
    <dc:language>en</dc:language>
    <dc:subject>Fiction</dc:subject>
    <dc:subject>Adventure</dc:subject>
    <dc:description>Back cover copy</dc:description>
    <meta name="calibre:series" content="Test Series"/>
    <meta name="calibre:series_index" content="2"/>
  </metadata>
</package>`)
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}
	return path
}
