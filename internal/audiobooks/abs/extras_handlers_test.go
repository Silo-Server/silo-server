package abs

import (
	"context"
	"errors"
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

func (s *ebookPrimaryTestStore) GetPrimaryEbookFileID(context.Context, string) (EbookPrimarySelection, error) {
	return EbookPrimarySelection{FileID: s.primaryID, Configured: s.configured, HasPrimary: s.hasPrimary}, nil
}

func (s *ebookPrimaryTestStore) SetPrimaryEbookFileID(_ context.Context, _ string, fileID int) error {
	s.setID = fileID
	return nil
}

func (s *ebookPrimaryTestStore) ClearPrimaryEbookFileID(context.Context, string) error {
	s.cleared = true
	return nil
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

// TestEbookEndpointsRejectNonEbookItems pins the item-type guard: the ABS item
// detail only ever advertises an ebookFile for an ebook item, so serving (or
// repointing) a supplementary ebook attached to an audiobook through these
// routes would be a shape the client never asked for. Upstream ABS answers a
// client error for a non-book item; these return 400.
func TestEbookEndpointsRejectNonEbookItems(t *testing.T) {
	newStore := func() *ebookPrimaryTestStore {
		return &ebookPrimaryTestStore{
			stubMediaStore: &stubMediaStore{known: map[string]*models.MediaItem{
				testEbookID: {ContentID: testEbookID, Type: mediaTypeAudiobook},
			}},
			files: []*models.MediaFile{{ID: 2, FilePath: "/books/primary.epub"}},
		}
	}

	t.Run("file", func(t *testing.T) {
		store := newStore()
		h := New(Dependencies{MediaStore: store})
		rec := dispatchABSWithParams(http.MethodGet, "/api/items/ebook-1/ebook",
			map[string]string{"id": testEbookID}, nil, "1", testProfileID, h.handleEbookFile)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("status", func(t *testing.T) {
		store := newStore()
		h := New(Dependencies{MediaStore: store, AccessResolver: metadataAuthorizerStub{allowed: true}})
		rec := dispatchABSWithParams(http.MethodPatch, "/api/items/ebook-1/ebook/2/status",
			map[string]string{"id": testEbookID, "fileid": "2"}, nil, "1", testProfileID, h.handleEbookStatus)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if store.cleared || store.setID != 0 {
			t.Fatal("a non-ebook item mutated the primary ebook selection")
		}
	})
}

// TestEbookEndpointsReportItemLookupFailures separates "this item is not
// visible to you" from "the lookup itself failed": a database error must not
// masquerade as a 404, which clients cache as a missing book.
func TestEbookEndpointsReportItemLookupFailures(t *testing.T) {
	newStore := func() *ebookPrimaryTestStore {
		return &ebookPrimaryTestStore{
			stubMediaStore: &stubMediaStore{lookupErr: errors.New("db down")},
			files:          []*models.MediaFile{{ID: 2, FilePath: "/books/primary.epub"}},
		}
	}

	t.Run("file", func(t *testing.T) {
		h := New(Dependencies{MediaStore: newStore()})
		rec := dispatchABSWithParams(http.MethodGet, "/api/items/ebook-1/ebook",
			map[string]string{"id": testEbookID}, nil, "1", testProfileID, h.handleEbookFile)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("status", func(t *testing.T) {
		store := newStore()
		h := New(Dependencies{MediaStore: store, AccessResolver: metadataAuthorizerStub{allowed: true}})
		rec := dispatchABSWithParams(http.MethodPatch, "/api/items/ebook-1/ebook/2/status",
			map[string]string{"id": testEbookID, "fileid": "2"}, nil, "1", testProfileID, h.handleEbookStatus)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
		}
		if store.cleared || store.setID != 0 {
			t.Fatal("a failed lookup mutated the primary ebook selection")
		}
	})
}

// TestEbookFileReportsMissingItemAsNotFound keeps the missing/inaccessible
// item on 404 rather than the 400 the type guard emits.
func TestEbookFileReportsMissingItemAsNotFound(t *testing.T) {
	store := &ebookPrimaryTestStore{
		stubMediaStore: &stubMediaStore{},
		files:          []*models.MediaFile{{ID: 2, FilePath: "/books/primary.epub"}},
	}
	h := New(Dependencies{MediaStore: store})
	rec := dispatchABSWithParams(http.MethodGet, "/api/items/ebook-1/ebook",
		map[string]string{"id": testEbookID}, nil, "1", testProfileID, h.handleEbookFile)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}
