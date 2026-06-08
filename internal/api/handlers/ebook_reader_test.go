package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func newEbookReaderAuthRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{
		UserID:    1,
		Role:      "user",
		TokenType: auth.TokenTypeAccess,
	})
	return req.WithContext(ctx)
}

func withEbookReaderRouteParams(req *http.Request, contentID, fileID string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("content_id", contentID)
	routeCtx.URLParams.Add("file_id", fileID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func TestEbookReaderServesEbookInlineWithRangeSupport(t *testing.T) {
	filePath := writePlaybackTestMediaFile(t, "book.epub")
	if err := os.WriteFile(filePath, []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("write ebook: %v", err)
	}

	handler := NewEbookReaderHandler(&MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{
			file: &models.MediaFile{
				ID:        42,
				ContentID: "ebook-1",
				FilePath:  filePath,
				Container: "epub",
				BaseType:  "ebook",
			},
		},
		ItemAccess: stubItemAccessChecker{},
	})

	req := newEbookReaderAuthRequest(http.MethodGet, "/ebooks/ebook-1/files/42/read")
	req.Header.Set("Range", "bytes=2-5")
	req = withEbookReaderRouteParams(req, "ebook-1", "42")

	rr := httptest.NewRecorder()
	handler.HandleReadFile(rr, req)

	if rr.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/epub+zip" {
		t.Fatalf("content-type = %q", got)
	}
	if got := rr.Header().Get("Content-Disposition"); got != `inline; filename="book.epub"` {
		t.Fatalf("content-disposition = %q", got)
	}
	if rr.Body.String() != "2345" {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestEbookReaderRejectsNonEbookFile(t *testing.T) {
	handler := NewEbookReaderHandler(&MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{
			file: &models.MediaFile{
				ID:        42,
				ContentID: "movie-1",
				FilePath:  "/tmp/movie.mkv",
				Container: "mkv",
				BaseType:  "movie",
			},
		},
		ItemAccess: stubItemAccessChecker{},
	})

	req := newEbookReaderAuthRequest(http.MethodGet, "/ebooks/movie-1/files/42/read")
	req = withEbookReaderRouteParams(req, "movie-1", "42")

	rr := httptest.NewRecorder()
	handler.HandleReadFile(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestEbookReaderRecognizesReadestFormats(t *testing.T) {
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
		"book.txt":     "text/plain; charset=utf-8",
		"book.md":      "text/markdown; charset=utf-8",
		"book.unknown": "application/octet-stream",
	}

	for name, wantMime := range cases {
		t.Run(name, func(t *testing.T) {
			file := &models.MediaFile{FilePath: "/library/" + name, Container: name}
			if name == "book.unknown" {
				if isEbookFile(file) {
					t.Fatal("unknown extension should not be treated as an ebook reader format")
				}
				return
			}
			file.BaseType = "ebook"
			if !isEbookFile(file) {
				t.Fatal("expected ebook reader format")
			}
			if got := ebookMimeType(file.FilePath, file.Container); got != wantMime {
				t.Fatalf("ebookMimeType() = %q, want %q", got, wantMime)
			}
		})
	}
}

func TestEbookReaderMapsAccessFailureToNotFound(t *testing.T) {
	handler := NewEbookReaderHandler(&MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{
			file: &models.MediaFile{
				ID:        42,
				ContentID: "ebook-1",
				FilePath:  "/tmp/book.epub",
				Container: "epub",
				BaseType:  "ebook",
			},
		},
		ItemAccess: stubItemAccessChecker{err: catalog.ErrItemNotFound},
	})

	req := newEbookReaderAuthRequest(http.MethodGet, "/ebooks/ebook-1/files/42/read")
	req = withEbookReaderRouteParams(req, "ebook-1", "42")

	rr := httptest.NewRecorder()
	handler.HandleReadFile(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestEbookReaderReturnsInternalErrorForUnexpectedAuthorizeFailure(t *testing.T) {
	handler := NewEbookReaderHandler(&MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{err: errors.New("db down")},
		ItemAccess:   stubItemAccessChecker{},
	})

	req := newEbookReaderAuthRequest(http.MethodGet, "/ebooks/ebook-1/files/42/read")
	req = withEbookReaderRouteParams(req, "ebook-1", "42")

	rr := httptest.NewRecorder()
	handler.HandleReadFile(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}
