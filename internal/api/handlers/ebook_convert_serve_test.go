package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ebookconvert"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeConverter struct {
	epubPath string
	err      error
	calls    int
}

func (f *fakeConverter) GetOrConvert(_ context.Context, _ string, _ ebookconvert.SourceKey) (string, error) {
	f.calls++
	return f.epubPath, f.err
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

func TestServeEbook_DisabledServesRaw(t *testing.T) {
	raw := writeTemp(t, "book.mobi", "raw-mobi-bytes")
	conv := &fakeConverter{epubPath: "should-not-be-used"}
	h := &EbookReaderHandler{Conversion: enabledConversion(conv, false)}

	file := &models.MediaFile{ID: 3, FilePath: raw, Container: "mobi"}
	rec := serveAndRecord(t, h, file)

	if got := rec.Header().Get(ConversionHeader); got != "" {
		t.Fatalf("%s = %q, want empty (no conversion attempted)", ConversionHeader, got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-mobipocket-ebook" {
		t.Fatalf("Content-Type = %q, want raw mobi mime", got)
	}
	if conv.calls != 0 {
		t.Fatalf("converter called %d times while disabled, want 0", conv.calls)
	}
}

func TestServeEbook_NonKindleUntouched(t *testing.T) {
	raw := writeTemp(t, "book.epub", "epub-bytes")
	conv := &fakeConverter{epubPath: "should-not-be-used"}
	h := &EbookReaderHandler{Conversion: enabledConversion(conv, true)}

	file := &models.MediaFile{ID: 4, FilePath: raw, Container: "epub"}
	rec := serveAndRecord(t, h, file)

	if rec.Header().Get(ConversionHeader) != "" {
		t.Fatal("non-kindle file must not trigger conversion")
	}
	if conv.calls != 0 {
		t.Fatalf("converter called %d times for epub, want 0", conv.calls)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/epub+zip" {
		t.Fatalf("Content-Type = %q, want epub", got)
	}
}

func TestServeEbook_NilConversionServesRaw(t *testing.T) {
	raw := writeTemp(t, "book.mobi", "raw")
	h := &EbookReaderHandler{} // Conversion nil
	file := &models.MediaFile{ID: 5, FilePath: raw, Container: "mobi"}
	rec := serveAndRecord(t, h, file)
	if rec.Header().Get(ConversionHeader) != "" {
		t.Fatal("nil Conversion must not set the conversion header")
	}
}

func TestHandleConversionCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		conv *EbookConversion
		want bool
	}{
		{"enabled", enabledConversion(&fakeConverter{}, true), true},
		{"flag-off", enabledConversion(&fakeConverter{}, false), false},
		{"not-wired", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &EbookReaderHandler{Conversion: tc.conv}
			rec := httptest.NewRecorder()
			h.HandleConversionCapability(rec, httptest.NewRequest(http.MethodGet, "/ebooks/capability", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			body := rec.Body.String()
			wantField := `"enabled":false`
			if tc.want {
				wantField = `"enabled":true`
			}
			if !strings.Contains(body, wantField) {
				t.Fatalf("body %q missing %s", body, wantField)
			}
			if !strings.Contains(body, `"served_format":"epub"`) || !strings.Contains(body, "mobi") {
				t.Fatalf("capability body incomplete: %s", body)
			}
		})
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
