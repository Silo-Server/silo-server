package abs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fileStreamTestStore struct {
	*stubMediaStore
	files []*models.MediaFile
}

func (s *fileStreamTestStore) GetMediaFiles(context.Context, string, catalog.AccessFilter) ([]*models.MediaFile, error) {
	return s.files, nil
}

func TestFileStreamRoutesRegisterHead(t *testing.T) {
	h := New(Dependencies{MediaStore: &stubMediaStore{}})
	router := chi.NewRouter()
	h.Mount(router)

	for _, path := range []string{
		"/api/items/book-1/file/1",
		"/api/items/book-1/file/1/download",
		"/abs/api/items/book-1/file/1",
		"/abs/api/items/book-1/file/1/download",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodHead, path, nil)
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("HEAD %s status = %d, want auth middleware 401 (route registered)", path, rec.Code)
		}
	}
}

func TestFileStreamNumericIdentifierIsResolvedByMediaType(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.bin")
	secondPath := filepath.Join(dir, "second.bin")
	if err := os.WriteFile(firstPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []*models.MediaFile{{ID: 1, FilePath: firstPath}, {ID: 99, FilePath: secondPath}}

	tests := []struct {
		name     string
		itemType string
		wantBody string
	}{
		{name: "audiobook numeric value is raw track index", itemType: mediaTypeAudiobook, wantBody: "second"},
		{name: "ebook numeric value is media file id", itemType: mediaTypeEbook, wantBody: "first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fileStreamTestStore{
				stubMediaStore: &stubMediaStore{known: map[string]*models.MediaItem{
					testBookID: {ContentID: testBookID, Type: tt.itemType},
				}},
				files: files,
			}
			h := New(Dependencies{MediaStore: store})
			rec := dispatchABSWithParams(http.MethodGet, "/api/items/book-1/file/1",
				map[string]string{libraryItemIDKey: testBookID, "ino": "1"}, nil, "1", "", h.handleFileStream)
			if rec.Code != http.StatusOK || rec.Body.String() != tt.wantBody {
				t.Fatalf("status/body = %d/%q, want 200/%q", rec.Code, rec.Body.String(), tt.wantBody)
			}
		})
	}
}

// TestFileStreamSeparatesLookupFailureFromMissingItem pins the split the ABS
// clients depend on: an unreachable database is a retryable 500, while a
// missing or inaccessible item stays a 404 the client can cache.
func TestFileStreamSeparatesLookupFailureFromMissingItem(t *testing.T) {
	tests := []struct {
		name  string
		store *fileStreamTestStore
		want  int
	}{
		{
			name:  "lookup failure",
			store: &fileStreamTestStore{stubMediaStore: &stubMediaStore{lookupErr: errors.New("db down")}},
			want:  http.StatusInternalServerError,
		},
		{
			name:  "missing item",
			store: &fileStreamTestStore{stubMediaStore: &stubMediaStore{}},
			want:  http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New(Dependencies{MediaStore: tt.store})
			rec := dispatchABSWithParams(http.MethodGet, "/api/items/book-1/file/1",
				map[string]string{libraryItemIDKey: testBookID, "ino": "1"}, nil, "1", "", h.handleFileStream)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

// TestFileStreamUsesTypeProjection keeps the Range-request path off the
// hydrating item fetch: GetAudiobookByID also loads people and series, and
// only the item's kind is read here.
func TestFileStreamUsesTypeProjection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(path, []byte("book"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fileStreamTestStore{
		stubMediaStore: &stubMediaStore{known: map[string]*models.MediaItem{
			testBookID: {ContentID: testBookID, Type: mediaTypeEbook},
		}},
		files: []*models.MediaFile{{ID: 1, FilePath: path}},
	}
	h := New(Dependencies{MediaStore: store})

	rec := dispatchABSWithParams(http.MethodGet, "/api/items/book-1/file/1",
		map[string]string{libraryItemIDKey: testBookID, "ino": "1"}, nil, "1", "", h.handleFileStream)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if store.fullItemLoads != 0 {
		t.Fatalf("GetAudiobookByID called %d times on the file-stream path", store.fullItemLoads)
	}
	if got := rec.Header().Get("Content-Type"); got != ebookEPUBMimeType {
		t.Fatalf("Content-Type = %q, want %q", got, ebookEPUBMimeType)
	}
}
