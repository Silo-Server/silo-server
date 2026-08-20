package ebookformat

import (
	"mime"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestMimeTypeNeverReturnsOctetStreamForAdmittedFiles pins the invariant the
// reader depends on: a file admitted by IsEbookFile always has a concrete
// ebook MIME type, including when it was admitted through its container rather
// than its extension.
func TestMimeTypeNeverReturnsOctetStreamForAdmittedFiles(t *testing.T) {
	cases := []struct {
		path      string
		container string
		wantMime  string
	}{
		{"/library/book.txt", "epub", "application/epub+zip"},
		{"/library/book", "pdf", "application/pdf"},
		{"/library/book.epub", "pdf", "application/epub+zip"}, // known extension wins
	}
	for _, tc := range cases {
		file := &models.MediaFile{FilePath: tc.path, Container: tc.container, BaseType: "ebook"}
		if !IsEbookFile(file) {
			t.Fatalf("IsEbookFile(%q, %q) = false, want admitted", tc.path, tc.container)
		}
		if got := MimeTypeForFile(file); got != tc.wantMime {
			t.Fatalf("MimeTypeForFile(%q, %q) = %q, want %q", tc.path, tc.container, got, tc.wantMime)
		}
	}
}

func TestRecognizesReadestFormats(t *testing.T) {
	cases := map[string]string{
		"book.epub":    "application/epub+zip",
		"book.pdf":     "application/pdf",
		"book.mobi":    "application/x-mobipocket-ebook",
		"book.azw":     "application/vnd.amazon.ebook",
		"book.azw3":    "application/vnd.amazon.mobi8-ebook",
		"book.cbz":     "application/vnd.comicbook+zip",
		"book.cbr":     "application/vnd.comicbook-rar",
		"book.fb2":     "application/x-fictionbook+xml",
		"book.fbz":     "application/x-zip-compressed-fb2",
		"book.fb2.zip": "application/x-zip-compressed-fb2",
		"book.md":      "application/octet-stream",
		"book.unknown": "application/octet-stream",
	}
	rejected := map[string]bool{"book.md": true, "book.unknown": true}

	for name, wantMime := range cases {
		t.Run(name, func(t *testing.T) {
			container := name
			if name == "book.fb2.zip" {
				container = "fbz"
			}
			file := &models.MediaFile{FilePath: "/library/" + name, Container: container, BaseType: "ebook"}
			if rejected[name] && IsEbookFile(file) {
				t.Fatalf("%s should not be treated as an ebook reader format", name)
			}
			if !rejected[name] && !IsEbookFile(file) {
				t.Fatal("expected ebook reader format")
			}
			if got := MimeTypeForFile(file); got != wantMime {
				t.Fatalf("MimeTypeForFile() = %q, want %q", got, wantMime)
			}
		})
	}
}

func TestRejectsPlainText(t *testing.T) {
	file := &models.MediaFile{FilePath: "/library/book.txt", BaseType: "ebook"}

	if IsEbookFile(file) {
		t.Fatal("plain text should not be treated as an ebook reader format")
	}
	if got := MimeTypeForFile(file); got == "text/plain; charset=utf-8" {
		t.Fatal("plain text should not have an ebook reader MIME type")
	}
}

func TestRecognizesFBZFromCompoundFilenameWithoutContainer(t *testing.T) {
	file := &models.MediaFile{FilePath: "/library/book.fb2.zip", BaseType: "ebook"}

	if !IsEbookFile(file) {
		t.Fatal("expected .fb2.zip path to be treated as an ebook reader format")
	}
}

// TestIsEbookFileRequiresEbookBaseType separates the two admission rules: the
// reader only opens files belonging to an ebook item, while the format-only
// check (used by the Audiobookshelf surface, where a supplementary ebook
// carries its audiobook item's base type) does not care.
func TestIsEbookFileRequiresEbookBaseType(t *testing.T) {
	file := &models.MediaFile{FilePath: "/library/extra.epub", BaseType: "audiobook"}

	if IsEbookFile(file) {
		t.Fatal("a non-ebook item's file must not be admitted for reading")
	}
	if got := FormatForFile(file); got != "epub" {
		t.Fatalf("FormatForFile() = %q, want epub", got)
	}
}

func TestMimeTypeRejectsUnknownFormat(t *testing.T) {
	if got := MimeType("m4b"); got != "" {
		t.Fatalf("MimeType(m4b) = %q, want empty", got)
	}
	if got := MimeType(Normalize(".PDF")); got != "application/pdf" {
		t.Fatalf("MimeType(Normalize(.PDF)) = %q, want application/pdf", got)
	}
	if got := MimeType(Normalize(".m4b")); got != "" {
		t.Fatalf("MimeType(Normalize(.m4b)) = %q, want empty", got)
	}
}

func TestPreferredReadFile(t *testing.T) {
	video := &models.MediaFile{ID: 9, FilePath: "/library/movie.mkv", BaseType: "movie"}
	pdf := &models.MediaFile{ID: 1, FilePath: "/library/book.pdf", BaseType: "ebook"}
	epub := &models.MediaFile{ID: 2, FilePath: "/library/book.epub", BaseType: "ebook"}

	if got := PreferredReadFile([]*models.MediaFile{video, pdf, epub}); got == nil || got.ID != 2 {
		t.Fatalf("PreferredReadFile = %+v, want epub file 2", got)
	}
	if got := PreferredReadFile([]*models.MediaFile{video, pdf}); got == nil || got.ID != 1 {
		t.Fatalf("PreferredReadFile = %+v, want first reader-supported file 1", got)
	}
	if got := PreferredReadFile([]*models.MediaFile{video}); got != nil {
		t.Fatalf("PreferredReadFile = %+v, want nil without ebook files", got)
	}
}

// TestPreferredFileIgnoresBaseType covers the Audiobookshelf path: an ebook
// shipped alongside audio in an audiobook item is still that item's ebook.
func TestPreferredFileIgnoresBaseType(t *testing.T) {
	audio := &models.MediaFile{ID: 3, FilePath: "/books/audio.m4b", BaseType: "audiobook"}
	pdf := &models.MediaFile{ID: 1, FilePath: "/books/supplement.pdf", BaseType: "audiobook"}
	epub := &models.MediaFile{ID: 2, FilePath: "/books/primary.epub", BaseType: "audiobook"}

	if got := PreferredFile([]*models.MediaFile{pdf, epub, audio}); got == nil || got.ID != 2 {
		t.Fatalf("PreferredFile = %+v, want EPUB id 2", got)
	}
	if got := PreferredFile([]*models.MediaFile{audio}); got != nil {
		t.Fatalf("PreferredFile = %+v, want nil for audio-only files", got)
	}
	if got := PreferredReadFile([]*models.MediaFile{pdf, epub}); got != nil {
		t.Fatalf("PreferredReadFile = %+v, want nil for non-ebook items", got)
	}
}

func TestContentDispositionProducesParseableHeaders(t *testing.T) {
	cases := map[string]string{
		"book.epub":            "book.epub",
		`bo"ok\.epub`:          `bo"ok\.epub`,
		`trailing-slash\`:      `trailing-slash\`,
		"börk \U0001F4DA.epub": "börk \U0001F4DA.epub",
		"":                     "ebook",
		".":                    "ebook",
	}
	for _, kind := range []string{"inline", "attachment"} {
		for name, want := range cases {
			t.Run(kind+"/"+name, func(t *testing.T) {
				got := ContentDisposition(kind, name)
				mediaType, params, err := mime.ParseMediaType(got)
				if err != nil {
					t.Fatalf("header %q is malformed: %v", got, err)
				}
				if mediaType != kind {
					t.Fatalf("media type = %q, want %q", mediaType, kind)
				}
				if params["filename"] != want {
					t.Fatalf("filename = %q, want %q", params["filename"], want)
				}
			})
		}
	}
}
