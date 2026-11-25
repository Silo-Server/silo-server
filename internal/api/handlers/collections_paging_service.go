package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type PersonalCollectionPageView struct {
	Query    *catalog.QueryCursor
	Items    []PersonalCollectionItemView
	Revision int64
	HasMore  bool
	Last     *userstore.CollectionItemPosition
}
type LibraryCollectionPageView struct {
	Query    *catalog.QueryCursor
	Items    []CollectionItemView
	Revision int64
	HasMore  bool
	Last     *userstore.CollectionItemPosition
}

func collectionPageError(err error) error {
	// A query cursor issued under an older sort order needs the same restart.
	if errors.Is(err, userstore.ErrCollectionChanged) || errors.Is(err, catalog.ErrCatalogCursorChanged) {
		return apiError(http.StatusConflict, "collection_changed", "The collection changed; restart from the first page")
	}
	if errors.Is(err, userstore.ErrCollectionNotFound) || errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
		return apiError(http.StatusNotFound, "not_found", "Collection not found")
	}
	return apiError(http.StatusInternalServerError, "internal_error", "Failed to load collection items")
}
func (h *CollectionHandler) PersonalCollectionItemsPage(ctx context.Context, userID int, profileID, id string, access catalog.AccessFilter, opts userstore.CollectionItemsPageOptions, afterQuery *catalog.QueryCursor) (PersonalCollectionPageView, error) {
	out := PersonalCollectionPageView{Items: []PersonalCollectionItemView{}}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return out, collectionPageError(err)
	}
	pager, ok := store.(userstore.CollectionItemsPager)
	if !ok {
		return out, apiError(http.StatusServiceUnavailable, "unavailable", "Collection paging is unavailable")
	}
	// The first bounded witness precedes authorization and the content read. The
	// second read must observe the same revision, including on an initial request.
	witness, err := pager.CollectionRevision(ctx, id)
	if err != nil {
		return out, collectionPageError(err)
	}
	_, c, err := h.personalCollectionStore(ctx, userID, profileID, id, false)
	if err != nil {
		return out, err
	}
	// Another profile's collection shows only what its owner can access too.
	access, err = catalog.PersonalCollectionFilter(ctx, h.CollectionOwners, access, userID, profileID, c.CreatorProfileID)
	if err != nil {
		return out, collectionPageError(err)
	}
	if catalog.IsLiveQueryType(c.CollectionType) {
		if opts.Revision != 0 && opts.Revision != witness {
			return out, collectionPageError(userstore.ErrCollectionChanged)
		}
		var def catalog.QueryDefinition
		if err := json.Unmarshal([]byte(c.QueryDefinition), &def); err != nil {
			return out, apiError(400, "bad_request", "Invalid smart collection query")
		}
		def = catalog.ApplySmartCollectionItemLimit(def.Normalize())
		if err := def.ValidateWithOptions(true, true); err != nil {
			return out, apiError(400, "bad_request", err.Error())
		}
		if h.Executor == nil {
			return out, apiError(503, "unavailable", "Catalog queries are unavailable")
		}
		page, err := h.Executor.PreviewCursorPage(ctx, def, access, opts.Limit, afterQuery, false)
		if err != nil {
			return out, collectionPageError(err)
		}
		revision, err := pager.CollectionRevision(ctx, id)
		if err != nil {
			return out, collectionPageError(err)
		}
		if revision != witness {
			return out, collectionPageError(userstore.ErrCollectionChanged)
		}
		consumed := 0
		if afterQuery != nil {
			consumed = afterQuery.Consumed
		}
		for index, item := range page.Items {
			out.Items = append(out.Items, PersonalCollectionItemView{CollectionID: id, MediaItemID: item.ContentID, Title: item.Title, Position: consumed + index, AddedAt: c.CreatedAt})
		}
		out.Revision = witness
		out.HasMore = page.HasMore
		out.Query = page.Next
		return out, nil
	}
	if opts.Revision != 0 && opts.Revision != witness {
		return out, collectionPageError(userstore.ErrCollectionChanged)
	}
	opts.Revision = witness
	page, err := pager.ListCollectionItemsPage(ctx, id, opts)
	if err != nil {
		return out, collectionPageError(err)
	}
	if h.Executor == nil || h.Executor.Pool == nil {
		return out, apiError(http.StatusServiceUnavailable, "unavailable", "Catalog access is unavailable")
	}
	ids := make([]string, 0, len(page.Items))
	for _, i := range page.Items {
		ids = append(ids, i.MediaItemID)
	}
	visible, err := catalog.NewItemRepository(h.Executor.Pool).GetListedByIDsWithAccess(ctx, ids, access)
	if err != nil {
		return out, collectionPageError(err)
	}
	allowed := make(map[string]string, len(visible))
	for _, i := range visible {
		allowed[i.ContentID] = i.Title
	}
	for _, i := range page.Items {
		if title, ok := allowed[i.MediaItemID]; ok {
			out.Items = append(out.Items, PersonalCollectionItemView{CollectionID: i.CollectionID, MediaItemID: i.MediaItemID, Title: title, Position: i.Position, AddedAt: i.AddedAt})
		}
	}
	if _, err = pager.ListCollectionItemsPage(ctx, id, userstore.CollectionItemsPageOptions{Limit: 1, Revision: page.Revision}); err != nil {
		return out, collectionPageError(err)
	}
	out.Revision = page.Revision
	out.HasMore = page.HasMore
	if len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		out.Last = &userstore.CollectionItemPosition{Position: last.Position, MediaItemID: last.MediaItemID}
	}
	return out, nil
}

// maxHiddenMemberBatches bounds the stored-member pages one page of a
// collection that hides watched titles reads.
const maxHiddenMemberBatches = 10

func (h *LibraryCollectionHandler) LibraryCollectionItemsPage(ctx context.Context, libraryID int, id string, access catalog.AccessFilter, opts userstore.CollectionItemsPageOptions, afterQuery *catalog.QueryCursor) (LibraryCollectionPageView, error) {
	out := LibraryCollectionPageView{Items: []CollectionItemView{}}
	if err := h.requireViewableLibrary(ctx, libraryID); err != nil {
		return out, err
	}
	witness, err := h.repo.CollectionRevision(ctx, id)
	if err != nil {
		return out, collectionPageError(err)
	}
	c, err := h.repo.GetByID(ctx, id)
	if err != nil || !collectionSpansLibrary(c, libraryID) || c.Visibility != catalog.LibraryCollectionVisibilityVisible {
		return out, apiError(http.StatusNotFound, "not_found", "Collection not found")
	}
	display := catalog.LibraryCollectionDisplayFor(c, access)
	if catalog.IsLiveQueryType(c.CollectionType) {
		if opts.Revision != 0 && opts.Revision != witness {
			return out, collectionPageError(userstore.ErrCollectionChanged)
		}
		var def catalog.QueryDefinition
		if err := json.Unmarshal(c.QueryDefinition, &def); err != nil {
			return out, apiError(400, "bad_request", "Invalid smart collection query")
		}
		def = catalog.ApplySmartCollectionItemLimit(def.Normalize())
		if err := def.ValidateWithOptions(false, false); err != nil {
			return out, apiError(400, "bad_request", err.Error())
		}
		if h.Executor == nil {
			return out, apiError(503, "unavailable", "Catalog queries are unavailable")
		}
		page, err := catalog.PreviewLibraryCollectionCursorPage(ctx, h.Executor, def, display, access, opts.Limit, afterQuery)
		if err != nil {
			return out, collectionPageError(err)
		}
		revision, err := h.repo.CollectionRevision(ctx, id)
		if err != nil {
			return out, collectionPageError(err)
		}
		if revision != witness {
			return out, collectionPageError(userstore.ErrCollectionChanged)
		}
		for _, item := range page.Items {
			out.Items = append(out.Items, h.itemListResponseOf(ctx, item))
		}
		h.setListingLogos(ctx, out.Items, page.Items, access)
		out.Revision = witness
		out.HasMore = page.HasMore
		out.Query = page.Next
		return out, nil
	}
	if opts.Revision != 0 && opts.Revision != witness {
		return out, collectionPageError(userstore.ErrCollectionChanged)
	}
	opts.Revision = witness
	// A collection that hides watched titles reads on past members the viewer
	// has watched, a bounded number of pages, so a page is not left empty
	// while unwatched members follow; the continuation is the last member read.
	batches := 1
	if display != "" {
		batches = maxHiddenMemberBatches
	}
	var listed []*models.MediaItem
	var revision int64
	for batch := 0; batch < batches; batch++ {
		page, err := h.repo.ListItemsPage(ctx, id, opts)
		if err != nil {
			return out, collectionPageError(err)
		}
		revision = page.Revision
		ids := make([]string, 0, len(page.Items))
		for _, i := range page.Items {
			ids = append(ids, i.MediaItemID)
		}
		items, err := h.itemRepo.GetListedByIDsWithAccess(ctx, ids, access)
		if err != nil {
			return out, collectionPageError(err)
		}
		if display != "" && h.Executor != nil {
			items, err = catalog.FilterCollectionItemsByDisplayQuery(ctx, h.Executor.Pool, items, display, access)
			if err != nil {
				return out, collectionPageError(err)
			}
		}
		byID := make(map[string]*models.MediaItem, len(items))
		for _, i := range items {
			byID[i.ContentID] = i
		}
		full := false
		for _, member := range page.Items {
			if len(out.Items) == opts.Limit {
				full = true
				break
			}
			out.Last = &userstore.CollectionItemPosition{Position: member.Position, MediaItemID: member.MediaItemID}
			if item, ok := byID[member.MediaItemID]; ok {
				out.Items = append(out.Items, h.itemListResponseOf(ctx, item))
				listed = append(listed, item)
			}
		}
		out.HasMore = full || page.HasMore
		if full || !page.HasMore || len(out.Items) == opts.Limit {
			break
		}
		opts.After = out.Last
	}
	h.setListingLogos(ctx, out.Items, listed, access)
	if _, err = h.repo.ListItemsPage(ctx, id, userstore.CollectionItemsPageOptions{Limit: 1, Revision: revision}); err != nil {
		return out, collectionPageError(err)
	}
	out.Revision = revision
	return out, nil
}
