package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// fixtureItemCollections answers one server and one personal collection for
// movie:harbor-lights-2019, the one server collection with a preview of its
// titles for movie:night-ferry-2021, and 404 for any other title.
type fixtureItemCollections struct{}

func (fixtureItemCollections) ItemCollections(_ context.Context, _ handlers.ItemViewer, _ int, contentID string) (handlers.ItemCollectionsView, error) {
	library, creator := 1, "p-owner"
	crime := sections.CollectionCard{ID: "01J9Z8C3W4R5T6Y7U8I9O0P1Q2", Source: sections.CollectionCardLibrary, Title: "Crime classics", PosterURL: "/api/v2/images/collections/01J9Z8C3W4R5T6Y7U8I9O0P1Q2/poster", ItemCount: 24, LibraryID: &library}
	switch contentID {
	case "movie:harbor-lights-2019":
		return handlers.ItemCollectionsView{Cards: []sections.CollectionCard{
			crime,
			{ID: "c1", Source: sections.CollectionCardUser, Title: "Favorites", PosterURL: "/api/v2/images/user-collections/c1/collage", PosterIsCollage: true, ItemCount: 7, CreatorProfileID: &creator},
		}}, nil
	case "movie:night-ferry-2021":
		return handlers.ItemCollectionsView{Cards: []sections.CollectionCard{crime}, Preview: []handlers.CollectionItemView{
			{ContentID: "movie:harbor-lights-2019", Type: "movie", Title: "Harbor Lights", Year: 2019, PosterURL: "/api/v2/images/items/movie:harbor-lights-2019/poster"},
		}}, nil
	}
	return handlers.ItemCollectionsView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
}

// catalogItemCollectionsFixtureCases pin a title page's "Also in" row.
func catalogItemCollectionsFixtureCases() []fixtureCase {
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	problem := "#/components/schemas/Problem"
	return []fixtureCase{
		{name: "list_catalog_item_collections_ok", operationID: opListItemCollections,
			scenario: "The collections a movie's page lists under Also in: a server collection, then one of the profile's own.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/movie:harbor-lights-2019/collections?library_id=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogItemCollections"},
		{name: "list_catalog_item_collections_single_preview", operationID: opListItemCollections,
			scenario: "A movie only one collection holds: its card, and its other titles to show in place of the card.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/movie:night-ferry-2021/collections?library_id=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogItemCollections"},
		{name: "list_catalog_item_collections_not_found", operationID: opListItemCollections,
			scenario: "A title the viewer cannot see answers 404, whatever collections hold it.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/movie:hidden/collections", headers: viewer,
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_catalog_item_collections_authentication_required", operationID: opListItemCollections,
			scenario: "The Also in row without a credential.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/movie:harbor-lights-2019/collections",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
	}
}
