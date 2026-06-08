package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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
	ctx = apimw.SetProfileID(ctx, "profile-1")
	return req.WithContext(ctx)
}

func withEbookReaderRouteParams(req *http.Request, contentID, fileID string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("content_id", contentID)
	routeCtx.URLParams.Add("file_id", fileID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func withEbookReaderContentRouteParam(req *http.Request, contentID string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("content_id", contentID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

type fakeEbookProgressStore struct {
	progress *EbookReaderProgress
	upserted *EbookReaderProgress
	err      error
}

func (s *fakeEbookProgressStore) Get(
	context.Context,
	int,
	string,
	string,
) (*EbookReaderProgress, error) {
	return s.progress, s.err
}

func (s *fakeEbookProgressStore) Upsert(_ context.Context, progress EbookReaderProgress) error {
	if s.err != nil {
		return s.err
	}
	s.upserted = &progress
	return nil
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
		"book.fb2.zip": "application/x-zip-compressed-fb2",
		"book.txt":     "text/plain; charset=utf-8",
		"book.md":      "text/markdown; charset=utf-8",
		"book.unknown": "application/octet-stream",
	}

	for name, wantMime := range cases {
		t.Run(name, func(t *testing.T) {
			container := name
			if name == "book.fb2.zip" {
				container = "fbz"
			}
			file := &models.MediaFile{FilePath: "/library/" + name, Container: container}
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

func TestEbookReaderSavesProgressForAuthorizedEbookFile(t *testing.T) {
	store := &fakeEbookProgressStore{}
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
		ItemAccess: stubItemAccessChecker{},
	})
	handler.ProgressStore = store

	req := httptest.NewRequest(
		http.MethodPut,
		"/ebooks/ebook-1/progress",
		strings.NewReader(`{"file_id":42,"location":"epubcfi(/6/4)","progress":0.42}`),
	)
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{
		UserID:    1,
		Role:      "user",
		TokenType: auth.TokenTypeAccess,
	})
	req = req.WithContext(apimw.SetProfileID(ctx, "profile-9"))
	req = withEbookReaderContentRouteParam(req, "ebook-1")

	rr := httptest.NewRecorder()
	handler.HandleSaveProgress(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if store.upserted == nil {
		t.Fatal("expected progress to be saved")
	}
	if store.upserted.UserID != 1 || store.upserted.ProfileID != "profile-9" {
		t.Fatalf("scope = user %d profile %q", store.upserted.UserID, store.upserted.ProfileID)
	}
	if store.upserted.ContentID != "ebook-1" || store.upserted.FileID != 42 {
		t.Fatalf("target = content %q file %d", store.upserted.ContentID, store.upserted.FileID)
	}
	if store.upserted.Location != "epubcfi(/6/4)" || store.upserted.Progress != 0.42 {
		t.Fatalf("progress = %+v", store.upserted)
	}
}

func TestEbookReaderReturnsSavedProgress(t *testing.T) {
	updatedAt := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	handler := NewEbookReaderHandler(&MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{},
		ItemAccess:   stubItemAccessChecker{},
	})
	handler.ProgressStore = &fakeEbookProgressStore{
		progress: &EbookReaderProgress{
			UserID:    1,
			ProfileID: "profile-1",
			ContentID: "ebook-1",
			FileID:    42,
			Location:  "epubcfi(/6/8)",
			Progress:  0.75,
			UpdatedAt: updatedAt,
		},
	}

	req := newEbookReaderAuthRequest(http.MethodGet, "/ebooks/ebook-1/progress")
	req = withEbookReaderContentRouteParam(req, "ebook-1")

	rr := httptest.NewRecorder()
	handler.HandleGetProgress(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		`"content_id":"ebook-1"`,
		`"file_id":42`,
		`"location":"epubcfi(/6/8)"`,
		`"progress":0.75`,
		`"updated_at":"2026-06-08T12:00:00Z"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("response %s missing %s", body, want)
		}
	}
}
