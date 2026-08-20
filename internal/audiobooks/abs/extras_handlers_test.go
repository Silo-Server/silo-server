package abs

import (
	"context"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type ebookPrimaryTestStore struct {
	*stubMediaStore
	files      []*models.MediaFile
	primaryID  int
	configured bool
	hasPrimary bool
	cleared    bool
	setID      int
}

type metadataAuthorizerStub struct{ allowed bool }

func (s metadataAuthorizerStub) ResolveABSAccess(context.Context, string, string) (catalog.AccessFilter, error) {
	return catalog.AccessFilter{}, nil
}

func (s metadataAuthorizerStub) CanCurateMetadata(context.Context, string, string) (bool, error) {
	return s.allowed, nil
}

func (s *ebookPrimaryTestStore) GetMediaFiles(context.Context, string, catalog.AccessFilter) ([]*models.MediaFile, error) {
	return s.files, nil
}

func (s *ebookPrimaryTestStore) GetPrimaryEbookFileID(context.Context, string) (int, bool, bool, error) {
	return s.primaryID, s.configured, s.hasPrimary, nil
}

func (s *ebookPrimaryTestStore) SetPrimaryEbookFileID(_ context.Context, _ string, fileID int) error {
	s.setID = fileID
	return nil
}

func (s *ebookPrimaryTestStore) ClearPrimaryEbookFileID(context.Context, string) error {
	s.cleared = true
	return nil
}

func TestEbookContentType(t *testing.T) {
	tests := map[string]string{
		".epub": ebookEPUBMimeType,
		".PDF":  "application/pdf",
		".cbz":  "application/vnd.comicbook+zip",
		".m4b":  "",
	}
	for ext, want := range tests {
		if got := ebookContentType(ext); got != want {
			t.Errorf("ebookContentType(%q) = %q, want %q", ext, got, want)
		}
	}
}

func TestSelectEbookFile(t *testing.T) {
	files := []*models.MediaFile{
		{ID: 1, FilePath: "/books/supplement.pdf"},
		{ID: 2, FilePath: "/books/primary.epub"},
		{ID: 3, FilePath: "/books/audio.m4b"},
	}
	if got := selectEbookFile(files, 0); got == nil || got.ID != 2 {
		t.Fatalf("primary ebook = %#v, want EPUB id 2", got)
	}
	if got := selectEbookFile(files, 1); got == nil || got.ID != 1 {
		t.Fatalf("requested ebook = %#v, want PDF id 1", got)
	}
	if got := selectEbookFile(files, 3); got != nil {
		t.Fatalf("audio file selected as ebook: %#v", got)
	}
}

func TestEbookStatusToggleClearsEffectivePrimary(t *testing.T) {
	store := &ebookPrimaryTestStore{
		stubMediaStore: &stubMediaStore{known: map[string]*models.MediaItem{
			testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
		}},
		files: []*models.MediaFile{{ID: 2, FilePath: "/books/primary.epub"}},
	}
	h := New(Dependencies{MediaStore: store, AccessResolver: metadataAuthorizerStub{allowed: true}})
	rec := dispatchABSWithParams(http.MethodPatch, "/api/items/ebook-1/ebook/2/status",
		map[string]string{"id": testEbookID, "fileid": "2"}, nil, "1", testProfileID, h.handleEbookStatus)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !store.cleared {
		t.Fatal("effective primary toggle did not persist the no-primary state")
	}
	if store.setID != 0 {
		t.Fatalf("SetPrimaryEbookFileID called with %d", store.setID)
	}
}

func TestEbookStatusRequiresMetadataCuration(t *testing.T) {
	store := &ebookPrimaryTestStore{
		stubMediaStore: &stubMediaStore{known: map[string]*models.MediaItem{
			testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
		}},
		files: []*models.MediaFile{{ID: 2, FilePath: "/books/primary.epub"}},
	}
	h := New(Dependencies{MediaStore: store, AccessResolver: metadataAuthorizerStub{}})
	rec := dispatchABSWithParams(http.MethodPatch, "/api/items/ebook-1/ebook/2/status",
		map[string]string{"id": testEbookID, "fileid": "2"}, nil, "1", testProfileID, h.handleEbookStatus)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if store.cleared || store.setID != 0 {
		t.Fatal("unauthorized request mutated the server-global primary ebook selection")
	}
}

func TestEbookFilePreservesSpecificContentType(t *testing.T) {
	filename := `reader "日本語".epub`
	filePath := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(filePath, []byte(mediaTypeEbook), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &ebookPrimaryTestStore{
		stubMediaStore: &stubMediaStore{known: map[string]*models.MediaItem{
			testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook},
		}},
		files: []*models.MediaFile{{ID: 2, FilePath: filePath}},
	}
	h := New(Dependencies{MediaStore: store})
	rec := dispatchABSWithParams(http.MethodGet, "/api/items/ebook-1/ebook",
		map[string]string{"id": testEbookID}, nil, "1", testProfileID, h.handleEbookFile)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != ebookEPUBMimeType {
		t.Fatalf("Content-Type = %q, want application/epub+zip", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	disposition, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("parse Content-Disposition: %v", err)
	}
	if disposition != "inline" || params["filename"] != filename {
		t.Fatalf("Content-Disposition = %q, want safe filename %q", rec.Header().Get("Content-Disposition"), filename)
	}
}
