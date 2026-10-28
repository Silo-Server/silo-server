package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Every library view seam refuses a library the viewer's allowlist excludes
// with not_found, so a restricted profile cannot probe which libraries exist.
// The handlers are built on nil repositories, so a skipped check panics
// rather than passes.
func TestLibraryViewsRefuseLibraryOutsideViewerScope(t *testing.T) {
	sectionsHandler := NewSectionHandler(nil, nil)
	collections := NewLibraryCollectionHandler(nil, nil, nil, nil)
	scope := access.Scope{UserID: 1, ProfileID: "p-kid", ProfileVerified: true, AllowedLibraryIDs: []int{2}, LibrariesRestricted: true}
	ctx := access.SetScope(context.Background(), scope)
	viewer := SectionViewer{Access: catalog.AccessFilter{UserID: 1, ProfileID: "p-kid"}}

	for name, call := range map[string]func() error{
		"layout": func() error { _, err := sectionsHandler.LibraryLayout(ctx, 1); return err },
		"sections": func() error {
			_, err := sectionsHandler.LibrarySections(ctx, 1, viewer)
			return err
		},
		"section items": func() error {
			_, err := sectionsHandler.LibrarySectionItems(ctx, 1, "default-recently-added", viewer)
			return err
		},
		"collections tab": func() error {
			_, err := collections.LibraryCollectionsTab(ctx, 1, 1, "p-kid")
			return err
		},
		"user collections": func() error {
			_, err := collections.LibraryUserCollections(ctx, 1, 1, "p-kid")
			return err
		},
		"collection items": func() error {
			_, err := collections.LibraryCollectionItemsPage(ctx, 1, "c1", viewer.Access, userstore.CollectionItemsPageOptions{}, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			var apiErr *APIError
			if err := call(); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || apiErr.Code != "not_found" {
				t.Fatalf("error = %#v, want 404 not_found", err)
			}
		})
	}
}
