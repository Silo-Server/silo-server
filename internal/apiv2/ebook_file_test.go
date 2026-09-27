package apiv2

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/ebookconvert"
	"github.com/Silo-Server/silo-server/internal/models"
)

type ebookFileResolver struct{ file *models.MediaFile }

func (f ebookFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	return f.file, nil
}

type ebookFileAccess struct {
	denied bool
	filter catalogpkg.AccessFilter
}

func (a *ebookFileAccess) EnsureAccessible(_ context.Context, _ string, filter catalogpkg.AccessFilter) error {
	a.filter = filter
	if a.denied {
		return catalogpkg.ErrItemNotFound
	}
	return nil
}

type ebookFileConverter struct {
	calls  int
	result string
	err    error
}

func (c *ebookFileConverter) GetOrConvert(context.Context, string, ebookconvert.SourceKey) (string, error) {
	c.calls++
	return c.result, c.err
}
func (c *ebookFileConverter) Lookup(ebookconvert.SourceKey) (string, error, bool) {
	return "", nil, false
}

func TestEbookFileRawTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Synthetic book.epub")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	access := &ebookFileAccess{}
	service := handlers.NewEbookReaderHandler(&handlers.MediaFileAuthorizer{FileResolver: ebookFileResolver{file: &models.MediaFile{ID: 42, ContentID: "book", BaseType: "ebook", Container: "epub", FilePath: path}}, ItemAccess: access})
	deps := pilotDeps(nil, nil)
	deps.EbookFiles = service
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	url := Prefix + "/ebooks/book/files/42/read"
	rec := do(t, h, "GET", url, "", with(viewer, "Range", "bytes=2-4"))
	if rec.Code != 206 || rec.Body.String() != "234" || rec.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Fatalf("range: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/epub+zip" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline;") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(rec.Header())
	}
	if access.filter.UserID != 1 || access.filter.ProfileID != "p-owner" {
		t.Fatalf("scope: %+v", access.filter)
	}
	rec = do(t, h, "HEAD", url, "", viewer)
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "10" {
		t.Fatalf("head: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	modified := rec.Header().Get("Last-Modified")
	rec = do(t, h, "GET", url, "", with(viewer, "If-Modified-Since", modified))
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("conditional: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "GET", url, "", with(viewer, "Range", "bytes=99-"))
	if rec.Code != 416 {
		t.Fatalf("unsatisfiable: %d", rec.Code)
	}
	for _, target := range []string{Prefix + "/ebooks/other/files/42/read", Prefix + "/ebooks/book/files/0/read"} {
		rec = do(t, h, "GET", target, "", viewer)
		if rec.Code != 404 && rec.Code != 422 {
			t.Fatalf("bad file: %d %s", rec.Code, rec.Body.String())
		}
	}
	access.denied = true
	rec = do(t, h, "GET", url, "", viewer)
	if rec.Code != 404 {
		t.Fatalf("access: %d %s", rec.Code, rec.Body.String())
	}
	for _, headers := range []map[string]string{nil, bearer(memberToken), with(bearer(memberToken), "X-Profile-Id", "p-other")} {
		rec = do(t, h, "GET", url, "", headers)
		if rec.Code == 200 || rec.Code == 206 {
			t.Fatal("authorization allowed raw file")
		}
	}
}

func TestEbookFileHeadDoesNotConvertAndGetFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.mobi")
	if err := os.WriteFile(path, []byte("raw-kindle"), 0600); err != nil {
		t.Fatal(err)
	}
	converter := &ebookFileConverter{err: errors.New("synthetic unsupported book")}
	service := handlers.NewEbookReaderHandler(&handlers.MediaFileAuthorizer{FileResolver: ebookFileResolver{file: &models.MediaFile{ID: 42, ContentID: "book", BaseType: "ebook", Container: "mobi", FilePath: path}}, ItemAccess: &ebookFileAccess{}})
	service.Conversion = &handlers.EbookConversion{Converter: converter, Enabled: func(context.Context) bool { return true }}
	deps := pilotDeps(nil, nil)
	deps.EbookFiles = service
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	url := Prefix + "/ebooks/book/files/42/read"
	rec := do(t, h, http.MethodHead, url, "", viewer)
	if rec.Code != 200 || converter.calls != 0 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "" || rec.Header().Get("Content-Type") != "application/epub+zip" {
		t.Fatalf("uncached HEAD: %d %v calls=%d", rec.Code, rec.Header(), converter.calls)
	}
	rec = do(t, h, http.MethodGet, url, "", viewer)
	if rec.Code != 200 || rec.Body.String() != "raw-kindle" || converter.calls != 1 || rec.Header().Get(handlers.ConversionHeader) != "failed" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("fallback: %d %v %s calls=%d", rec.Code, rec.Header(), rec.Body.String(), converter.calls)
	}
}
