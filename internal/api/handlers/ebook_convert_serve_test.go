package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ebookconvert"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeConverter struct {
	epubPath string
	err      error
	calls    int

	lookupPath string
	lookupErr  error
	lookupOK   bool
}

func (f *fakeConverter) GetOrConvert(_ context.Context, _ string, _ ebookconvert.SourceKey) (string, error) {
	f.calls++
	return f.epubPath, f.err
}

func (f *fakeConverter) Lookup(_ ebookconvert.SourceKey) (string, error, bool) {
	return f.lookupPath, f.lookupErr, f.lookupOK
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func serveAndRecord(t *testing.T, h *EbookReaderHandler, file *models.MediaFile) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/read", nil)
	if err := h.serveEbook(rec, req, file); err != nil {
		t.Fatalf("serveEbook: %v", err)
	}
	return rec
}

func enabledConversion(conv EbookConverter, on bool) *EbookConversion {
	return &EbookConversion{Converter: conv, Enabled: func(context.Context) bool { return on }}
}

func TestServeEbook_ConvertsKindleWhenEnabled(t *testing.T) {
	epub := writeTemp(t, "out.epub", "PK\x03\x04 fake-epub-bytes")
	src := writeTemp(t, "book.azw3", "raw-azw3-source")
	conv := &fakeConverter{epubPath: epub}
	h := &EbookReaderHandler{Conversion: enabledConversion(conv, true)}

	file := &models.MediaFile{ID: 1, FilePath: src, Container: "azw3", FileHash: "abc123"}
	rec := serveAndRecord(t, h, file)

	if got := rec.Header().Get("Content-Type"); got != "application/epub+zip" {
		t.Fatalf("Content-Type = %q, want application/epub+zip", got)
	}
	if got := rec.Header().Get(ConversionHeader); got != "converted" {
		t.Fatalf("%s = %q, want converted", ConversionHeader, got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("expected an ETag on converted response")
	}
	if rec.Body.String() != "PK\x03\x04 fake-epub-bytes" {
		t.Fatalf("body = %q, want the epub bytes", rec.Body.String())
	}
	if conv.calls != 1 {
		t.Fatalf("converter called %d times, want 1", conv.calls)
	}
}

func TestServeEbook_ConversionFailureFallsBackToRawWithHeader(t *testing.T) {
	raw := writeTemp(t, "book.mobi", "raw-mobi-bytes")
	conv := &fakeConverter{err: ebookconvert.ErrDRMProtected}
	h := &EbookReaderHandler{Conversion: enabledConversion(conv, true)}

	file := &models.MediaFile{ID: 2, FilePath: raw, Container: "mobi"}
	rec := serveAndRecord(t, h, file)

	if got := rec.Header().Get(ConversionHeader); got != "failed" {
		t.Fatalf("%s = %q, want failed", ConversionHeader, got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-mobipocket-ebook" {
		t.Fatalf("Content-Type = %q, want raw mobi mime", got)
	}
	if rec.Body.String() != "raw-mobi-bytes" {
		t.Fatalf("body = %q, want raw mobi bytes", rec.Body.String())
	}
}

// A HEAD on a cache miss must NOT trigger a (possibly minute-long) conversion;
// it advertises the converted representation cheaply and lets the GET do the work.
func TestServeEbook_HeadDoesNotConvertOnMiss(t *testing.T) {
	src := writeTemp(t, "book.azw3", "raw-azw3-source")
	conv := &fakeConverter{lookupOK: false} // cache miss
	h := &EbookReaderHandler{Conversion: enabledConversion(conv, true)}
	file := &models.MediaFile{ID: 1, FilePath: src, Container: "azw3"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/read", nil)
	if err := h.serveEbook(rec, req, file); err != nil {
		t.Fatalf("serveEbook HEAD: %v", err)
	}
	if conv.calls != 0 {
		t.Fatalf("HEAD on a cache miss triggered %d conversions, want 0", conv.calls)
	}
	if got := rec.Header().Get(ConversionHeader); got != "converted" {
		t.Fatalf("%s = %q, want converted (advertised on HEAD miss)", ConversionHeader, got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/epub+zip" {
		t.Fatalf("Content-Type = %q, want application/epub+zip", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("HEAD miss should still carry the conversion ETag")
	}
}

// A HEAD that hits the cache serves the converted headers from the cached file,
// without converting.
func TestServeEbook_HeadServesCachedConverted(t *testing.T) {
	epub := writeTemp(t, "out.epub", "PK\x03\x04 cached-epub")
	src := writeTemp(t, "book.mobi", "raw-mobi-source")
	conv := &fakeConverter{lookupPath: epub, lookupOK: true}
	h := &EbookReaderHandler{Conversion: enabledConversion(conv, true)}
	file := &models.MediaFile{ID: 2, FilePath: src, Container: "mobi"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/read", nil)
	if err := h.serveEbook(rec, req, file); err != nil {
		t.Fatalf("serveEbook HEAD: %v", err)
	}
	if conv.calls != 0 {
		t.Fatalf("HEAD on a cache hit triggered %d conversions, want 0", conv.calls)
	}
	if got := rec.Header().Get(ConversionHeader); got != "converted" {
		t.Fatalf("%s = %q, want converted", ConversionHeader, got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/epub+zip" {
		t.Fatalf("Content-Type = %q, want application/epub+zip", got)
	}
}

// A HEAD on a negatively-cached (DRM/failed) source reports the failed contract
// without converting.
func TestServeEbook_HeadDRMReturnsFailed(t *testing.T) {
	src := writeTemp(t, "book.mobi", "raw-mobi-source")
	conv := &fakeConverter{lookupErr: ebookconvert.ErrDRMProtected}
	h := &EbookReaderHandler{Conversion: enabledConversion(conv, true)}
	file := &models.MediaFile{ID: 3, FilePath: src, Container: "mobi"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/read", nil)
	if err := h.serveEbook(rec, req, file); err != nil {
		t.Fatalf("serveEbook HEAD: %v", err)
	}
	if conv.calls != 0 {
		t.Fatalf("HEAD on a negatively-cached source triggered %d conversions, want 0", conv.calls)
	}
	if got := rec.Header().Get(ConversionHeader); got != "failed" {
		t.Fatalf("%s = %q, want failed", ConversionHeader, got)
	}
}

func TestServeEbook_ContextCancelPropagates(t *testing.T) {
	src := writeTemp(t, "book.mobi", "raw-mobi-source")
	conv := &fakeConverter{err: context.Canceled}
	h := &EbookReaderHandler{Conversion: enabledConversion(conv, true)}
	file := &models.MediaFile{ID: 6, FilePath: src, Container: "mobi"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/read", nil)
	err := h.serveEbook(rec, req, file)
	if err == nil {
		t.Fatal("expected context cancellation to propagate, not fall back")
	}
	if rec.Header().Get(ConversionHeader) == "failed" {
		t.Fatal("cancellation must not be reported as a conversion failure")
	}
}
