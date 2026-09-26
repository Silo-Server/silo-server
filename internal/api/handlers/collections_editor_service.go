package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type collectionExpectedRevisionKey struct{}

// WithCollectionExpectedRevision carries the already evaluated transport
// precondition into the store transaction. A negative revision is explicit overwrite.
func WithCollectionExpectedRevision(ctx context.Context, revision int64) context.Context {
	return context.WithValue(ctx, collectionExpectedRevisionKey{}, revision)
}
func collectionExpectedRevision(ctx context.Context) *int64 {
	v, ok := ctx.Value(collectionExpectedRevisionKey{}).(int64)
	if !ok {
		return nil
	}
	return &v
}

type PersonalCollectionEditorView struct {
	Collection PersonalCollectionView
	Revision   int64
}
type PersonalCollectionOrderView struct {
	GroupID    *string
	OrderedIDs []string
	Revision   int64
	HasMore    bool
}
type PersonalCollectionGroupEditorView struct {
	Group    CollectionGroupView
	Revision int64
}

func (h *CollectionHandler) PersonalCollectionEditor(ctx context.Context, userID int, profileID, id string) (PersonalCollectionEditorView, error) {
	var out PersonalCollectionEditorView
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return out, apiError(500, "internal_error", "Failed to access user store")
	}
	pager, ok := store.(userstore.CollectionItemsPager)
	if !ok {
		return out, apiError(503, "unavailable", "Collection versioning is unavailable")
	}
	rev, err := pager.CollectionRevision(ctx, id)
	if err != nil {
		return out, collectionPageError(err)
	}
	view, err := h.GetPersonalCollection(ctx, userID, profileID, id)
	if err != nil {
		return out, err
	}
	after, err := pager.CollectionRevision(ctx, id)
	if err != nil {
		return out, collectionPageError(err)
	}
	if after != rev {
		return out, collectionPageError(userstore.ErrCollectionChanged)
	}
	// This is the canonical editor representation. Presigned URLs are displayed
	// from the collection listing and never participate in a strong validator.
	view.PosterURL = ""
	out.Collection = view
	out.Revision = rev
	return out, nil
}

func (h *CollectionHandler) PersonalCollectionOrderEditor(ctx context.Context, userID int, profileID string, groupID *string) (PersonalCollectionOrderView, error) {
	out := PersonalCollectionOrderView{GroupID: groupID, OrderedIDs: []string{}}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return out, apiError(500, "internal_error", "Failed to access user store")
	}
	versioner, ok := store.(interface {
		CollectionOrderRevision(context.Context) (int64, error)
	})
	if !ok {
		return out, apiError(503, "unavailable", "Collection ordering is unavailable")
	}
	rev, err := versioner.CollectionOrderRevision(ctx)
	if err != nil {
		return out, err
	}
	view, err := h.ListPersonalCollections(ctx, userID, profileID)
	if err != nil {
		return out, err
	}
	for _, c := range view.Collections {
		same := c.GroupID == nil && groupID == nil
		if c.GroupID != nil && groupID != nil {
			same = *c.GroupID == *groupID
		}
		if same {
			out.OrderedIDs = append(out.OrderedIDs, c.ID)
		}
	}
	after, err := versioner.CollectionOrderRevision(ctx)
	if err != nil {
		return out, err
	}
	if rev != after {
		return out, collectionPageError(userstore.ErrCollectionChanged)
	}
	out.Revision = rev
	return out, nil
}
func (h *CollectionHandler) PersonalCollectionGroupsEditor(ctx context.Context, userID int) ([]CollectionGroupView, int64, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, 0, apiError(500, "internal_error", "Failed to access user store")
	}
	if err := collectionFeatureError(store, "groups"); err != nil {
		return nil, 0, err
	}
	versioner, ok := store.(interface {
		CollectionOrderRevision(context.Context) (int64, error)
	})
	if !ok {
		return nil, 0, apiError(503, "unavailable", "Collection ordering is unavailable")
	}
	rev, err := versioner.CollectionOrderRevision(ctx)
	if err != nil {
		return nil, 0, err
	}
	groups, err := store.ListCollectionGroups(ctx)
	if err != nil {
		return nil, 0, err
	}
	after, err := versioner.CollectionOrderRevision(ctx)
	if err != nil {
		return nil, 0, err
	}
	if rev != after {
		return nil, 0, collectionPageError(userstore.ErrCollectionChanged)
	}
	out := make([]CollectionGroupView, 0, len(groups))
	for _, g := range groups {
		out = append(out, collectionGroupView(g))
	}
	return out, rev, nil
}
func (h *CollectionHandler) PersonalCollectionGroupEditor(ctx context.Context, userID int, id string) (PersonalCollectionGroupEditorView, error) {
	groups, rev, err := h.PersonalCollectionGroupsEditor(ctx, userID)
	if err != nil {
		return PersonalCollectionGroupEditorView{}, err
	}
	for _, g := range groups {
		if g.ID == id {
			return PersonalCollectionGroupEditorView{Group: g, Revision: rev}, nil
		}
	}
	return PersonalCollectionGroupEditorView{}, apiError(http.StatusNotFound, "not_found", "Collection group not found")
}
func (h *CollectionHandler) PersonalCollectionItemsOrderEditor(ctx context.Context, userID int, profileID, id string) (PersonalCollectionOrderView, error) {
	out := PersonalCollectionOrderView{OrderedIDs: []string{}}
	store, c, err := h.personalCollectionStore(ctx, userID, profileID, id, true)
	if err != nil {
		return out, err
	}
	if c.CollectionType != collectionManagementModeManual {
		return out, apiError(400, "bad_request", "Only manual collections have an editable item order")
	}
	pager, ok := store.(userstore.CollectionItemsPager)
	if !ok {
		return out, apiError(503, "unavailable", "Collection ordering is unavailable")
	}
	page, err := pager.ListCollectionItemsPage(ctx, id, userstore.CollectionItemsPageOptions{Limit: 200})
	if err != nil {
		return out, collectionPageError(err)
	}
	// Authorization is checked again against this page's committed witness.
	if _, _, err = h.personalCollectionStore(ctx, userID, profileID, id, true); err != nil {
		return out, err
	}
	rev, err := pager.CollectionRevision(ctx, id)
	if err != nil {
		return out, collectionPageError(err)
	}
	if rev != page.Revision {
		return out, collectionPageError(userstore.ErrCollectionChanged)
	}
	reader := h.ItemReader
	if reader == nil && h.Executor != nil && h.Executor.Pool != nil {
		reader = catalog.NewItemRepository(h.Executor.Pool)
	}
	if len(page.Items) > 0 {
		if reader == nil {
			return out, apiError(503, "unavailable", "Catalog access is unavailable")
		}
		ids := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			ids = append(ids, item.MediaItemID)
		}
		visible, err := reader.GetByIDsWithAccess(ctx, ids, AccessFilterFromContext(ctx, ""))
		if err != nil {
			return out, apiError(500, "internal_error", "Failed to validate collection items")
		}
		if len(visible) != len(ids) {
			return out, apiError(404, "not_found", "The complete collection order is not accessible")
		}
	}
	after, err := pager.CollectionRevision(ctx, id)
	if err != nil {
		return out, collectionPageError(err)
	}
	if after != page.Revision {
		return out, collectionPageError(userstore.ErrCollectionChanged)
	}
	for _, i := range page.Items {
		out.OrderedIDs = append(out.OrderedIDs, i.MediaItemID)
	}
	out.Revision = rev
	out.HasMore = page.HasMore
	return out, nil
}

func mutationStore(store userstore.UserStore) (userstore.CollectionMutationStore, error) {
	s, ok := store.(userstore.CollectionMutationStore)
	if !ok {
		return nil, apiError(503, "unavailable", "Collection concurrency is unavailable")
	}
	return s, nil
}
func deleteCollectionWithRevision(ctx context.Context, s userstore.UserStore, id string) error {
	if expected := collectionExpectedRevision(ctx); expected != nil {
		cas, e := mutationStore(s)
		if e != nil {
			return e
		}
		return cas.DeleteCollectionIfRevision(ctx, id, *expected)
	}
	return s.DeleteCollection(ctx, id)
}
func reorderCollectionItemsWithRevision(ctx context.Context, s userstore.UserStore, id string, ids []string) error {
	if expected := collectionExpectedRevision(ctx); expected != nil {
		cas, e := mutationStore(s)
		if e != nil {
			return e
		}
		return cas.ReorderCollectionItemsIfRevision(ctx, id, ids, *expected)
	}
	return s.ReorderCollectionItems(ctx, id, ids)
}
func reorderCollectionsWithRevision(ctx context.Context, s userstore.UserStore, profile string, group *string, ids []string) error {
	if expected := collectionExpectedRevision(ctx); expected != nil {
		cas, e := mutationStore(s)
		if e != nil {
			return e
		}
		return cas.ReorderCollectionsIfRevision(ctx, profile, group, ids, *expected)
	}
	return s.ReorderCollections(ctx, profile, group, ids)
}
func updateCollectionGroupWithRevision(ctx context.Context, s userstore.UserStore, id string, name, slug *string, mode *userstore.GroupSortMode) (*userstore.CollectionGroup, error) {
	if expected := collectionExpectedRevision(ctx); expected != nil {
		cas, e := mutationStore(s)
		if e != nil {
			return nil, e
		}
		return cas.UpdateCollectionGroupIfRevision(ctx, id, name, slug, mode, *expected)
	}
	return s.UpdateCollectionGroup(ctx, id, name, slug, mode)
}
func deleteCollectionGroupWithRevision(ctx context.Context, s userstore.UserStore, id string) error {
	if expected := collectionExpectedRevision(ctx); expected != nil {
		cas, e := mutationStore(s)
		if e != nil {
			return e
		}
		return cas.DeleteCollectionGroupIfRevision(ctx, id, *expected)
	}
	return s.DeleteCollectionGroup(ctx, id)
}
func reorderCollectionGroupsWithRevision(ctx context.Context, s userstore.UserStore, ids []string) error {
	if expected := collectionExpectedRevision(ctx); expected != nil {
		cas, e := mutationStore(s)
		if e != nil {
			return e
		}
		return cas.ReorderCollectionGroupsIfRevision(ctx, ids, *expected)
	}
	return s.ReorderCollectionGroups(ctx, ids)
}
