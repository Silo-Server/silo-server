package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const opCreateAdminCollection = "createAdminCollection"

type AdminCollectionService interface {
	ListAdminCollections(context.Context, *int) (handlers.AdminCollectionsList, error)
	GetAdminCollection(context.Context, string) (handlers.AdminCollection, int64, error)
	CreateAdminCollection(context.Context, handlers.AdminCollectionCreate) (handlers.AdminCollection, error)
	UpdateAdminCollection(context.Context, string, handlers.AdminCollectionUpdate) (handlers.AdminCollection, error)
	DeleteAdminCollection(context.Context, string) error
	AdminCollectionOrder(context.Context, int, *string) (handlers.AdminCollectionOrderView, error)
	ReorderAdminCollections(context.Context, int, *string, []string) error
	AdminCollectionItemsOrder(context.Context, string) (handlers.AdminCollectionOrderView, error)
	AdminCollectionItemsPage(context.Context, string, userstore.CollectionItemsPageOptions) (handlers.AdminCollectionItemsPageView, error)
	ReorderAdminCollectionItems(context.Context, string, []string) error
	AddAdminCollectionItem(context.Context, string, string, int) error
	RemoveAdminCollectionItem(context.Context, string, string) error
}
type AdminCollectionGroupService interface {
	ListAdminCollectionGroups(context.Context, int) (handlers.AdminCollectionGroupsView, error)
	CreateAdminCollectionGroup(context.Context, int, handlers.AdminCollectionGroupCreate) (handlers.AdminCollectionGroupView, error)
	GetAdminCollectionGroup(context.Context, string) (handlers.AdminCollectionGroupView, int64, error)
	UpdateAdminCollectionGroup(context.Context, string, handlers.AdminCollectionGroupUpdate) (handlers.AdminCollectionGroupView, error)
	DeleteAdminCollectionGroup(context.Context, string) error
	AdminCollectionGroupOrder(context.Context, int) (handlers.AdminCollectionOrderView, error)
	ReorderAdminCollectionGroups(context.Context, int, []string) error
	AdminGroupCollectionOrder(context.Context, string, int) (handlers.AdminCollectionOrderView, error)
	MoveAdminGroupCollections(context.Context, string, int, []string, bool) error
}
type AdminCollectionGroup struct {
	ID              ID     `json:"id"`
	LibraryID       ID     `json:"library_id"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	Kind            string `json:"kind"`
	DefaultSortMode string `json:"default_sort_mode"`
	SortOrder       int    `json:"sort_order"`
}
type AdminCollectionList struct {
	Collection[AdminCollection]
	Groups []AdminCollectionGroup `json:"groups"`
}
type AdminCollectionListOutput struct{ Body AdminCollectionList }
type AdminCollectionOutput struct {
	ETag string `header:"ETag"`
	Body AdminCollection
}
type AdminCollectionCreatedOutput struct {
	Location string `header:"Location"`
	Body     AdminCollection
}
type AdminCollectionListInput struct {
	LibraryID ID `query:"library_id"`
}
type AdminCollectionIDInput struct {
	ID          ID     `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminCollectionCreateInput struct {
	Body    AdminCollectionCreate
	RawBody []byte
}
type AdminCollectionUpdateInput struct {
	ID          ID     `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminCollectionUpdate
	RawBody     []byte
}
type AdminCollectionOrder struct {
	LibraryID  ID   `json:"library_id,omitempty"`
	GroupID    *ID  `json:"group_id" nullable:"true"`
	OrderedIDs []ID `json:"ordered_ids" uniqueItems:"true"`
	HasMore    bool `json:"has_more"`
}
type AdminCollectionOrderOutput struct {
	ETag string `header:"ETag"`
	Body AdminCollectionOrder
}
type AdminCollectionOrderBody struct {
	LibraryID  ID   `json:"library_id"`
	GroupID    *ID  `json:"group_id,omitempty" nullable:"true"`
	OrderedIDs []ID `json:"ordered_ids" uniqueItems:"true"`
}
type AdminCollectionOrderInput struct {
	LibraryID ID `query:"library_id" required:"true"`
	GroupID   ID `query:"group_id"`
}
type AdminCollectionOrderWriteInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminCollectionOrderBody
}
type AdminCollectionIDsOrder struct {
	OrderedIDs []ID `json:"ordered_ids" uniqueItems:"true" doc:"Every entry in the target order, exactly once."`
}

type AdminCollectionItemsOrderInput struct {
	ID          ID     `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminCollectionIDsOrder
}
type AdminCollectionCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminCollectionCapabilityOutputBody
}

type AdminCollectionCapabilityOutputBody struct {
	Capability
	Groups      bool `json:"groups"`
	Imports     bool `json:"imports"`
	Artwork     bool `json:"artwork"`
	ItemReorder bool `json:"item_reorder"`
}

func adminCollectionOperation(method, path, id, summary string, guarded bool) Operation {
	op := Operation{Operation: humaOp(method, Prefix+path, id, "admin-collections", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: isMutatingMethod(method), Guarded: guarded}
	if method != http.MethodGet {
		op.RetrySafety = RetrySafetyNonRetryable
	}
	if method == http.MethodDelete {
		op.DefaultStatus = http.StatusNoContent
	}
	if method == http.MethodPost && id == opCreateAdminCollection {
		op.DefaultStatus = http.StatusCreated
	}
	return op
}
func registerAdminCollections(reg *Registry) {
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/collections", "listAdminCollections", "List administrator collections, including hidden entries.", false), reg.listAdminCollections)
	Register(reg, adminCollectionOperation(http.MethodPost, "/admin/collections", opCreateAdminCollection, "Create a library collection. Artwork is uploaded separately.", false), reg.createAdminCollection)
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/collections/{id}", "getAdminCollection", "Read the canonical editor and strong validator.", false), reg.getAdminCollection)
	Register(reg, adminCollectionOperation(http.MethodPatch, "/admin/collections/{id}", "updateAdminCollection", "Update a library collection using its observed validator.", true), reg.updateAdminCollection)
	Register(reg, adminCollectionOperation(http.MethodDelete, "/admin/collections/{id}", "deleteAdminCollection", "Delete an unused collection using its observed validator.", true), reg.deleteAdminCollection)
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/collections/order", "getAdminCollectionOrder", "Read complete manual ordering for one library and group.", false), reg.getAdminCollectionOrder)
	Register(reg, adminCollectionOperation(http.MethodPut, "/admin/collections/order", "reorderAdminCollections", "Replace collection order within a library group.", true), reg.reorderAdminCollections)
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/collections/{id}/items/order", "getAdminCollectionItemsOrder", "Read at most 200 manual memberships. A partial window cannot be reordered.", false), reg.getAdminCollectionItemsOrder)
	Register(reg, adminCollectionOperation(http.MethodPut, "/admin/collections/{id}/items/order", "reorderAdminCollectionItems", "Replace the complete manual membership order.", true), reg.reorderAdminCollectionItems)
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/collections/capabilities", "getAdminCollectionCapabilities", "Read configured administrator collection capabilities.", false), func(ctx context.Context, _ *CapabilityInput) (*AdminCollectionCapabilityOutput, error) {
		svc, ok := reg.deps.AdminCollections.(interface {
			AdminCollectionFeatures(context.Context) userstore.CollectionFeatures
		})
		if !ok {
			return &AdminCollectionCapabilityOutput{Body: AdminCollectionCapabilityOutputBody{Capability: Capability{State: StateNotConfigured}}}, nil
		}
		v := svc.AdminCollectionFeatures(ctx)
		out := &AdminCollectionCapabilityOutput{}
		out.Body.Groups = v.Groups
		out.Body.Imports = v.Imports
		out.Body.Artwork = v.Artwork
		out.Body.ItemReorder = v.ItemReorder
		return out, nil
	})
	registerAdminCollectionGroups(reg)
	registerAdminCollectionExtras(reg)
}
func adminGroupOf(g handlers.AdminCollectionGroupView) AdminCollectionGroup {
	return AdminCollectionGroup{ID: ID(g.ID), LibraryID: IDFromInt(int64(g.LibraryID)), Name: g.Name, Slug: g.Slug, Kind: g.Kind, DefaultSortMode: g.DefaultSortMode, SortOrder: g.SortOrder}
}
func adminOrderOf(v handlers.AdminCollectionOrderView) AdminCollectionOrder {
	out := AdminCollectionOrder{OrderedIDs: make([]ID, 0, len(v.OrderedIDs)), HasMore: v.HasMore}
	if v.LibraryID > 0 {
		out.LibraryID = IDFromInt(int64(v.LibraryID))
	}
	if v.GroupID != nil {
		out.GroupID = new(ID(*v.GroupID))
	}
	for _, id := range v.OrderedIDs {
		out.OrderedIDs = append(out.OrderedIDs, ID(id))
	}
	return out
}
func adminCollectionTag(ctx context.Context, kind, id string, revision int64) EntityTag {
	return collectionEditorTag(ctx, "admin-"+kind, id, revision)
}
func adminCollectionError(err error) *Problem {
	if _, ok := errors.AsType[*catalogsvc.StrictReorderError](err); ok {
		return NewProblem(TypeValidationFailed, "ordered_ids must include every collection in the target group.")
	}
	switch {
	case errors.Is(err, catalogsvc.ErrLibraryCollectionSyncModeUnsupported):
		return NewProblem(TypeValidationFailed, "This collection has no import source to synchronize.")
	case errors.Is(err, catalogsvc.ErrLibraryCollectionNotManual):
		return NewProblem(TypeConflict, "Only manual collections support manual items.")
	case errors.Is(err, catalogsvc.ErrLibraryCollectionItemNotFound):
		return NewProblem(TypeNotFound, "Item not found in collection libraries.")
	case errors.Is(err, collectionutil.ErrOrderedIDsMismatch):
		return NewProblem(TypeValidationFailed, "ordered_ids must include every current entry exactly once.")
	case errors.Is(err, catalogsvc.ErrLibraryCollectionRevisionMismatch):
		return NewProblem(TypeConflict, "The collection changed during the read; fetch its current editor again.")
	case errors.Is(err, catalogsvc.ErrLibraryCollectionNotFound), errors.Is(err, catalogsvc.ErrLibraryCollectionGroupNotFound):
		return NewProblem(TypeNotFound, "Collection or group not found.")
	case errors.Is(err, catalogsvc.ErrLibraryCollectionInUse):
		return NewProblem(TypeConflict, "The collection is used by a section.")
	case errors.Is(err, userstore.ErrCollectionChanged):
		return NewProblem(TypeInvalidCursor, "The collection changed; restart from its first page.")
	}
	return collectionProblem(err)
}
func applyAdminCollectionGuard(ctx context.Context, kind, id string, rev int64, match, none string) (context.Context, *Problem) {
	tag := adminCollectionTag(ctx, kind, id, rev)
	if p := EvaluateGuardedPreconditions(match, none, tag); p != nil {
		return ctx, p
	}
	if match == "*" {
		rev = -1
	}
	return handlers.WithAdminCollectionExpectedRevision(ctx, rev), nil
}
func (reg *Registry) listAdminCollections(ctx context.Context, in *AdminCollectionListInput) (*AdminCollectionListOutput, error) {
	if reg.deps.AdminCollections == nil {
		return nil, unavailable("admin collections")
	}
	var library *int
	if in.LibraryID != "" {
		id, p := libraryID(in.LibraryID)
		if p != nil {
			return nil, p
		}
		library = &id
	}
	v, e := reg.deps.AdminCollections.ListAdminCollections(ctx, library)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	items := make([]AdminCollection, 0, len(v.Collections))
	groups := make([]AdminCollectionGroup, 0, len(v.Groups))
	for _, c := range v.Collections {
		items = append(items, adminCollectionOf(c))
	}
	for _, g := range v.Groups {
		groups = append(groups, adminGroupOf(g))
	}
	return &AdminCollectionListOutput{Body: AdminCollectionList{Collection: NewCollection(items), Groups: groups}}, nil
}
func (reg *Registry) getAdminCollection(ctx context.Context, in *AdminCollectionIDInput) (*AdminCollectionOutput, error) {
	if reg.deps.AdminCollections == nil {
		return nil, unavailable("admin collections")
	}
	v, rev, e := reg.deps.AdminCollections.GetAdminCollection(ctx, string(in.ID))
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return &AdminCollectionOutput{ETag: adminCollectionTag(ctx, "collection", string(in.ID), rev).String(), Body: adminCollectionOf(v)}, nil
}
func (reg *Registry) createAdminCollection(ctx context.Context, in *AdminCollectionCreateInput) (*AdminCollectionCreatedOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, map[string]bool{adminCollectionGroupField: true}); p != nil {
		return nil, p
	}
	if reg.deps.AdminCollections == nil {
		return nil, unavailable("admin collections")
	}
	cmd, p := in.Body.command()
	if p != nil {
		return nil, p
	}
	v, e := reg.deps.AdminCollections.CreateAdminCollection(ctx, cmd)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return &AdminCollectionCreatedOutput{Location: Prefix + "/admin/collections/" + v.ID, Body: adminCollectionOf(v)}, nil
}
func (reg *Registry) adminCollectionGuard(ctx context.Context, id, match, none string) (context.Context, *Problem) {
	if reg.deps.AdminCollections == nil {
		return ctx, unavailable("admin collections")
	}
	_, rev, e := reg.deps.AdminCollections.GetAdminCollection(ctx, id)
	if e != nil {
		return ctx, adminCollectionError(e)
	}
	return applyAdminCollectionGuard(ctx, "collection", id, rev, match, none)
}
func (reg *Registry) adminCollectionMutationError(ctx context.Context, id string, err error) *Problem {
	if errors.Is(err, catalogsvc.ErrLibraryCollectionRevisionMismatch) {
		_, rev, e := reg.deps.AdminCollections.GetAdminCollection(ctx, id)
		if e != nil {
			return adminCollectionError(e)
		}
		return StaleVersionProblem(adminCollectionTag(ctx, "collection", id, rev))
	}
	return adminCollectionError(err)
}
func (reg *Registry) updateAdminCollection(ctx context.Context, in *AdminCollectionUpdateInput) (*AdminCollectionOutput, error) {
	guarded, p := reg.adminCollectionGuard(ctx, string(in.ID), in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	cmd, p := in.Body.command(in.RawBody)
	if p != nil {
		return nil, p
	}
	if _, e := reg.deps.AdminCollections.UpdateAdminCollection(guarded, string(in.ID), cmd); e != nil {
		return nil, reg.adminCollectionMutationError(ctx, string(in.ID), e)
	}
	return reg.getAdminCollection(ctx, &AdminCollectionIDInput{ID: in.ID})
}
func (reg *Registry) deleteAdminCollection(ctx context.Context, in *AdminCollectionIDInput) (*struct{}, error) {
	guarded, p := reg.adminCollectionGuard(ctx, string(in.ID), in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	if e := reg.deps.AdminCollections.DeleteAdminCollection(guarded, string(in.ID)); e != nil {
		return nil, reg.adminCollectionMutationError(ctx, string(in.ID), e)
	}
	return nil, nil
}
func (reg *Registry) getAdminCollectionOrder(ctx context.Context, in *AdminCollectionOrderInput) (*AdminCollectionOrderOutput, error) {
	if reg.deps.AdminCollections == nil {
		return nil, unavailable("admin collections")
	}
	lib, p := libraryID(in.LibraryID)
	if p != nil {
		return nil, p
	}
	var group *string
	if in.GroupID != "" {
		group = new(string(in.GroupID))
	}
	v, e := reg.deps.AdminCollections.AdminCollectionOrder(ctx, lib, group)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return &AdminCollectionOrderOutput{ETag: adminCollectionTag(ctx, "order", strconv.Itoa(lib)+":"+string(in.GroupID), v.Revision).String(), Body: adminOrderOf(v)}, nil
}
func (reg *Registry) reorderAdminCollections(ctx context.Context, in *AdminCollectionOrderWriteInput) (*AdminCollectionOrderOutput, error) {
	read := &AdminCollectionOrderInput{LibraryID: in.Body.LibraryID}
	if in.Body.GroupID != nil {
		read.GroupID = *in.Body.GroupID
	}
	if reg.deps.AdminCollections == nil {
		return nil, unavailable("admin collections")
	}
	lib, p := libraryID(in.Body.LibraryID)
	if p != nil {
		return nil, p
	}
	var group *string
	if in.Body.GroupID != nil {
		group = new(string(*in.Body.GroupID))
	}
	v, e := reg.deps.AdminCollections.AdminCollectionOrder(ctx, lib, group)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	guarded, p := applyAdminCollectionGuard(ctx, "order", strconv.Itoa(lib)+":"+string(read.GroupID), v.Revision, in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	e = reg.deps.AdminCollections.ReorderAdminCollections(guarded, lib, group, idsToStrings(in.Body.OrderedIDs))
	if errors.Is(e, catalogsvc.ErrLibraryCollectionRevisionMismatch) {
		now, readErr := reg.getAdminCollectionOrder(ctx, read)
		if readErr != nil {
			return nil, readErr
		}
		tag, _ := ParseEntityTag(now.ETag)
		return nil, StaleVersionProblem(tag)
	}
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return reg.getAdminCollectionOrder(ctx, read)
}
func (reg *Registry) getAdminCollectionItemsOrder(ctx context.Context, in *AdminCollectionIDInput) (*AdminCollectionOrderOutput, error) {
	if reg.deps.AdminCollections == nil {
		return nil, unavailable("admin collections")
	}
	v, e := reg.deps.AdminCollections.AdminCollectionItemsOrder(ctx, string(in.ID))
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return &AdminCollectionOrderOutput{ETag: adminCollectionTag(ctx, "items-order", string(in.ID), v.Revision).String(), Body: adminOrderOf(v)}, nil
}
func (reg *Registry) reorderAdminCollectionItems(ctx context.Context, in *AdminCollectionItemsOrderInput) (*AdminCollectionOrderOutput, error) {
	if reg.deps.AdminCollections == nil {
		return nil, unavailable("admin collections")
	}
	v, e := reg.deps.AdminCollections.AdminCollectionItemsOrder(ctx, string(in.ID))
	if e != nil {
		return nil, adminCollectionError(e)
	}
	guarded, p := applyAdminCollectionGuard(ctx, "items-order", string(in.ID), v.Revision, in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	e = reg.deps.AdminCollections.ReorderAdminCollectionItems(guarded, string(in.ID), idsToStrings(in.Body.OrderedIDs))
	if errors.Is(e, catalogsvc.ErrLibraryCollectionRevisionMismatch) {
		now, readErr := reg.getAdminCollectionItemsOrder(ctx, &AdminCollectionIDInput{ID: in.ID})
		if readErr != nil {
			return nil, readErr
		}
		tag, _ := ParseEntityTag(now.ETag)
		return nil, StaleVersionProblem(tag)
	}
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return reg.getAdminCollectionItemsOrder(ctx, &AdminCollectionIDInput{ID: in.ID})
}

func (c AdminCollectionCapabilityOutputBody) capabilityState() string { return StateAvailable }
