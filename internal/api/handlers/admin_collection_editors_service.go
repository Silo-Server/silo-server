package handlers

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collections/templates"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const adminCollectionUngrouped = "ungrouped"

type AdminCollectionOrderView struct {
	LibraryID  int
	GroupID    *string
	OrderedIDs []string
	HasMore    bool
	Revision   int64
}

func (h *LibraryCollectionGroupHandler) GetAdminCollectionGroup(ctx context.Context, id string) (AdminCollectionGroupView, int64, error) {
	g, err := h.groupRepo.GetByID(ctx, id)
	if err != nil {
		return AdminCollectionGroupView{}, 0, err
	}
	rev, err := h.groupRepo.CollectionOrderRevision(ctx, g.LibraryID)
	if err != nil {
		return AdminCollectionGroupView{}, 0, err
	}
	g, err = h.groupRepo.GetByID(ctx, id)
	if err != nil {
		return AdminCollectionGroupView{}, 0, err
	}
	after, err := h.groupRepo.CollectionOrderRevision(ctx, g.LibraryID)
	if err != nil {
		return AdminCollectionGroupView{}, 0, err
	}
	if rev != after {
		return AdminCollectionGroupView{}, 0, catalog.ErrLibraryCollectionRevisionMismatch
	}
	return toLibraryCollectionGroupResponse(*g), rev, nil
}

func (h *LibraryCollectionGroupHandler) AdminCollectionGroupOrder(ctx context.Context, libraryID int) (AdminCollectionOrderView, error) {
	out := AdminCollectionOrderView{LibraryID: libraryID, OrderedIDs: []string{}}
	rev, err := h.groupRepo.CollectionOrderRevision(ctx, libraryID)
	if err != nil {
		return out, err
	}
	groups, err := h.groupRepo.ListByLibrary(ctx, libraryID)
	if err != nil {
		return out, err
	}
	ungrouped, err := h.groupRepo.GetUngroupedSortOrder(ctx, libraryID)
	if err != nil {
		return out, err
	}
	type positionedID struct {
		id       string
		position int
	}
	entries := make([]positionedID, 0, len(groups)+1)
	for _, g := range groups {
		entries = append(entries, positionedID{g.ID, g.SortOrder})
	}
	entries = append(entries, positionedID{adminCollectionUngrouped, ungrouped})
	slices.SortFunc(entries, func(a, b positionedID) int {
		return cmp.Or(cmp.Compare(a.position, b.position), cmp.Compare(a.id, b.id))
	})
	for _, entry := range entries {
		out.OrderedIDs = append(out.OrderedIDs, entry.id)
	}
	after, err := h.groupRepo.CollectionOrderRevision(ctx, libraryID)
	if err != nil {
		return out, err
	}
	if after != rev {
		return out, catalog.ErrLibraryCollectionRevisionMismatch
	}
	out.Revision = rev
	return out, nil
}

func (h *LibraryCollectionGroupHandler) ReorderAdminCollectionGroups(ctx context.Context, libraryID int, ids []string) error {
	if rev := adminCollectionExpectedRevision(ctx); rev != nil {
		return h.groupRepo.ReorderIfRevision(ctx, libraryID, ids, *rev)
	}
	return h.groupRepo.Reorder(ctx, libraryID, ids)
}

func adminCollectionOrder(ctx context.Context, repo *catalog.LibraryCollectionRepository, libraryID int, groupID *string) (AdminCollectionOrderView, error) {
	out := AdminCollectionOrderView{LibraryID: libraryID, GroupID: groupID, OrderedIDs: []string{}}
	rev, err := repo.CollectionOrderRevision(ctx, libraryID)
	if err != nil {
		return out, err
	}
	// Admin ordering includes hidden rows. ListByGroup is viewer-filtered.
	collections, err := repo.ListAll(ctx, &libraryID, catalog.ListLibraryCollectionsOptions{IncludeHidden: true})
	if err != nil {
		return out, err
	}
	slices.SortFunc(collections, func(a, b *models.LibraryCollection) int {
		return cmp.Or(cmp.Compare(a.SortOrder, b.SortOrder), cmp.Compare(a.ID, b.ID))
	})
	for _, c := range collections {
		same := c.GroupID == nil && groupID == nil
		if c.GroupID != nil && groupID != nil {
			same = *c.GroupID == *groupID
		}
		if same {
			out.OrderedIDs = append(out.OrderedIDs, c.ID)
		}
	}
	after, err := repo.CollectionOrderRevision(ctx, libraryID)
	if err != nil {
		return out, err
	}
	if after != rev {
		return out, catalog.ErrLibraryCollectionRevisionMismatch
	}
	out.Revision = rev
	return out, nil
}

func (h *LibraryCollectionHandler) AdminCollectionOrder(ctx context.Context, libraryID int, groupID *string) (AdminCollectionOrderView, error) {
	if groupID != nil {
		if h.GroupRepo == nil {
			return AdminCollectionOrderView{}, catalog.ErrLibraryCollectionGroupNotFound
		}
		group, err := h.GroupRepo.GetByID(ctx, *groupID)
		if err != nil {
			return AdminCollectionOrderView{}, err
		}
		if group.LibraryID != libraryID {
			return AdminCollectionOrderView{}, catalog.ErrLibraryCollectionGroupNotFound
		}
	}
	return adminCollectionOrder(ctx, h.repo, libraryID, groupID)
}
func (h *LibraryCollectionHandler) ReorderAdminCollections(ctx context.Context, libraryID int, groupID *string, ids []string) error {
	if rev := adminCollectionExpectedRevision(ctx); rev != nil {
		return h.repo.ReorderCollectionsIfRevision(ctx, libraryID, groupID, ids, *rev)
	}
	return h.repo.ReorderCollections(ctx, libraryID, groupID, ids)
}

func (h *LibraryCollectionGroupHandler) AdminGroupCollectionOrder(ctx context.Context, groupID string, libraryID int) (AdminCollectionOrderView, error) {
	var target *string
	if groupID != adminCollectionUngrouped {
		g, err := h.groupRepo.GetByID(ctx, groupID)
		if err != nil {
			return AdminCollectionOrderView{}, err
		}
		libraryID = g.LibraryID
		target = &g.ID
	}
	if libraryID <= 0 {
		return AdminCollectionOrderView{}, fieldError("library_id", "library_id is required")
	}
	return adminCollectionOrder(ctx, h.collRepo, libraryID, target)
}
func (h *LibraryCollectionGroupHandler) MoveAdminGroupCollections(ctx context.Context, groupID string, libraryID int, ids []string, strict bool) error {
	view, err := h.AdminGroupCollectionOrder(ctx, groupID, libraryID)
	if err != nil {
		return err
	}
	return h.collRepo.MoveAndReorder(ctx, catalog.MoveAndReorderInput{LibraryID: view.LibraryID, TargetGroupID: view.GroupID, OrderedIDs: ids, Strict: strict, ExpectedRevision: adminCollectionExpectedRevision(ctx)})
}

type AdminCollectionItemsPageView struct {
	userstore.CollectionItemsPage
	Titles map[string]string
}

func (h *LibraryCollectionHandler) AdminCollectionItemsPage(ctx context.Context, id string, opts userstore.CollectionItemsPageOptions) (AdminCollectionItemsPageView, error) {
	before, err := h.repo.CollectionRevision(ctx, id)
	if err != nil {
		return AdminCollectionItemsPageView{}, err
	}
	c, err := h.repo.GetByID(ctx, id)
	if err != nil {
		return AdminCollectionItemsPageView{}, err
	}
	if c.CollectionType != collectionManagementModeManual {
		return AdminCollectionItemsPageView{}, apiError(409, "collection_not_manual", "Only manual collections support manual items")
	}
	membership, err := h.repo.ListItemsPage(ctx, id, opts)
	page := AdminCollectionItemsPageView{CollectionItemsPage: membership}
	if err != nil {
		return page, err
	}
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.MediaItemID)
	}
	items, err := h.itemRepo.GetByIDs(ctx, ids)
	if err != nil {
		return page, err
	}
	page.Titles = make(map[string]string, len(items))
	for _, item := range items {
		page.Titles[item.ContentID] = item.Title
	}
	after, err := h.repo.CollectionRevision(ctx, id)
	if err != nil {
		return page, err
	}
	if before != page.Revision || after != page.Revision {
		return page, userstore.ErrCollectionChanged
	}
	return page, nil
}
func (h *LibraryCollectionHandler) AdminCollectionItemsOrder(ctx context.Context, id string) (AdminCollectionOrderView, error) {
	out := AdminCollectionOrderView{OrderedIDs: []string{}}
	page, err := h.AdminCollectionItemsPage(ctx, id, userstore.CollectionItemsPageOptions{Limit: 200})
	if errors.Is(err, userstore.ErrCollectionChanged) {
		return out, catalog.ErrLibraryCollectionRevisionMismatch
	}
	if err != nil {
		return out, err
	}
	for _, item := range page.Items {
		out.OrderedIDs = append(out.OrderedIDs, item.MediaItemID)
	}
	out.Revision = page.Revision
	out.HasMore = page.HasMore
	return out, nil
}

func (h *LibraryCollectionHandler) AdminCollectionFeatures(context.Context) userstore.CollectionFeatures {
	return userstore.CollectionFeatures{Groups: h.GroupRepo != nil, Imports: h.service != nil, Artwork: h.ArtworkStore != nil, ItemReorder: h.repo != nil}
}
func (h *LibraryCollectionHandler) AdminCollectionTemplateCatalog(context.Context) templates.Catalog {
	return h.templateRegistry().Catalog()
}
