package scanner

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
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

func TestNormalizeEbookISBNHandlesCommonLabels(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ISBN-13: 978-0-306-40615-7", "9780306406157"},
		{"ISBN-10: 0-9752298-0-x", "097522980X"},
	}
	for _, tc := range cases {
		if got := normalizeEbookISBN(tc.in); got != tc.want {
			t.Errorf("normalizeEbookISBN(%q) = %q, want %q", tc.in, got, tc.want)
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

func TestEbookIdentityConfidenceReflectsMetadataCompleteness(t *testing.T) {
	book := &parsedEbook{Title: "Tagged Ebook", Authors: []string{"Author"}, Year: 2024, ISBN: "9780306406157"}
	if got := ebookIdentityConfidence(book); got != "high" {
		t.Fatalf("complete metadata confidence = %q, want high", got)
	}

	book = &parsedEbook{Title: "Tagged Ebook", Authors: []string{"Author"}, Year: 2024}
	if got := ebookIdentityConfidence(book); got != "medium" {
		t.Fatalf("partial metadata confidence = %q, want medium", got)
	}

	book = &parsedEbook{}
	if got := ebookIdentityConfidence(book); got != "low" {
		t.Fatalf("empty metadata confidence = %q, want low", got)
	}
}

func TestResolveEbookMediaItemCreatesNewWhenRootHasNoClaim(t *testing.T) {
	finder := &fakeRootContentFinder{}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolveEbookMediaItem(context.Background(), finder, writer, 7, "/library/Author/Book.epub", &parsedEbook{
		Title: "Book", Year: 2024, Authors: []string{"Author"}, Description: "Overview", Publisher: "Publisher", Genres: []string{"Fiction"}, Language: "en",
	})
	if err != nil {
		t.Fatalf("resolveEbookMediaItem: %v", err)
	}
	if got == "" || len(writer.upserts) != 1 {
		t.Fatalf("contentID/upserts = %q/%d, want id and one upsert", got, len(writer.upserts))
	}
	item := writer.upserts[0]
	if item.Type != "ebook" || item.Title != "Book" || item.Year != 2024 || item.Overview != "Overview" || item.OriginalLanguage != "en" {
		t.Fatalf("upserted ebook item = %+v", item)
	}
}

func TestResolveEbookMediaItemReusesRootScopedContentID(t *testing.T) {
	finder := &fakeRootContentFinder{contentID: "ebook-root-id"}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolveEbookMediaItem(context.Background(), finder, writer, 7, "/library/Author/Book.epub", &parsedEbook{Title: "Book"})
	if err != nil {
		t.Fatalf("resolveEbookMediaItem: %v", err)
	}
	if got != "ebook-root-id" {
		t.Fatalf("contentID = %q, want root-scoped id", got)
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("unexpected item upsert for existing root: %d", len(writer.upserts))
	}
}

func TestEbookPeopleCreditsEqualAuthorsOnly(t *testing.T) {
	existing := []models.ItemPerson{
		{Person: models.Person{Name: "Ada Writer"}, Kind: models.PersonKindAuthor, SortOrder: 0},
		{Person: models.Person{Name: "Ben Author"}, Kind: models.PersonKindAuthor, SortOrder: 1},
	}
	desired := []ebookCredit{
		{Name: "Ada Writer", Kind: models.PersonKindAuthor},
		{Name: "Ben Author", Kind: models.PersonKindAuthor},
	}
	if !ebookPeopleCreditsEqual(existing, desired) {
		t.Fatal("expected matching author credits to compare equal")
	}

	existing = append(existing, models.ItemPerson{Person: models.Person{Name: "Narrator"}, Kind: models.PersonKindNarrator, SortOrder: 2})
	if ebookPeopleCreditsEqual(existing, desired) {
		t.Fatal("expected narrator credit to make ebook author-only set differ")
	}
}

func TestScanEbookFolderReturnsErrorWhenEveryReconcileFails(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad.epub"), []byte("not a real epub"), 0o644); err != nil {
		t.Fatalf("write fake ebook: %v", err)
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

func TestScanEbookFolderReturnsCanceledContext(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &Scanner{}
	err := s.ScanEbookFolder(ctx, &models.MediaFolder{ID: 44, Paths: []string{root}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanEbookFolder error = %v, want context.Canceled", err)
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
