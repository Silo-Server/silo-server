package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// A query cursor issued under an older sort order asks the client to restart
// from the first page, like a collection that changed, instead of failing.
func TestCollectionPageErrorRestartsOnChangedCursor(t *testing.T) {
	for name, err := range map[string]error{
		"collection changed":   userstore.ErrCollectionChanged,
		"query cursor changed": fmt.Errorf("%w: cursor key count does not match sort", catalog.ErrCatalogCursorChanged),
	} {
		t.Run(name, func(t *testing.T) {
			var apiErr *APIError
			if !errors.As(collectionPageError(err), &apiErr) {
				t.Fatalf("collectionPageError(%v) is not an APIError", err)
			}
			if apiErr.Status != http.StatusConflict || apiErr.Code != "collection_changed" {
				t.Fatalf("got %d %s, want 409 collection_changed", apiErr.Status, apiErr.Code)
			}
		})
	}
}
