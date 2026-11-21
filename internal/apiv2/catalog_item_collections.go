package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// Section catalog-item-collections: the "Also in" row of a movie or series
// page, the collections that store the title and that the viewer may see.
// getCatalogSearchCapabilities.item_collections advertises it.

const opListItemCollections = "listCatalogItemCollections"

// ItemCollectionService is the slice of *handlers.ItemCollections the read
// uses.
type ItemCollectionService interface {
	ItemCollections(ctx context.Context, v handlers.ItemViewer, userID int, contentID string) (handlers.ItemCollectionsView, error)
}

// CatalogItemCollectionsInput names the title whose collections a page shows.
type CatalogItemCollectionsInput struct {
	ID        string `path:"id" doc:"Content id of the movie or series" example:"movie:heat-1995"`
	LibraryID ID     `query:"library_id" doc:"The library the title is being viewed in. It picks the box set whose synced copy is left out, as getCatalogItemBoxSet does. Absent uses the first of the title's libraries with box sets on" example:"1"`
	ArtworkQuery
}

// CatalogItemCollections is the listCatalogItemCollections response.
type CatalogItemCollections struct {
	Items        []SectionCollectionCard `json:"items" doc:"Server collections first, then personal collections, each by title; empty, never null"`
	PreviewItems []CatalogItem           `json:"preview_items" doc:"When items holds exactly one collection, up to 6 of its other titles as the first page of listCatalogItems lists the collection (its order, Show only filter and access rules), so a page can show them in place of its card; empty, never null, otherwise"`
}

// CatalogItemCollectionsOutput is the listCatalogItemCollections response.
type CatalogItemCollectionsOutput struct {
	Body CatalogItemCollections
}

func registerCatalogItemCollections(reg *Registry) {
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/items/{id}/collections", opListItemCollections, "catalog",
		"The collections a title's page lists under \"Also in\": visible manual and synced collections, server and personal, that store the title. Smart collections are not listed, nor the synced copy of the box set the page shows. When only one collection holds the title, preview_items carries up to 6 of its other titles. 404 when the ID is missing, hidden, or not a catalog title.")), reg.listCatalogItemCollections)
}

func (reg *Registry) listCatalogItemCollections(ctx context.Context, in *CatalogItemCollectionsInput) (*CatalogItemCollectionsOutput, error) {
	if reg.deps.ItemCollections == nil || reg.deps.CatalogAccess == nil {
		return nil, unavailable("item collections")
	}
	userID, _, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	viewer, p := reg.itemViewer(ctx, in.Artwork(), in.LibraryID, "")
	if p != nil {
		return nil, p
	}
	view, err := reg.deps.ItemCollections.ItemCollections(ctx, viewer, userID, in.ID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := sectionCollectionCardsOf(view.Cards)
	if items == nil {
		items = []SectionCollectionCard{}
	}
	sel := reg.ratingSelection(ctx)
	preview := make([]CatalogItem, 0, len(view.Preview))
	for _, item := range view.Preview {
		preview = append(preview, catalogItemOfListing(item, sel))
	}
	return &CatalogItemCollectionsOutput{Body: CatalogItemCollections{Items: items, PreviewItems: preview}}, nil
}
