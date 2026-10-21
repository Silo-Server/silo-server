package handlers

import (
	"context"
	"encoding/json"
	"io"
	"mime"
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

func withEbookReaderAnnotationRouteParams(req *http.Request, contentID, annotationID string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("content_id", contentID)
	routeCtx.URLParams.Add("annotation_id", annotationID)
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

type fakeEbookReaderAnnotationStore struct {
	items    []EbookReaderAnnotation
	existing *EbookReaderAnnotation
	created  *EbookReaderAnnotation
	updated  *EbookReaderAnnotation
	deleted  string
	err      error
}

func (s *fakeEbookReaderAnnotationStore) List(context.Context, int, string, string) ([]EbookReaderAnnotation, error) {
	return s.items, s.err
}

func (s *fakeEbookReaderAnnotationStore) Create(_ context.Context, annotation EbookReaderAnnotation) error {
	if s.err != nil {
		return s.err
	}
	s.created = &annotation
	s.items = append(s.items, annotation)
	return nil
}

// Update models the PG store contract: read the stored row, invoke merge with
// it, and persist the merged result, all as one atomic operation.
func (s *fakeEbookReaderAnnotationStore) Update(
	_ context.Context,
	_ int,
	_ string,
	_ string,
	annotationID string,
	merge func(existing EbookReaderAnnotation) (EbookReaderAnnotation, error),
) (*EbookReaderAnnotation, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.existing == nil || s.existing.ID != annotationID {
		return nil, nil
	}
	existing := *s.existing
	merged, err := merge(existing)
	if err != nil {
		return nil, err
	}
	s.updated = &merged
	stored := merged
	s.existing = &stored
	return &merged, nil
}

func (s *fakeEbookReaderAnnotationStore) Delete(_ context.Context, userID int, profileID, contentID, annotationID string) error {
	if s.err != nil {
		return s.err
	}
	s.deleted = annotationID
	return nil
}

func TestEbookReaderServesContainerMimeForMismatchedExtension(t *testing.T) {
	filePath := writePlaybackTestMediaFile(t, "book.txt")
	if err := os.WriteFile(filePath, []byte("<html>not really</html>"), 0o644); err != nil {
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
	req = withEbookReaderRouteParams(req, "ebook-1", "42")

	rr := httptest.NewRecorder()
	handler.HandleReadFile(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	// The file is admitted via its validated container, so the response must
	// carry that container's MIME type, never application/octet-stream.
	if got := rr.Header().Get("Content-Type"); got != "application/epub+zip" {
		t.Fatalf("content-type = %q, want application/epub+zip", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("x-content-type-options = %q, want nosniff", got)
	}
}

func TestEbookMimeTypeNeverReturnsOctetStreamForAdmittedFiles(t *testing.T) {
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
		if !isEbookFile(file) {
			t.Fatalf("isEbookFile(%q, %q) = false, want admitted", tc.path, tc.container)
		}
		if got := ebookMimeType(tc.path, tc.container); got != tc.wantMime {
			t.Fatalf("ebookMimeType(%q, %q) = %q, want %q", tc.path, tc.container, got, tc.wantMime)
		}
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
		"book.md":      "application/octet-stream",
		"book.unknown": "application/octet-stream",
	}
	rejected := map[string]bool{"book.md": true, "book.unknown": true}

	for name, wantMime := range cases {
		t.Run(name, func(t *testing.T) {
			container := name
			if name == "book.fb2.zip" {
				container = ""
			}
			file := &models.MediaFile{FilePath: "/library/" + name, Container: container}
			if rejected[name] {
				file.BaseType = "ebook"
				if isEbookFile(file) {
					t.Fatalf("%s should not be treated as an ebook reader format", name)
				}
				if got := ebookMimeType(file.FilePath, file.Container); got != wantMime {
					t.Fatalf("ebookMimeType() = %q, want %q", got, wantMime)
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

func existingEbookReaderAnnotation() *EbookReaderAnnotation {
	createdAt := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	return &EbookReaderAnnotation{
		ID:           "ann-1",
		UserID:       1,
		ProfileID:    "profile-1",
		ContentID:    "ebook-1",
		Kind:         "highlight",
		CFIRange:     "epubcfi(/6/4,/1:0,/1:12)",
		SelectedText: "sample text",
		Note:         "original note",
		Style:        "highlight",
		Color:        "#facc15",
		Metadata:     json.RawMessage(`{"chapter":"one"}`),
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}
}

func patchEbookReaderAnnotation(t *testing.T, store *fakeEbookReaderAnnotationStore, body string) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewEbookReaderHandler(&MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{},
		ItemAccess:   stubItemAccessChecker{},
	})
	handler.AnnotationStore = store

	req := newEbookReaderAuthRequest(http.MethodPatch, "/ebooks/ebook-1/annotations/ann-1")
	req.Body = io.NopCloser(strings.NewReader(body))
	req = withEbookReaderAnnotationRouteParams(req, "ebook-1", "ann-1")

	rr := httptest.NewRecorder()
	handler.HandleUpdateAnnotation(rr, req)
	return rr
}

func TestEbookReaderPatchKeepsAbsentFields(t *testing.T) {
	original := existingEbookReaderAnnotation()
	store := &fakeEbookReaderAnnotationStore{existing: original}

	rr := patchEbookReaderAnnotation(t, store, `{"note":"updated","color":"#bfdbfe"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if store.updated == nil || store.updated.ID != "ann-1" || store.updated.Note != "updated" || store.updated.Color != "#bfdbfe" {
		t.Fatalf("updated = %+v", store.updated)
	}
	if !store.updated.UpdatedAt.After(original.UpdatedAt) {
		t.Fatalf("updated_at %v did not advance past %v", store.updated.UpdatedAt, original.UpdatedAt)
	}
	if string(store.updated.Metadata) != `{"chapter":"one"}` {
		t.Fatalf("metadata should be preserved when absent, got %s", store.updated.Metadata)
	}
	if store.updated.Kind != "highlight" || store.updated.CFIRange != "epubcfi(/6/4,/1:0,/1:12)" {
		t.Fatalf("absent fields should keep stored values, got %+v", store.updated)
	}
}

func TestEbookReaderPatchClearsExplicitlyEmptyFields(t *testing.T) {
	store := &fakeEbookReaderAnnotationStore{existing: existingEbookReaderAnnotation()}

	rr := patchEbookReaderAnnotation(t, store, `{"note":"","metadata":{}}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if store.updated == nil || store.updated.Note != "" {
		t.Fatalf("note should be cleared, got %+v", store.updated)
	}
	if string(store.updated.Metadata) != `{}` {
		t.Fatalf("metadata should be cleared to {}, got %s", store.updated.Metadata)
	}
}

func TestEbookReaderPatchValidatesResultingState(t *testing.T) {
	cases := map[string]string{
		"kind change without location": `{"kind":"bookmark"}`,
		"clearing required cfi_range":  `{"cfi_range":""}`,
		"unknown kind":                 `{"kind":"doodle"}`,
		"non-object metadata":          `{"metadata":["bad"]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeEbookReaderAnnotationStore{existing: existingEbookReaderAnnotation()}
			rr := patchEbookReaderAnnotation(t, store, body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			if store.updated != nil {
				t.Fatalf("invalid patch must not reach the store, got %+v", store.updated)
			}
		})
	}
}

func TestEbookReaderRejectsOversizedProgressBody(t *testing.T) {
	store := &fakeEbookProgressStore{}
	handler := NewEbookReaderHandler(&MediaFileAuthorizer{
		FileResolver: stubMediaFileResolver{},
		ItemAccess:   stubItemAccessChecker{},
	})
	handler.ProgressStore = store

	body := `{"file_id":42,"location":"` + strings.Repeat("x", int(ebookReaderProgressMaxBodySize)) + `","progress":0.5}`
	req := newEbookReaderAuthRequest(http.MethodPut, "/ebooks/ebook-1/progress")
	req.Body = io.NopCloser(strings.NewReader(body))
	req = withEbookReaderContentRouteParam(req, "ebook-1")

	rr := httptest.NewRecorder()
	handler.HandleSaveProgress(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if store.upserted != nil {
		t.Fatalf("oversized body must not be saved, got %+v", store.upserted)
	}
}

func TestInlineContentDispositionProducesParseableHeaders(t *testing.T) {
	cases := map[string]string{
		"book.epub":            "book.epub",
		`bo"ok\.epub`:          `bo"ok\.epub`,
		`trailing-slash\`:      `trailing-slash\`,
		"börk \U0001F4DA.epub": "börk \U0001F4DA.epub",
		"":                     "ebook",
		".":                    "ebook",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got := inlineContentDisposition(name)
			mediaType, params, err := mime.ParseMediaType(got)
			if err != nil {
				t.Fatalf("header %q is malformed: %v", got, err)
			}
			if mediaType != "inline" {
				t.Fatalf("media type = %q", mediaType)
			}
			if params["filename"] != want {
				t.Fatalf("filename = %q, want %q", params["filename"], want)
			}
		})
	}
}
