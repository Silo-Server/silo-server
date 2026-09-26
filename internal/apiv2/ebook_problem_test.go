package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type inaccessibleEbook struct{}

func (inaccessibleEbook) GetByID(context.Context, int) (*models.MediaFile, error) {
	return &models.MediaFile{ID: 42, ContentID: "book", BaseType: "ebook", Container: "epub"}, nil
}

func (inaccessibleEbook) EnsureAccessible(context.Context, string, catalogpkg.AccessFilter) error {
	return catalogpkg.ErrItemNotFound
}

func TestEbookInaccessibleStateIsNotFound(t *testing.T) {
	handler := handlers.NewEbookReaderHandler(&handlers.MediaFileAuthorizer{ItemAccess: inaccessibleEbook{}, FileResolver: inaccessibleEbook{}})
	// Nil pool stores satisfy readiness and should never be touched: access is
	// refused before the read/write reaches storage.
	handler.ProgressStore = handlers.NewPGEbookReaderProgressStore(nil)
	handler.ConfigStore = handlers.NewPGEbookReaderConfigStore(nil)
	deps := pilotDeps(nil, nil)
	deps.EbookProgress = handler
	deps.EbookConfig = handler
	handler.AnnotationStore = handlers.NewPGEbookReaderAnnotationStore(nil)
	deps.EbookAnnotations = handler
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	for _, path := range []string{"/ebooks/book/progress", "/ebooks/book/reader-config", "/ebooks/book/annotations"} {
		rec := do(t, h, http.MethodGet, Prefix+path, "", viewer)
		if rec.Code != 404 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, h, http.MethodPut, Prefix+"/ebooks/book/reader-config", `{"config":{}}`, with(viewer, "If-Match", "*"))
	if rec.Code != 404 {
		t.Fatalf("config mutation: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, Prefix+"/ebooks/book/progress", `{"file_id":"42","location":"here","progress":0.2,"updated_at":"2026-01-01T00:00:00.000Z"}`, viewer)
	if rec.Code != 404 {
		t.Fatalf("progress mutation: %d %s", rec.Code, rec.Body.String())
	}
}

func TestEbookInaccessibleAnnotationMutations(t *testing.T) {
	handler := handlers.NewEbookReaderHandler(&handlers.MediaFileAuthorizer{ItemAccess: inaccessibleEbook{}})
	handler.AnnotationStore = handlers.NewPGEbookReaderAnnotationStore(nil)
	deps := pilotDeps(nil, nil)
	deps.EbookAnnotations = handler
	h := newTestHandler(t, deps)
	viewer := with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "If-Match", "*")
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/ebooks/book/annotations", `{"id":"intent","kind":"note","location":"here"}`},
		{"PATCH", "/ebooks/book/annotations/intent", `{"note":"changed"}`},
		{"DELETE", "/ebooks/book/annotations/intent", ""},
	} {
		rec := do(t, h, tc.method, Prefix+tc.path, tc.body, viewer)
		if rec.Code != 404 {
			t.Fatalf("%s: %d %s", tc.method, rec.Code, rec.Body.String())
		}
	}
}
