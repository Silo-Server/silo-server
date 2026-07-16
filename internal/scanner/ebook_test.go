package scanner

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const ebookCoverTestThumbhash = "thumb"

type recordingEbookExecutor struct {
	queries []string
	args    [][]any
}

func (r *recordingEbookExecutor) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	r.queries = append(r.queries, query)
	r.args = append(r.args, append([]any(nil), args...))
	return pgconn.CommandTag{}, nil
}

type fakeFilesystemItemReader struct {
	items []*models.MediaItem
	err   error
}

func (f *fakeFilesystemItemReader) GetByIDs(_ context.Context, _ []string) ([]*models.MediaItem, error) {
	return f.items, f.err
}

type fakeEbookCoverCacher struct {
	calls     int
	data      []byte
	contentID string
	err       error
}

func (f *fakeEbookCoverCacher) CacheEbookCover(_ context.Context, data []byte, contentID string) (string, string, error) {
	f.calls++
	f.data = append([]byte(nil), data...)
	f.contentID = contentID
	if f.err != nil {
		return "", "", f.err
	}
	return "local/ebooks/" + contentID + "/poster/original.test-revision.webp", ebookCoverTestThumbhash, nil
}

type fakeEbookMetadataUpdater struct {
	posterPath      string
	posterThumbhash string
	getErr          error
	contentID       string
	setPosterPath   string
	setThumbhash    string
	setPrefix       string
	setCalls        int
	err             error
}

func (f *fakeEbookMetadataUpdater) GetPoster(_ context.Context, _ string) (string, string, error) {
	return f.posterPath, f.posterThumbhash, f.getErr
}

func (f *fakeEbookMetadataUpdater) SetLocalPoster(_ context.Context, contentID, posterPath, thumbhash, localPrefix string) (bool, error) {
	f.setCalls++
	f.contentID = contentID
	f.setPosterPath = posterPath
	f.setThumbhash = thumbhash
	f.setPrefix = localPrefix
	return f.err == nil, f.err
}

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
		{"book.fb2.zip", true},
		{"book.fbz", true},
		{"book.cbz", true},
		{"book.cbr", true},
		{"book.txt", false},
		{"book.md", false},
		{"README.md", false},
		{"book.mp3", false},
		{"movie.mkv", false},
	}
	for _, tc := range cases {
		if got := SupportsEbookFile(tc.path); got != tc.want {
			t.Errorf("SupportsEbookFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestParseEbookFileSupportsReaderFormatsWithoutEmbeddedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wantFormat string
	}{
		{"book.pdf", "pdf"},
		{"book.mobi", "mobi"},
		{"book.azw", "azw"},
		{"book.azw3", "azw3"},
		{"book.cbr", "cbr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name)
			if err := os.WriteFile(path, []byte("placeholder"), 0o644); err != nil {
				t.Fatalf("write file: %v", err)
			}
			got, err := parseEbookFile(path)
			if err != nil {
				t.Fatalf("parseEbookFile: %v", err)
			}
			if got.Format != tc.wantFormat {
				t.Fatalf("Format = %q, want %q", got.Format, tc.wantFormat)
			}
		})
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

func TestParseEbookEPUBMetadataStripsHTMLDescription(t *testing.T) {
	path := writeTestEPUBWithDescription(t, `&lt;div&gt;&lt;p&gt;&lt;strong&gt;Shanghai Dreams ...&lt;/strong&gt; Magnus is stationed at Shanghai.&lt;br&gt;Published by The Electronic Book Company&lt;/p&gt;&lt;/div&gt;`)

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	want := "Shanghai Dreams ... Magnus is stationed at Shanghai. Published by The Electronic Book Company"
	if got.Description != want {
		t.Fatalf("Description = %q, want %q", got.Description, want)
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

func TestParseEbookEPUBMetadataReadsISBNFromMetaTags(t *testing.T) {
	path := writeTestEPUBWithMeta(t, nil, `    <meta name="calibre:isbn" content="978-0-306-40615-7"/>`)

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}
	if got.ISBN != "9780306406157" {
		t.Fatalf("ISBN = %q, want normalized ISBN from calibre meta tag", got.ISBN)
	}
}

func TestParseEbookEPUBMetadataHandlesLatin1OPF(t *testing.T) {
	path := writeTestEPUBWithOPFBytes(t, []byte("<?xml version=\"1.0\" encoding=\"iso-8859-1\"?>\n"+
		"<package xmlns:dc=\"http://purl.org/dc/elements/1.1/\"><metadata>"+
		"<dc:title>Caf\xe9 Society</dc:title>"+
		"<dc:creator>Fran\xe7ois Author</dc:creator>"+
		"<dc:publisher>Cr\xe8me Press</dc:publisher>"+
		"</metadata></package>"))

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Title != "Café Society" || strings.Join(got.Authors, ", ") != "François Author" || got.Publisher != "Crème Press" {
		t.Fatalf("parsed latin1 metadata = title %q authors %v publisher %q", got.Title, got.Authors, got.Publisher)
	}
}

func TestParseEbookEPUBMetadataHandlesWindows1251OPF(t *testing.T) {
	path := writeTestEPUBWithOPFBytes(t, []byte("<?xml version=\"1.0\" encoding=\"windows-1251\"?>\n"+
		"<package xmlns:dc=\"http://purl.org/dc/elements/1.1/\"><metadata>"+
		"<dc:title>\xc2\xee\xe9\xed\xe0 \xe8 \xec\xe8\xf0</dc:title>"+
		"<dc:creator>\xd2\xee\xeb\xf1\xf2\xee\xe9</dc:creator>"+
		"</metadata></package>"))

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Title != "Война и мир" || strings.Join(got.Authors, ", ") != "Толстой" {
		t.Fatalf("parsed windows-1251 metadata = title %q authors %v", got.Title, got.Authors)
	}
}

func TestParseEbookEPUBMetadataAllowsXMLVersion11(t *testing.T) {
	path := writeTestEPUBWithOPFBytes(t, []byte(`<?xml version="1.1" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/"><metadata>
  <dc:title>Versioned Ebook</dc:title>
  <dc:creator>Ada Writer</dc:creator>
</metadata></package>`))

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Title != "Versioned Ebook" || strings.Join(got.Authors, ", ") != "Ada Writer" {
		t.Fatalf("parsed XML 1.1 metadata = title %q authors %v", got.Title, got.Authors)
	}
}

func TestParseEbookEPUBExtractsManifestCover(t *testing.T) {
	path := writeTestEPUBWithCover(t, "Images/cover.jpg", "image/jpeg", []byte("epub-cover-bytes"))

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Cover == nil {
		t.Fatal("Cover = nil, want embedded EPUB cover")
	}
	if got.Cover.ContentType != "image/jpeg" {
		t.Fatalf("Cover.ContentType = %q, want image/jpeg", got.Cover.ContentType)
	}
	if string(got.Cover.Bytes) != "epub-cover-bytes" {
		t.Fatalf("Cover.Bytes = %q, want EPUB cover bytes", string(got.Cover.Bytes))
	}
}

func TestParseEbookEPUBCoverSkipsXHTMLCoverPage(t *testing.T) {
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
	// EPUB2-style meta cover pointing at the XHTML cover *page*, with the
	// real image declared later via properties="cover-image".
	add("OPS/content.opf", `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
  <metadata>
    <dc:title>The Test Ebook</dc:title>
    <meta name="cover" content="cover"/>
  </metadata>
  <manifest>
    <item id="cover" href="cover.xhtml" media-type="application/xhtml+xml"/>
    <item id="cover-img" href="Images/cover.jpg" media-type="image/jpeg" properties="cover-image"/>
  </manifest>
</package>`)
	add("OPS/cover.xhtml", "<html><body><img src='Images/cover.jpg'/></body></html>")
	add("OPS/Images/cover.jpg", "real-cover-bytes")
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Cover == nil {
		t.Fatal("Cover = nil, want cover-image manifest item")
	}
	if got.Cover.ContentType != "image/jpeg" || string(got.Cover.Bytes) != "real-cover-bytes" {
		t.Fatalf("Cover = %q/%q, want the image item, not the XHTML cover page", got.Cover.ContentType, string(got.Cover.Bytes))
	}
}

func TestReadEPUBZipEntryRejectsOversizedEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.epub")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	zw := zip.NewWriter(file)
	w, err := zw.Create("OPS/content.opf")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write([]byte(strings.Repeat("x", maxEPUBMetadataEntrySize+1))); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer reader.Close()

	_, err = readEPUBZipEntry(&reader.Reader, "OPS/content.opf")
	if err == nil || !strings.Contains(err.Error(), "epub entry too large") {
		t.Fatalf("readEPUBZipEntry error = %v, want size limit error", err)
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

func TestParseEbookFBZMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.fb2.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fbz: %v", err)
	}
	zw := zip.NewWriter(file)
	w, err := zw.Create("nested/book.fb2")
	if err != nil {
		t.Fatalf("create fb2 entry: %v", err)
	}
	if _, err := w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<FictionBook>
  <description>
    <title-info>
      <genre>fantasy</genre>
      <author><first-name>Ada</first-name><last-name>Writer</last-name></author>
      <book-title>FBZ Test Ebook</book-title>
      <date value="2021-02-03"/>
      <lang>en</lang>
      <sequence name="Archive Series" number="4"/>
    </title-info>
    <publish-info>
      <publisher>Archive Press</publisher>
      <year>2021</year>
      <isbn>978-0-306-40615-7</isbn>
    </publish-info>
  </description>
</FictionBook>`)); err != nil {
		t.Fatalf("write fb2 entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close fbz: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Format != "fbz" || got.Title != "FBZ Test Ebook" {
		t.Fatalf("Format/Title = %q/%q, want fbz/FBZ Test Ebook", got.Format, got.Title)
	}
	if strings.Join(got.Authors, ", ") != "Ada Writer" {
		t.Fatalf("Authors = %v, want Ada Writer", got.Authors)
	}
	if got.ISBN != "9780306406157" {
		t.Fatalf("ISBN = %q, want normalized ISBN", got.ISBN)
	}
	if got.Publisher != "Archive Press" || got.Year != 2021 {
		t.Fatalf("Publisher/Year = %q/%d, want Archive Press/2021", got.Publisher, got.Year)
	}
	if got.Series != "Archive Series" || got.SeriesIndex != "4" {
		t.Fatalf("Series = %q/%q, want Archive Series/4", got.Series, got.SeriesIndex)
	}
}

func TestParseEbookPDFInfoMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(path, []byte(`%PDF-1.7
1 0 obj
<< /Title (PDF Test Ebook)
   /Author (Ada Writer; Ben Author)
   /Subject (ISBN 978-0-306-40615-7)
   /Keywords (science fiction, adventure)
   /CreationDate (D:20240102030405Z)
>>
endobj
trailer
<< /Info 1 0 R >>
%%EOF`), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Format != "pdf" || got.Title != "PDF Test Ebook" {
		t.Fatalf("Format/Title = %q/%q, want pdf/PDF Test Ebook", got.Format, got.Title)
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
	if strings.Join(got.Genres, ", ") != "science fiction, adventure" {
		t.Fatalf("Genres = %v, want science fiction and adventure", got.Genres)
	}
}

func TestParseEbookPDFInfoMetadataDecodesUTF16BELiterals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\n"+
		"1 0 obj\n"+
		"<< /Title (\xfe\xff\x00C\x00a\x00f\x00e)\n"+
		"   /Author (\xfe\xff\x00T\x00h\x00o\x00m\x00a\x00s\x00 \x00D\x00 \x00S\x00e\x00e\x00l\x00e\x00y)\n"+
		">>\nendobj\n%%EOF"), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Title != "Cafe" || strings.Join(got.Authors, ", ") != "Thomas D Seeley" {
		t.Fatalf("PDF metadata = title %q authors %v, want decoded UTF-16BE", got.Title, got.Authors)
	}
}

func TestParseEbookPDFInfoMetadataPreservesUTF8Literals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\n"+
		"1 0 obj\n"+
		"<< /Title (Caf\xc3\xa9 Society)\n"+
		"   /Author (\xef\xbb\xbfFran\xc3\xa7ois Author)\n"+
		">>\nendobj\n%%EOF"), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Title != "Café Society" {
		t.Fatalf("Title = %q, want UTF-8 literal preserved without Windows-1252 mojibake", got.Title)
	}
	if strings.Join(got.Authors, ", ") != "François Author" {
		t.Fatalf("Authors = %v, want UTF-8 BOM stripped and bytes preserved", got.Authors)
	}
}

func TestParseEbookPDFInfoMetadataFallsBackToWindows1252(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\n"+
		"1 0 obj\n"+
		"<< /Title (Caf\xe9 Society)\n"+
		">>\nendobj\n%%EOF"), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Title != "Café Society" {
		t.Fatalf("Title = %q, want Windows-1252 fallback for non-UTF-8 literal", got.Title)
	}
}

func TestParseEbookPDFInfoMetadataDecodesHexStrings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(path, []byte(`%PDF-1.7
1 0 obj
<< /Title <FEFF00480065007800200042006F006F006B>
   /Author <41646120577269746572>
   /CreationDate (0000-01-01)
>>
endobj
%%EOF`), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Title != "Hex Book" || strings.Join(got.Authors, ", ") != "Ada Writer" {
		t.Fatalf("PDF hex metadata = title %q authors %v", got.Title, got.Authors)
	}
	if got.Year != 0 || !got.PublishedAt.IsZero() {
		t.Fatalf("bad PDF date produced year/date = %d/%v, want ignored", got.Year, got.PublishedAt)
	}
}

func TestParseEbookCBZPageCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comic.cbz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create cbz: %v", err)
	}
	zw := zip.NewWriter(file)
	for _, name := range []string{
		"Comic/001.jpg",
		"Comic/002.PNG",
		"Comic/003.webp",
		"Comic/notes.txt",
		"__MACOSX/._001.jpg",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte("placeholder")); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close cbz: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Format != "cbz" {
		t.Fatalf("Format = %q, want cbz", got.Format)
	}
	if got.PageCount != 3 {
		t.Fatalf("PageCount = %d, want 3", got.PageCount)
	}
}

func TestParseEbookCBZExtractsFirstReadablePageAsCover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comic.cbz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create cbz: %v", err)
	}
	zw := zip.NewWriter(file)
	for _, entry := range []struct {
		name string
		body []byte
	}{
		{name: "Comic/002.png", body: []byte("second-page")},
		{name: "Comic/001.jpg", body: []byte("first-page")},
		{name: "Comic/notes.txt", body: []byte("ignored")},
	} {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", entry.name, err)
		}
		if _, err := w.Write(entry.body); err != nil {
			t.Fatalf("write zip entry %s: %v", entry.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close cbz: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Cover == nil {
		t.Fatal("Cover = nil, want first CBZ page as cover")
	}
	if got.Cover.ContentType != "image/jpeg" {
		t.Fatalf("Cover.ContentType = %q, want image/jpeg", got.Cover.ContentType)
	}
	if string(got.Cover.Bytes) != "first-page" {
		t.Fatalf("Cover.Bytes = %q, want first page bytes", string(got.Cover.Bytes))
	}
}

func TestParseEbookCBZPicksNaturallyFirstPageAsCover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comic.cbz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create cbz: %v", err)
	}
	zw := zip.NewWriter(file)
	for _, entry := range []struct {
		name string
		body string
	}{
		{name: "ch10/page10.jpg", body: "chapter-ten"},
		{name: "ch2/page2.jpg", body: "chapter-two"},
	} {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", entry.name, err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatalf("write zip entry %s: %v", entry.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close cbz: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}

	if got.Cover == nil {
		t.Fatal("Cover = nil, want first page in natural order")
	}
	if string(got.Cover.Bytes) != "chapter-two" {
		t.Fatalf("Cover.Bytes = %q, want ch2 before ch10 (natural order, not byte order)", string(got.Cover.Bytes))
	}
}

func TestEbookTitleFromPathStripsCompoundFB2Extension(t *testing.T) {
	cases := map[string]string{
		"/lib/Anna Karenina.fb2.zip": "Anna Karenina",
		"/lib/Anna Karenina.FB2.ZIP": "Anna Karenina",
		"/lib/Book.epub":             "Book",
		"/lib/vol.1.pdf":             "vol.1",
	}
	for path, want := range cases {
		if got := ebookTitleFromPath(path); got != want {
			t.Errorf("ebookTitleFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestEbookSeriesDesiredParsesIndex(t *testing.T) {
	name, idx := ebookSeriesDesired(&parsedEbook{
		Series:      " The Expanse ",
		SeriesIndex: "2 of 9",
	})

	if name != "The Expanse" {
		t.Fatalf("series name = %q, want The Expanse", name)
	}
	if idx == nil || *idx != 2 {
		t.Fatalf("series index = %v, want 2", idx)
	}
}

func TestUpsertEbookSeriesNilScannerReturnsError(t *testing.T) {
	var s *Scanner
	if err := s.upsertEbookSeries(context.Background(), "content-1", &parsedEbook{Series: "Series"}, false); err == nil {
		t.Fatal("upsertEbookSeries nil scanner error = nil, want error")
	}
}

func TestUpsertEbookSeriesNilFileRepoReturnsError(t *testing.T) {
	s := &Scanner{}
	if err := s.upsertEbookSeries(context.Background(), "content-1", &parsedEbook{Series: "Series"}, false); err == nil {
		t.Fatal("upsertEbookSeries nil fileRepo error = nil, want error")
	}
}

func TestPlanEbookSeriesWriteInsertsWhenRowAbsent(t *testing.T) {
	plan, err := planEbookSeriesWrite(&parsedEbook{Series: " Series ", SeriesIndex: "2 of 9"}, nil, nil, pgx.ErrNoRows, false)
	if err != nil {
		t.Fatalf("planEbookSeriesWrite: %v", err)
	}
	if plan.Kind != ebookSeriesWriteUpsert || plan.Name != "Series" {
		t.Fatalf("plan = %+v, want upsert Series", plan)
	}
	if plan.Index == nil || *plan.Index != 2 {
		t.Fatalf("index = %v, want 2", plan.Index)
	}
}

func TestPlanEbookSeriesWriteBlankSeriesDeletesExistingAndSkipsAbsent(t *testing.T) {
	currentName := "Series"
	plan, err := planEbookSeriesWrite(&parsedEbook{Series: " "}, &currentName, nil, nil, false)
	if err != nil {
		t.Fatalf("planEbookSeriesWrite delete: %v", err)
	}
	if plan.Kind != ebookSeriesWriteDelete {
		t.Fatalf("blank existing plan = %+v, want delete", plan)
	}

	plan, err = planEbookSeriesWrite(&parsedEbook{Series: ""}, nil, nil, pgx.ErrNoRows, false)
	if err != nil {
		t.Fatalf("planEbookSeriesWrite absent: %v", err)
	}
	if plan.Kind != ebookSeriesWriteNone {
		t.Fatalf("blank absent plan = %+v, want none", plan)
	}
}

func TestPlanEbookSeriesWriteSkipsIdenticalRow(t *testing.T) {
	currentName := "Series"
	currentIdx := 3.5
	plan, err := planEbookSeriesWrite(&parsedEbook{Series: "Series", SeriesIndex: "3.5"}, &currentName, &currentIdx, nil, false)
	if err != nil {
		t.Fatalf("planEbookSeriesWrite: %v", err)
	}
	if plan.Kind != ebookSeriesWriteNone {
		t.Fatalf("identical plan = %+v, want none", plan)
	}
}

func TestPlanEbookSeriesWriteAllowsNullNumericIndex(t *testing.T) {
	plan, err := planEbookSeriesWrite(&parsedEbook{Series: "Series", SeriesIndex: "appendix"}, nil, nil, pgx.ErrNoRows, false)
	if err != nil {
		t.Fatalf("planEbookSeriesWrite: %v", err)
	}
	if plan.Kind != ebookSeriesWriteUpsert || plan.Index != nil {
		t.Fatalf("plan = %+v, want upsert with nil index", plan)
	}
}

func TestPlanEbookSeriesWriteFillOnlyNeverReplacesOrDeletesExistingRow(t *testing.T) {
	currentName := "Provider Series"
	currentIdx := 1.0

	// A curated item with an existing (provider-enriched) row must not be
	// replaced by a differing file-embedded series...
	plan, err := planEbookSeriesWrite(&parsedEbook{Series: "File Series", SeriesIndex: "4"}, &currentName, &currentIdx, nil, true)
	if err != nil {
		t.Fatalf("planEbookSeriesWrite replace: %v", err)
	}
	if plan.Kind != ebookSeriesWriteNone {
		t.Fatalf("fill-only replace plan = %+v, want none", plan)
	}

	// ...nor deleted when the file carries no series at all.
	plan, err = planEbookSeriesWrite(&parsedEbook{Series: ""}, &currentName, &currentIdx, nil, true)
	if err != nil {
		t.Fatalf("planEbookSeriesWrite delete: %v", err)
	}
	if plan.Kind != ebookSeriesWriteNone {
		t.Fatalf("fill-only delete plan = %+v, want none", plan)
	}
}

func TestPlanEbookSeriesWriteFillOnlyInsertsWhenRowAbsent(t *testing.T) {
	plan, err := planEbookSeriesWrite(&parsedEbook{Series: "File Series", SeriesIndex: "2"}, nil, nil, pgx.ErrNoRows, true)
	if err != nil {
		t.Fatalf("planEbookSeriesWrite: %v", err)
	}
	if plan.Kind != ebookSeriesWriteUpsert || plan.Name != "File Series" {
		t.Fatalf("plan = %+v, want fill-empty upsert", plan)
	}
}

func TestEbookPeopleWriteAllowedFillsEmptyOnlyWhenCurated(t *testing.T) {
	providerAuthor := []models.ItemPerson{{Person: models.Person{Name: "Provider Author"}, Kind: models.PersonKindAuthor}}
	nonAuthorOnly := []models.ItemPerson{{Person: models.Person{Name: "Manual Writer"}, Kind: models.PersonKindWriter}}

	cases := []struct {
		name     string
		curated  bool
		existing []models.ItemPerson
		want     bool
	}{
		{"pending item always writes", false, providerAuthor, true},
		{"curated with provider authors is read-only", true, providerAuthor, false},
		{"curated without author credits fills empty", true, nil, true},
		{"curated with only non-author credits fills empty", true, nonAuthorOnly, true},
	}
	for _, tc := range cases {
		if got := ebookPeopleWriteAllowed(tc.curated, tc.existing); got != tc.want {
			t.Errorf("%s: ebookPeopleWriteAllowed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPlanEbookSeriesWriteReturnsQueryError(t *testing.T) {
	queryErr := errors.New("query failed")
	if _, err := planEbookSeriesWrite(&parsedEbook{Series: "Series"}, nil, nil, queryErr, false); !errors.Is(err, queryErr) {
		t.Fatalf("planEbookSeriesWrite error = %v, want query error", err)
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

func TestEbookContentGroupKeyGroupsMultipleSiblingFormatsByTitleAuthor(t *testing.T) {
	want := ebookContentGroupKey(&parsedEbook{
		Format:  "epub",
		Title:   " The Test Ebook ",
		Authors: []string{"Ada Writer", "Ben Author"},
	}, "/library/Ada Writer/The Test Ebook.epub")
	if want == "" {
		t.Fatal("ebookContentGroupKey returned blank")
	}
	for _, tc := range []struct {
		format string
		path   string
	}{
		{format: "mobi", path: "/library/Ada Writer/The Test Ebook.mobi"},
		{format: "azw3", path: "/library/Ada Writer/The Test Ebook.azw3"},
		{format: "pdf", path: "/library/Ada Writer/The Test Ebook.pdf"},
		{format: "fb2", path: "/library/Ada Writer/The Test Ebook.fb2"},
	} {
		got := ebookContentGroupKey(&parsedEbook{
			Format:  tc.format,
			Title:   "The Test Ebook",
			Authors: []string{"ben author", "ada writer"},
		}, tc.path)
		if got != want {
			t.Fatalf("%s content group key = %q, want %q", tc.format, got, want)
		}
		if strings.Contains(got, tc.format) {
			t.Fatalf("content group key should not include file format %q: %q", tc.format, got)
		}
	}
}

func TestEbookContentGroupKeyPrefersISBNAcrossTitleVariants(t *testing.T) {
	first := ebookContentGroupKey(&parsedEbook{
		Format:  "epub",
		Title:   "The Test Ebook",
		Authors: []string{"Ada Writer"},
		ISBN:    "9780306406157",
	}, "/library/Ada Writer/The Test Ebook.epub")
	second := ebookContentGroupKey(&parsedEbook{
		Format:  "pdf",
		Title:   "The Test Ebook: Revised Edition",
		Authors: []string{"Someone Else"},
		ISBN:    "9780306406157",
	}, "/library/Other Name.pdf")

	if first != second {
		t.Fatalf("ISBN-backed content group keys differ: %q != %q", first, second)
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
	if item.Status != "pending" {
		t.Fatalf("Status = %q, want pending so enrichment can promote it to matched", item.Status)
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

func TestResolveEbookExistingRootAppliesParsedMetadata(t *testing.T) {
	reader := &fakeFilesystemItemReader{items: []*models.MediaItem{{
		ContentID:        "ebook-root-id",
		Type:             "ebook",
		Title:            "Old Title",
		Year:             2020,
		OriginalLanguage: "",
	}}}
	writer := &fakeFilesystemItemWriter{}

	curated, err := updateExistingEbookMediaItem(context.Background(), reader, writer, "ebook-root-id", &parsedEbook{
		Title:     "New Title",
		Year:      2026,
		Publisher: "New Press",
		Genres:    []string{"Fiction"},
		Language:  "en",
	})
	if err != nil {
		t.Fatalf("updateExistingEbookMediaItem: %v", err)
	}
	if curated {
		t.Fatal("curated = true, want false for an unmatched item")
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(writer.upserts))
	}
	item := writer.upserts[0]
	if item.ContentID != "ebook-root-id" || item.Type != "ebook" || item.Title != "New Title" || item.Year != 2026 || item.OriginalLanguage != "en" {
		t.Fatalf("updated item = %+v", item)
	}
	if strings.Join(item.Studios, ",") != "New Press" || strings.Join(item.Genres, ",") != "Fiction" {
		t.Fatalf("updated item studios/genres = %+v/%+v", item.Studios, item.Genres)
	}
}

func TestResolveEbookExistingRootReplacesHTMLOverview(t *testing.T) {
	reader := &fakeFilesystemItemReader{items: []*models.MediaItem{{
		ContentID: "ebook-root-id",
		Type:      "ebook",
		Title:     "Old Title",
		Overview:  "<div><p>Raw overview</p></div>",
	}}}
	writer := &fakeFilesystemItemWriter{}

	_, err := updateExistingEbookMediaItem(context.Background(), reader, writer, "ebook-root-id", &parsedEbook{
		Title:       "New Title",
		Description: "Clean overview",
	})
	if err != nil {
		t.Fatalf("updateExistingEbookMediaItem: %v", err)
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(writer.upserts))
	}
	if writer.upserts[0].Overview != "Clean overview" {
		t.Fatalf("Overview = %q, want Clean overview", writer.upserts[0].Overview)
	}
}

func TestApplyEbookToMediaItemPreservesExistingYearWhenParsedYearMissing(t *testing.T) {
	item := &models.MediaItem{
		Type:  "ebook",
		Title: "Existing",
		Year:  1999,
	}

	applyEbookToMediaItem(item, &parsedEbook{Title: "Updated", Year: 0})

	if item.Title != "Updated" {
		t.Fatalf("Title = %q, want Updated", item.Title)
	}
	if item.Year != 1999 {
		t.Fatalf("Year = %d, want preserved 1999", item.Year)
	}
}

func TestEbookPeopleCreditsEqualAuthorsOnly(t *testing.T) {
	existing := []models.ItemPerson{
		{Person: models.Person{Name: "Ada Writer"}, Kind: models.PersonKindAuthor, SortOrder: 0},
		{Person: models.Person{Name: "Ben Author"}, Kind: models.PersonKindAuthor, SortOrder: 1},
		{Person: models.Person{Name: "Manual Contributor"}, Kind: models.PersonKindWriter, SortOrder: 2},
	}
	desired := []ebookCredit{
		{Name: "Ada Writer", Kind: models.PersonKindAuthor},
		{Name: "Ben Author", Kind: models.PersonKindAuthor},
	}
	if !ebookPeopleCreditsEqual(existing, desired) {
		t.Fatal("expected matching author credits to compare equal while ignoring non-authors")
	}

	existing[1] = models.ItemPerson{Person: models.Person{Name: "Other Author"}, Kind: models.PersonKindAuthor, SortOrder: 1}
	if ebookPeopleCreditsEqual(existing, desired) {
		t.Fatal("expected different author credit to make ebook author set differ")
	}
}

func TestEbookPeopleCreditsEqualRejectsStaleNarratorCredit(t *testing.T) {
	existing := []models.ItemPerson{
		{Person: models.Person{Name: "Ada Writer"}, Kind: models.PersonKindAuthor, SortOrder: 0},
		{Person: models.Person{Name: "Legacy Narrator"}, Kind: models.PersonKindNarrator, SortOrder: 1},
	}
	desired := []ebookCredit{
		{Name: "Ada Writer", Kind: models.PersonKindAuthor},
	}

	if ebookPeopleCreditsEqual(existing, desired) {
		t.Fatal("expected stale narrator credit to force ebook people replacement")
	}
}

func TestEbookPeopleMergePreservesExistingNonAuthorCreditsExceptNarrators(t *testing.T) {
	existing := []models.ItemPerson{
		{Person: models.Person{ID: 10, Name: "Old Author"}, Kind: models.PersonKindAuthor, SortOrder: 0},
		{Person: models.Person{ID: 20, Name: "Manual Writer"}, Kind: models.PersonKindWriter, SortOrder: 1, Character: "essay"},
		{Person: models.Person{ID: 30, Name: "Manual Narrator"}, Kind: models.PersonKindNarrator, SortOrder: 2},
	}
	authors := []ebookResolvedAuthor{
		{ID: 40, Name: "New Author"},
	}

	got := mergeEbookPeople(existing, authors)
	if len(got) != 2 {
		t.Fatalf("merged people len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Person.ID != 20 || got[0].Kind != models.PersonKindWriter || got[0].Character != "essay" {
		t.Fatalf("first preserved non-author = %+v", got[0])
	}
	if got[1].Person.ID != 40 || got[1].Kind != models.PersonKindAuthor || got[1].SortOrder != 1 {
		t.Fatalf("new author credit = %+v", got[1])
	}
}

func TestEbookPeopleReplacePlanReturnsGetPeopleError(t *testing.T) {
	wantErr := errors.New("get people failed")
	_, err := ebookPeopleForReplace(nil, wantErr, []ebookResolvedAuthor{{ID: 40, Name: "New Author"}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestScanEbookBuildMediaFileSetsCorePersistenceFields(t *testing.T) {
	modifiedAt := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	book := &parsedEbook{Format: "epub", Title: "Book", Authors: []string{"Author"}, Year: 2024, ISBN: "9780306406157", PageCount: 321}

	got := buildEbookMediaFile(&models.MediaFolder{ID: 44}, "content-1", "/library/Book.epub", 1234, modifiedAt, book, ebookContentGroupKey(book, "/library/Book.epub"))
	if got.ContentID != "content-1" || got.MediaFolderID != 44 {
		t.Fatalf("ids = %q/%d, want content-1/44", got.ContentID, got.MediaFolderID)
	}
	if got.BaseType != "ebook" || got.BaseTitle != "Book" || got.BaseYear != 2024 || got.Container != "epub" || got.ProbeSource != "local" {
		t.Fatalf("ebook file metadata = %+v", got)
	}
	if got.Duration != 321 {
		t.Fatalf("Duration = %d, want ebook page count", got.Duration)
	}
	if got.CanonicalRootPath != "/library/Book.epub" || got.ObservedRootPath != "/library/Book.epub" || got.FilePath != "/library/Book.epub" {
		t.Fatalf("ebook paths = canonical %q observed %q file %q", got.CanonicalRootPath, got.ObservedRootPath, got.FilePath)
	}
	if got.ContentGroupKey != "ebook:isbn:9780306406157" {
		t.Fatalf("ContentGroupKey = %q, want ISBN-backed ebook group", got.ContentGroupKey)
	}
	if got.GroupKeyVersion != ebookGroupKeyVersion {
		t.Fatalf("GroupKeyVersion = %d, want %d", got.GroupKeyVersion, ebookGroupKeyVersion)
	}
	if got.FileSize != 1234 || got.FileModifiedAt == nil || !got.FileModifiedAt.Equal(modifiedAt) {
		t.Fatalf("file facts = size %d modified %v", got.FileSize, got.FileModifiedAt)
	}
}

func TestApplyEbookLocalCoverCachesEmbeddedAndSetsPoster(t *testing.T) {
	cacher := &fakeEbookCoverCacher{}
	updater := &fakeEbookMetadataUpdater{}

	err := applyEbookLocalCover(context.Background(), updater, cacher, "content-1", filepath.Join(t.TempDir(), "book.epub"), &parsedEbook{
		Cover: &parsedEbookCover{
			ContentType: "image/jpeg",
			Bytes:       []byte("cover-bytes"),
		},
	})
	if err != nil {
		t.Fatalf("applyEbookLocalCover: %v", err)
	}

	if cacher.calls != 1 || cacher.contentID != "content-1" || string(cacher.data) != "cover-bytes" {
		t.Fatalf("cache call = calls %d content %q data %q", cacher.calls, cacher.contentID, string(cacher.data))
	}
	if updater.setCalls != 1 || updater.contentID != "content-1" {
		t.Fatalf("set call = calls %d content %q", updater.setCalls, updater.contentID)
	}
	if updater.setPosterPath != "local/ebooks/content-1/poster/original.test-revision.webp" {
		t.Fatalf("poster path = %q", updater.setPosterPath)
	}
	if updater.setThumbhash != "thumb" {
		t.Fatalf("poster thumbhash = %q", updater.setThumbhash)
	}
	if updater.setPrefix != localEbookPosterPrefix {
		t.Fatalf("local prefix = %q, want %q", updater.setPrefix, localEbookPosterPrefix)
	}
}

func TestApplyEbookLocalCoverPreservesProviderPoster(t *testing.T) {
	cacher := &fakeEbookCoverCacher{}
	updater := &fakeEbookMetadataUpdater{posterPath: "ebook-metadata/ebooks/content-1/poster/original.webp"}

	err := applyEbookLocalCover(context.Background(), updater, cacher, "content-1", filepath.Join(t.TempDir(), "book.epub"), &parsedEbook{
		Cover: &parsedEbookCover{Bytes: []byte("cover-bytes")},
	})
	if err != nil {
		t.Fatalf("applyEbookLocalCover: %v", err)
	}

	if cacher.calls != 0 {
		t.Fatalf("cache calls = %d, want 0", cacher.calls)
	}
	if updater.setCalls != 0 {
		t.Fatalf("set calls = %d, want 0", updater.setCalls)
	}
}

func TestApplyEbookLocalCoverRefreshesStaleLocalPoster(t *testing.T) {
	cacher := &fakeEbookCoverCacher{}
	updater := &fakeEbookMetadataUpdater{
		posterPath:      "local/ebooks/content-1/poster/original.webp",
		posterThumbhash: "stale-thumb",
	}

	err := applyEbookLocalCover(context.Background(), updater, cacher, "content-1", filepath.Join(t.TempDir(), "book.epub"), &parsedEbook{
		Cover: &parsedEbookCover{Bytes: []byte("replacement-cover-bytes")},
	})
	if err != nil {
		t.Fatalf("applyEbookLocalCover: %v", err)
	}

	if cacher.calls != 1 || string(cacher.data) != "replacement-cover-bytes" {
		t.Fatalf("cache call = calls %d data %q, want refresh of locally owned poster", cacher.calls, string(cacher.data))
	}
	if updater.setCalls != 1 {
		t.Fatalf("set calls = %d, want 1", updater.setCalls)
	}
}

func TestApplyEbookLocalCoverPrefersSidecarOverEmbedded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "book.epub"), []byte("book"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("ebook-sidecar-cover"), 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}
	cacher := &fakeEbookCoverCacher{}
	updater := &fakeEbookMetadataUpdater{}

	err := applyEbookLocalCover(context.Background(), updater, cacher, "content-1", filepath.Join(dir, "book.epub"), &parsedEbook{
		Cover: &parsedEbookCover{Bytes: []byte("embedded-cover")},
	})
	if err != nil {
		t.Fatalf("applyEbookLocalCover: %v", err)
	}

	if cacher.calls != 1 || string(cacher.data) != "ebook-sidecar-cover" {
		t.Fatalf("cache call = calls %d data %q, want sidecar bytes", cacher.calls, string(cacher.data))
	}
	if updater.setCalls != 1 || updater.setPosterPath != "local/ebooks/content-1/poster/original.test-revision.webp" {
		t.Fatalf("poster update = calls %d path %q", updater.setCalls, updater.setPosterPath)
	}
}

func TestFindSidecarBookCoverIgnoresGenericNamesInMultiBookDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha.epub", "beta.epub"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("book"), 0o644); err != nil {
			t.Fatalf("write ebook %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("shared-cover"), 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.jpg"), []byte("beta-cover"), 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}

	cover, _, err := findSidecarBookCover(filepath.Join(dir, "alpha.epub"))
	if err != nil {
		t.Fatalf("findSidecarBookCover(alpha): %v", err)
	}
	if cover != nil {
		t.Fatalf("alpha cover = %q, want none (generic cover.jpg must not claim every book)", string(cover.Bytes))
	}

	cover, _, err = findSidecarBookCover(filepath.Join(dir, "beta.epub"))
	if err != nil {
		t.Fatalf("findSidecarBookCover(beta): %v", err)
	}
	if cover == nil || string(cover.Bytes) != "beta-cover" {
		t.Fatalf("beta cover = %v, want filename-matched beta.jpg", cover)
	}
}

func TestScanEbookPersistenceSQLWritesLibraryMembershipAndISBNOnly(t *testing.T) {
	ctx := context.Background()
	exec := &recordingEbookExecutor{}

	if err := insertEbookLibraryMembership(ctx, exec, "content-1", 44); err != nil {
		t.Fatalf("insertEbookLibraryMembership: %v", err)
	}
	if err := insertEbookISBNProviderID(ctx, exec, "content-1", "9780306406157"); err != nil {
		t.Fatalf("insertEbookISBNProviderID: %v", err)
	}
	if len(exec.queries) != 2 {
		t.Fatalf("queries = %d, want 2", len(exec.queries))
	}
	if !strings.Contains(exec.queries[0], "media_item_libraries") || exec.args[0][0] != "content-1" || exec.args[0][1] != 44 {
		t.Fatalf("library membership write = query %q args %+v", exec.queries[0], exec.args[0])
	}
	if !strings.Contains(exec.queries[1], "media_item_provider_ids") || !strings.Contains(exec.queries[1], "'isbn'") || !strings.Contains(exec.queries[1], "'ebook'") {
		t.Fatalf("ISBN provider write query = %q", exec.queries[1])
	}
	if strings.Contains(exec.queries[1], "asin") || exec.args[1][0] != "content-1" || exec.args[1][1] != "9780306406157" {
		t.Fatalf("ISBN provider write args/query = query %q args %+v", exec.queries[1], exec.args[1])
	}
}

func TestCollectEbookRootScansExcludesUnmountedRootFromReconciliation(t *testing.T) {
	healthy := t.TempDir()
	if err := os.WriteFile(filepath.Join(healthy, "book.epub"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}
	unmounted := filepath.Join(t.TempDir(), "gone")

	scans, err := collectEbookRootScans(context.Background(), 44, []string{unmounted, healthy})
	if err != nil {
		t.Fatalf("collectEbookRootScans: %v", err)
	}
	if len(scans) != 2 {
		t.Fatalf("scans = %d, want 2", len(scans))
	}
	if !scans[0].failed() || scans[0].rootErr == nil {
		t.Fatalf("unmounted root scan = %+v, want failed with rootErr", scans[0])
	}
	if scans[1].failed() || len(scans[1].files) != 1 {
		t.Fatalf("healthy root scan = %+v, want 1 file and no failure", scans[1])
	}

	reconcileRoots, sawFiles := splitEbookReconcileRoots(scans)
	if !sawFiles {
		t.Fatal("sawFiles = false, want true for the healthy root")
	}
	if len(reconcileRoots) != 1 || reconcileRoots[0] != healthy {
		t.Fatalf("reconcileRoots = %v, want only the healthy root: an unmounted root must never be reconciled (no mass-missing, no deletion)", reconcileRoots)
	}
}

func TestCollectEbookRootScansTreatsNonDirectoryRootAsFailed(t *testing.T) {
	dir := t.TempDir()
	fileRoot := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}

	scans, err := collectEbookRootScans(context.Background(), 44, []string{fileRoot})
	if err != nil {
		t.Fatalf("collectEbookRootScans: %v", err)
	}
	if len(scans) != 1 || !scans[0].failed() {
		t.Fatalf("scans = %+v, want one failed scan for a non-directory root", scans)
	}
	// The file itself is still indexed; only reconciliation is withheld.
	if len(scans[0].files) != 1 {
		t.Fatalf("files = %v, want the ebook file root indexed", scans[0].files)
	}
	if roots, _ := splitEbookReconcileRoots(scans); len(roots) != 0 {
		t.Fatalf("reconcileRoots = %v, want none", roots)
	}
}

func TestCollectEbookRootScansMidWalkSubtreeErrorExcludesRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based subtree failure cannot be simulated as root")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "readable.epub"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}
	locked := filepath.Join(root, "zz-locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(locked, "hidden.epub"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write hidden ebook: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	scans, err := collectEbookRootScans(context.Background(), 44, []string{root})
	if err != nil {
		t.Fatalf("collectEbookRootScans: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("scans = %d, want 1", len(scans))
	}
	if scans[0].walkFailures == 0 || !scans[0].failed() {
		t.Fatalf("scan = %+v, want walk failure recorded for unreadable subtree", scans[0])
	}
	if len(scans[0].files) != 1 {
		t.Fatalf("files = %v, want the readable ebook still indexed", scans[0].files)
	}
	// The unseen hidden.epub must not be marked missing: the whole root sits
	// out of reconciliation.
	if roots, _ := splitEbookReconcileRoots(scans); len(roots) != 0 {
		t.Fatalf("reconcileRoots = %v, want none after a mid-walk subtree error", roots)
	}
}

func TestCollectEbookRootScansFollowsSymlinkedRoot(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "book.epub"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}
	link := filepath.Join(t.TempDir(), "library")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	scans, err := collectEbookRootScans(context.Background(), 44, []string{link})
	if err != nil {
		t.Fatalf("collectEbookRootScans: %v", err)
	}
	if len(scans) != 1 || scans[0].failed() {
		t.Fatalf("scans = %+v, want one clean scan through the symlinked root", scans)
	}
	want := filepath.Join(link, "book.epub")
	if len(scans[0].files) != 1 || scans[0].files[0] != want {
		t.Fatalf("files = %v, want %q recorded under the logical (symlink) path", scans[0].files, want)
	}
	if roots, sawFiles := splitEbookReconcileRoots(scans); !sawFiles || len(roots) != 1 {
		t.Fatalf("reconcileRoots/sawFiles = %v/%v, want symlinked root reconcilable", roots, sawFiles)
	}
}

type fakeEbookCleanupGuard struct {
	allowance    bool
	consumeCalls int
	warnings     []string
	consumeErr   error
}

func (f *fakeEbookCleanupGuard) ConsumeEmptyCleanupAllowance(_ context.Context, _ int) (bool, error) {
	f.consumeCalls++
	return f.allowance, f.consumeErr
}

func (f *fakeEbookCleanupGuard) SetScanWarning(_ context.Context, _ int, code, _ string, _ time.Time) error {
	f.warnings = append(f.warnings, code)
	return nil
}

func TestEbookEmptyCleanupAllowedRequiresExplicitAllowance(t *testing.T) {
	guard := &fakeEbookCleanupGuard{allowance: false}

	allowed, err := ebookEmptyCleanupAllowed(context.Background(), guard, 44, true)
	if err != nil {
		t.Fatalf("ebookEmptyCleanupAllowed: %v", err)
	}
	if allowed {
		t.Fatal("allowed = true, want false: an empty root must not reconcile without confirmation")
	}
	if guard.consumeCalls != 1 {
		t.Fatalf("consumeCalls = %d, want 1", guard.consumeCalls)
	}
	if len(guard.warnings) != 1 || guard.warnings[0] != "empty_root" {
		t.Fatalf("warnings = %v, want one empty_root scan warning", guard.warnings)
	}
}

func TestEbookEmptyCleanupAllowedConsumesAllowanceOnce(t *testing.T) {
	guard := &fakeEbookCleanupGuard{allowance: true}

	allowed, err := ebookEmptyCleanupAllowed(context.Background(), guard, 44, true)
	if err != nil {
		t.Fatalf("ebookEmptyCleanupAllowed: %v", err)
	}
	if !allowed {
		t.Fatal("allowed = false, want true once the operator confirmed cleanup")
	}
	if len(guard.warnings) != 0 {
		t.Fatalf("warnings = %v, want none for a confirmed cleanup", guard.warnings)
	}
}

func TestEbookEmptyCleanupAllowedNeverConsumesForSubtreeScans(t *testing.T) {
	guard := &fakeEbookCleanupGuard{allowance: true}

	allowed, err := ebookEmptyCleanupAllowed(context.Background(), guard, 44, false)
	if err != nil {
		t.Fatalf("ebookEmptyCleanupAllowed: %v", err)
	}
	if allowed || guard.consumeCalls != 0 {
		t.Fatalf("allowed/consumeCalls = %v/%d, want subtree scans to skip cleanup without touching the folder allowance", allowed, guard.consumeCalls)
	}
}

func TestEbookEmptyCleanupAllowedNilGuardDeniesCleanup(t *testing.T) {
	allowed, err := ebookEmptyCleanupAllowed(context.Background(), nil, 44, true)
	if err != nil || allowed {
		t.Fatalf("allowed/err = %v/%v, want denial when no guard repository is wired", allowed, err)
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

func TestScanEbookFolderReportsProgress(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken.epub"), []byte("not a zip"), 0o644); err != nil {
		t.Fatalf("write ebook fixture: %v", err)
	}
	var updates []ProgressUpdate
	ctx := WithProgressReporter(context.Background(), func(update ProgressUpdate) {
		updates = append(updates, update)
	})

	err := (&Scanner{}).ScanEbookFolder(ctx, &models.MediaFolder{ID: 44, Paths: []string{root}})
	if err == nil {
		t.Fatal("ScanEbookFolder returned nil, want aggregate failure")
	}

	if len(updates) == 0 {
		t.Fatal("expected ebook scanner progress updates")
	}
	if updates[0].Phase != "ebook_scan" || updates[0].TotalFiles != 1 {
		t.Fatalf("first progress update = %+v, want ebook_scan discovery total", updates[0])
	}
	last := updates[len(updates)-1]
	if last.FilesProcessed != 1 || last.Errors != 1 {
		t.Fatalf("last progress update = %+v, want processed/error counts", last)
	}
}

func writeTestEPUB(t *testing.T, identifiers []string) string {
	return writeTestEPUBWithMeta(t, identifiers, "")
}

func writeTestEPUBWithMeta(t *testing.T, identifiers []string, extraMetaXML string) string {
	return writeTestEPUBWithDescriptionAndMeta(t, identifiers, "Back cover copy", extraMetaXML)
}

func writeTestEPUBWithDescription(t *testing.T, description string) string {
	return writeTestEPUBWithDescriptionAndMeta(t, []string{"ISBN: 978-0-306-40615-7"}, description, "")
}

func writeTestEPUBWithDescriptionAndMeta(t *testing.T, identifiers []string, description string, extraMetaXML string) string {
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
    <dc:description>`+description+`</dc:description>
    <meta name="calibre:series" content="Test Series"/>
    <meta name="calibre:series_index" content="2"/>
`+extraMetaXML+`
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

func writeTestEPUBWithOPFBytes(t *testing.T, opf []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "book.epub")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	zw := zip.NewWriter(file)
	add := func(name string, body []byte) {
		t.Helper()
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	add("META-INF/container.xml", []byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`))
	add("OPS/content.opf", opf)
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}
	return path
}

func writeTestEPUBWithCover(t *testing.T, coverHref string, coverType string, coverBytes []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "book.epub")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	zw := zip.NewWriter(file)
	add := func(name string, body []byte) {
		t.Helper()
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	add("META-INF/container.xml", []byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`))
	add("OPS/content.opf", []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
  <metadata>
    <dc:title>The Test Ebook</dc:title>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>
    <item id="cover-image" href="`+coverHref+`" media-type="`+coverType+`"/>
  </manifest>
</package>`))
	add("OPS/"+coverHref, coverBytes)
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}
	return path
}

func TestUpdateExistingEbookMediaItemPreservesCuratedMatchedItem(t *testing.T) {
	reader := &fakeFilesystemItemReader{items: []*models.MediaItem{{
		ContentID: "ebook-root-id",
		Type:      "ebook",
		Title:     "Curated Title",
		Year:      2020,
		Status:    "matched",
	}}}
	writer := &fakeFilesystemItemWriter{}

	curated, err := updateExistingEbookMediaItem(context.Background(), reader, writer, "ebook-root-id", &parsedEbook{
		Title: "Embedded File Title",
		Year:  2026,
	})
	if err != nil {
		t.Fatalf("updateExistingEbookMediaItem: %v", err)
	}
	if !curated {
		t.Fatal("curated = false, want true for a matched item")
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0: matched items must not be overwritten by file metadata", len(writer.upserts))
	}
}

func TestEbookItemHasCuratedMetadata(t *testing.T) {
	cases := []struct {
		item *models.MediaItem
		want bool
	}{
		{nil, false},
		{&models.MediaItem{Status: "matched"}, true},
		{&models.MediaItem{Status: " Matched "}, true},
		{&models.MediaItem{Status: "pending"}, false},
		{&models.MediaItem{Status: "unmatched"}, false},
		{&models.MediaItem{}, false},
	}
	for _, tc := range cases {
		if got := ebookItemHasCuratedMetadata(tc.item); got != tc.want {
			t.Errorf("ebookItemHasCuratedMetadata(%+v) = %v, want %v", tc.item, got, tc.want)
		}
	}
}

func TestParseEbookPDFReadsInfoDictionaryFromFileTail(t *testing.T) {
	// Non-linearized PDFs put the Info dictionary at the end of the file and
	// typically have no Info keys in the head at all; the parser must find the
	// trailing dictionary past the head scan window and use it to fill every
	// key the head did not produce.
	head := "%PDF-1.7\n" +
		"1 0 obj\n" +
		"<< /Author (Ada Writer)\n" +
		">>\nendobj\n"
	tail := "2 0 obj\n" +
		"<< /Title (Tail Info Title)\n" +
		"   /CreationDate (D:20240102030405Z)\n" +
		">>\nendobj\n" +
		"trailer\n<< /Info 2 0 R >>\n%%EOF"
	padding := strings.Repeat("x", maxPDFMetadataScanSize)

	path := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(path, []byte(head+padding+tail), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}
	if got.Title != "Tail Info Title" {
		t.Fatalf("Title = %q, want tail Info dictionary value", got.Title)
	}
	if strings.Join(got.Authors, ", ") != "Ada Writer" {
		t.Fatalf("Authors = %v, want head-only value preserved", got.Authors)
	}
	if got.Year != 2024 {
		t.Fatalf("Year = %d, want 2024 from tail CreationDate", got.Year)
	}
}

func TestParseEbookPDFHeadInfoWinsOverTailStreamGarbage(t *testing.T) {
	// Linearized PDFs carry the real Info dictionary in the head; a
	// "/Title"-shaped byte pattern inside a trailing compressed stream must
	// not replace it.
	head := "%PDF-1.7\n" +
		"1 0 obj\n" +
		"<< /Linearized 1 >>\nendobj\n" +
		"2 0 obj\n" +
		"<< /Title (Real Head Title)\n" +
		"   /Author (Ada Writer)\n" +
		">>\nendobj\n"
	tail := "3 0 obj\n" +
		"<< /Length 64 >>\nstream\n" +
		"\x01\x02/Title (\xfe\xba\xad binary stream garbage)\x03\x04\n" +
		"endstream\nendobj\n" +
		"trailer\n<< /Info 2 0 R >>\n%%EOF"
	padding := strings.Repeat("x", maxPDFMetadataScanSize)

	path := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(path, []byte(head+padding+tail), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	got, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}
	if got.Title != "Real Head Title" {
		t.Fatalf("Title = %q, want authoritative head value over tail stream garbage", got.Title)
	}
}

func TestFindPDFInfoValueRequiresProperKeyDelimiter(t *testing.T) {
	// "/TitleSort" must not satisfy a "/Title" lookup, and the real key later
	// in the window must still be found.
	data := []byte(`<< /TitleSort (Adventures of, The) /Title (The Adventures) >>`)
	value, ok := findPDFInfoValue(data, "Title")
	if !ok || value != "The Adventures" {
		t.Fatalf("findPDFInfoValue = %q, %v; want the properly delimited /Title", value, ok)
	}
}

func TestFindPDFInfoValueRetriesPastUnparseableOccurrences(t *testing.T) {
	// A key match inside binary stream data whose "value" is not a PDF string
	// must not end the search; the real key/value pair later in the window
	// still wins.
	data := []byte("stream\x00/Title \x07\x08garbage\x00endstream\n" +
		"<< /Title (Recovered Title) >>")
	value, ok := findPDFInfoValue(data, "Title")
	if !ok || value != "Recovered Title" {
		t.Fatalf("findPDFInfoValue = %q, %v; want recovery past unparseable match", value, ok)
	}

	if _, ok := findPDFInfoValue([]byte("/Titled (Nope)"), "Title"); ok {
		t.Fatal("findPDFInfoValue matched a longer key sharing the prefix")
	}
}

func TestParseEbookFB2RejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.fb2")
	if err := os.WriteFile(path, make([]byte, maxEPUBMetadataEntrySize+1), 0o644); err != nil {
		t.Fatalf("write fb2: %v", err)
	}

	_, err := parseEbookFile(path)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("parseEbookFile error = %v, want size-cap rejection", err)
	}
}

// buildTestMOBI assembles a minimal but valid MOBI/AZW container: a PDB header,
// a single record-info entry pointing at record 0, and record 0 holding a
// PalmDOC header, a MOBI header, an EXTH block, and the full-title string.
func buildTestMOBI(t *testing.T) string {
	t.Helper()

	exthRec := func(typ uint32, val string) []byte {
		b := make([]byte, 8+len(val))
		binary.BigEndian.PutUint32(b[0:4], typ)
		binary.BigEndian.PutUint32(b[4:8], uint32(8+len(val)))
		copy(b[8:], val)
		return b
	}
	var recs []byte
	recs = append(recs, exthRec(100, "A H Lee")...)          // author
	recs = append(recs, exthRec(503, "The Sea: A Novel")...) // updated title
	recs = append(recs, exthRec(104, "9780306406157")...)    // ISBN
	exth := make([]byte, 12)
	copy(exth[0:4], "EXTH")
	binary.BigEndian.PutUint32(exth[4:8], uint32(12+len(recs)))
	binary.BigEndian.PutUint32(exth[8:12], 3)
	exth = append(exth, recs...)

	const mobiHeaderLen = 232
	mobi := make([]byte, mobiHeaderLen)
	copy(mobi[0:4], "MOBI")
	binary.BigEndian.PutUint32(mobi[4:8], mobiHeaderLen)
	binary.BigEndian.PutUint32(mobi[12:16], 65001) // text encoding: UTF-8

	fullName := []byte("The Sea")
	nameOff := 16 + mobiHeaderLen + len(exth) // relative to record 0 start
	binary.BigEndian.PutUint32(mobi[0x44:0x48], uint32(nameOff))
	binary.BigEndian.PutUint32(mobi[0x48:0x4C], uint32(len(fullName)))

	var rec0 []byte
	rec0 = append(rec0, make([]byte, 16)...) // PalmDOC header (zeroed)
	rec0 = append(rec0, mobi...)
	rec0 = append(rec0, exth...)
	rec0 = append(rec0, fullName...)

	const rec0Off = 86 // 78-byte PDB header + one 8-byte record-info entry
	pdb := make([]byte, rec0Off)
	copy(pdb[0:32], "The Sea") // PDB database name (last-resort title)
	copy(pdb[60:64], "BOOK")
	copy(pdb[64:68], "MOBI")
	binary.BigEndian.PutUint16(pdb[76:78], 1)       // record count
	binary.BigEndian.PutUint32(pdb[78:82], rec0Off) // record 0 offset

	path := filepath.Join(t.TempDir(), "book.mobi")
	if err := os.WriteFile(path, append(pdb, rec0...), 0o644); err != nil {
		t.Fatalf("write test mobi: %v", err)
	}
	return path
}

func TestParseEbookMOBIEXTH(t *testing.T) {
	got, err := parseEbookFile(buildTestMOBI(t))
	if err != nil {
		t.Fatalf("parseEbookFile error = %v", err)
	}
	if got.Format != "mobi" {
		t.Fatalf("Format = %q, want mobi", got.Format)
	}
	if got.Title != "The Sea: A Novel" {
		t.Fatalf("Title = %q, want EXTH updated title", got.Title)
	}
	if len(got.Authors) != 1 || got.Authors[0] != "A H Lee" {
		t.Fatalf("Authors = %v, want [A H Lee]", got.Authors)
	}
	if got.ISBN != "9780306406157" {
		t.Fatalf("ISBN = %q, want 9780306406157", got.ISBN)
	}
}

func TestEbookAuthorFromPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "corroborated grandparent and filename suffix",
			path: "/books/Books_English/Lisa Jewell/The House We Grew Up In (135563)/The House We Grew Up In - Lisa Jewell.azw3",
			want: "Lisa Jewell",
		},
		{
			name: "case and spacing differences still match",
			path: "/books/Books_English/A. F. Carter/All of Us (57890)/All of Us - a. f.  carter.pdf",
			want: "A. F. Carter",
		},
		{
			name: "no dash in filename",
			path: "/books/Books_German/Schweizer Familie 11.04.2019.pdf",
			want: "",
		},
		{
			name: "suffix does not match grandparent dir",
			path: "/books/Books_English/Stephen King/Salem's Lot (8507)/Salem's Lot - Some Other Name.pdf",
			want: "",
		},
		{
			name: "empty suffix",
			path: "/books/X/Author/Title/Title - .pdf",
			want: "",
		},
		{
			name: "inverted series folder rejected (grandparent is title)",
			path: "/books/Books_Dutch/De legenden van de Alfen/Heinz, Markus (2118)/Heinz, Markus - De legenden van de Alfen.epub",
			want: "",
		},
		{
			name: "comma surname form accepted",
			path: "/books/Books_Dutch/Mersbergen, Jan van/De laatste ontsnapping (8702)/De laatste ontsnapping - Mersbergen, Jan van.epub",
			want: "Mersbergen, Jan van",
		},
		{
			name: "particle in name accepted",
			path: "/books/Books_English/Dean R. Koontz/Midnight (7408)/Midnight - Dean R. Koontz.epub",
			want: "Dean R. Koontz",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ebookAuthorFromPath(tc.path); got != tc.want {
				t.Fatalf("ebookAuthorFromPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
