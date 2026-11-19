package handlers

import (
	"context"
	"fmt"
	"slices"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// CollectionRowCards builds the cards of rows of collections
// (sections.CollectionCardSource) with the same per-viewer rules the
// collection lists use: server collections as ListServerCollections shows
// them, personal collections as ListPersonalCollections does.
type CollectionRowCards struct {
	Library  *LibraryCollectionHandler
	Personal *CollectionHandler
}

var _ sections.CollectionCardSource = (*CollectionRowCards)(nil)

// CollectionCards answers one card per ref the viewer may see, in ref order.
// A collection that is missing, hidden, in no library the viewer can open
// (or not in the library page's library), or whose titles the viewer sees
// none of is left out, as is a personal collection the profile can't see.
// The cards only reach /api/v2, so personal collections show their collages
// as the v2 collection lists do.
func (c *CollectionRowCards) CollectionCards(ctx context.Context, refs []recipes.CollectionRef, scope sections.CollectionCardScope) ([]sections.CollectionCard, error) {
	var libraryIDs, userIDs []string
	for _, ref := range refs {
		switch {
		case ref.LibraryCollectionID != "":
			libraryIDs = append(libraryIDs, ref.LibraryCollectionID)
		case ref.UserCollectionID != "" && !scope.AdminRow:
			userIDs = append(userIDs, ref.UserCollectionID)
		}
	}
	libraryCards, err := c.libraryCards(ctx, libraryIDs, scope)
	if err != nil {
		return nil, err
	}
	userCards, err := c.userCards(ctx, userIDs, scope)
	if err != nil {
		return nil, err
	}
	out := make([]sections.CollectionCard, 0, len(refs))
	for _, ref := range refs {
		var card sections.CollectionCard
		var ok bool
		if ref.LibraryCollectionID != "" {
			card, ok = libraryCards[ref.LibraryCollectionID]
		} else {
			card, ok = userCards[ref.UserCollectionID]
		}
		if ok {
			out = append(out, card)
		}
	}
	return out, nil
}

// libraryCards builds the cards of the server collections among ids that
// the viewer is shown, keyed by collection id.
func (c *CollectionRowCards) libraryCards(ctx context.Context, ids []string, scope sections.CollectionCardScope) (map[string]sections.CollectionCard, error) {
	out := map[string]sections.CollectionCard{}
	h := c.Library
	if len(ids) == 0 || h == nil || h.repo == nil {
		return out, nil
	}
	collections, err := h.repo.ListVisibleByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("loading row collections: %w", err)
	}
	if len(collections) == 0 {
		return out, nil
	}
	openable, err := c.openableLibraries(ctx, scope.LibraryID)
	if err != nil {
		return nil, err
	}
	shownIn := make(map[string]int, len(collections))
	kept := make([]*models.LibraryCollection, 0, len(collections))
	for _, collection := range collections {
		if library, ok := firstHoldingLibrary(collection, openable); ok {
			shownIn[collection.ID] = library
			kept = append(kept, collection)
		}
	}
	kept = catalog.CollectionsShownToViewer(kept, h.viewerCollectionCounts(ctx, kept, scope.Filter))
	for _, collection := range h.withViewerPosters(ctx, kept, scope.Filter) {
		tab := h.libraryTabCardOf(ctx, collection)
		library := shownIn[collection.ID]
		out[collection.ID] = sections.CollectionCard{
			ID:                tab.ID,
			Source:            sections.CollectionCardLibrary,
			Title:             tab.Title,
			PosterURL:         tab.PosterURL,
			PosterThumbhash:   tab.PosterThumbhash,
			PosterIsCollage:   tab.PosterIsCollage,
			BackdropURL:       h.presignBackdropURLCtx(ctx, collection.BackdropURL),
			BackdropThumbhash: collection.BackdropThumbhash,
			ItemCount:         tab.ItemCount,
			LibraryID:         &library,
		}
	}
	return out, nil
}

// openableLibraries lists, in library order, the enabled libraries the
// viewer can open: only page when a library page's row is read.
func (c *CollectionRowCards) openableLibraries(ctx context.Context, page *int) ([]int, error) {
	if page != nil {
		if !viewerCanAccessLibrary(ctx, *page) {
			return nil, nil
		}
		return []int{*page}, nil
	}
	if c.Library.FolderRepo == nil {
		return nil, nil
	}
	folders, err := c.Library.FolderRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading libraries for a row of collections: %w", err)
	}
	out := make([]int, 0, len(folders))
	for _, folder := range folders {
		if folder.Enabled && viewerCanAccessLibrary(ctx, folder.ID) {
			out = append(out, folder.ID)
		}
	}
	return out, nil
}

// firstHoldingLibrary is the first of libraries, in their order, that holds
// collection.
func firstHoldingLibrary(collection *models.LibraryCollection, libraries []int) (int, bool) {
	for _, id := range libraries {
		if collectionSpansLibrary(collection, id) {
			return id, true
		}
	}
	return 0, false
}

// userCards builds the cards of the personal collections among ids that the
// acting profile can see, keyed by collection id.
func (c *CollectionRowCards) userCards(ctx context.Context, ids []string, scope sections.CollectionCardScope) (map[string]sections.CollectionCard, error) {
	out := map[string]sections.CollectionCard{}
	h := c.Personal
	if len(ids) == 0 || h == nil || h.storeProvider == nil || scope.UserID <= 0 || scope.ProfileID == "" {
		return out, nil
	}
	store, err := h.storeProvider.ForUser(ctx, scope.UserID)
	if err != nil {
		return nil, fmt.Errorf("opening user store for a row of collections: %w", err)
	}
	listed, err := store.ListCollections(ctx, scope.ProfileID)
	if err != nil {
		return nil, fmt.Errorf("listing collections for a row of collections: %w", err)
	}
	picked := make([]userstore.Collection, 0, len(ids))
	for _, collection := range listed {
		if slices.Contains(ids, collection.ID) && collection.VisibleTo(scope.ProfileID) {
			picked = append(picked, collection)
		}
	}
	if len(picked) == 0 {
		return out, nil
	}
	for _, view := range h.collectionViews(WithNativeAPIV2(ctx), store, scope.UserID, scope.ProfileID, picked) {
		creator := view.CreatorProfileID
		out[view.ID] = sections.CollectionCard{
			ID:               view.ID,
			Source:           sections.CollectionCardUser,
			Title:            view.Name,
			PosterURL:        view.PosterURL,
			PosterThumbhash:  view.PosterThumbhash,
			PosterIsCollage:  view.PosterIsCollage,
			ItemCount:        view.ItemCount,
			CreatorProfileID: &creator,
		}
	}
	return out, nil
}
