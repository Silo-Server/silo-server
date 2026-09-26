package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
)

type AdminGroups struct {
	Items              []AdminCollectionGroup `json:"items"`
	UngroupedSortOrder int                    `json:"ungrouped_sort_order"`
}
type AdminGroupsOutput struct{ Body AdminGroups }
type AdminGroupOutput struct {
	ETag string `header:"ETag"`
	Body AdminCollectionGroup
}
type AdminGroupCreatedOutput struct {
	Location string `header:"Location"`
	Body     AdminCollectionGroup
}
type AdminLibraryGroupsInput struct {
	LibraryID ID `path:"library_id"`
}
type AdminGroupCreateInput struct {
	LibraryID ID `path:"library_id"`
	Body      CollectionGroupCreate
	RawBody   []byte
}
type AdminGroupUpdateInput struct {
	ID          ID     `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        CollectionGroupUpdate
	RawBody     []byte
}
type AdminGroupOrderWriteInput struct {
	LibraryID   ID     `path:"library_id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminCollectionIDsOrder
}
type AdminMoveOrderInput struct {
	GroupID   ID `path:"group_id"`
	LibraryID ID `query:"library_id"`
}
type AdminMoveOrderWriteInput struct {
	GroupID     ID     `path:"group_id"`
	LibraryID   ID     `query:"library_id"`
	MoveOmitted string `query:"move_omitted"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminCollectionIDsOrder
}

func registerAdminCollectionGroups(reg *Registry) {
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/libraries/{library_id}/collection-groups", "listAdminCollectionGroups", "List groups and the synthetic ungrouped position.", false), reg.listAdminCollectionGroups)
	create := adminCollectionOperation(http.MethodPost, "/admin/libraries/{library_id}/collection-groups", "createAdminCollectionGroup", "Create a library collection group.", false)
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, reg.createAdminCollectionGroup)
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/collection-groups/{id}", "getAdminCollectionGroup", "Read a canonical collection group and validator.", false), reg.getAdminCollectionGroup)
	Register(reg, adminCollectionOperation(http.MethodPatch, "/admin/collection-groups/{id}", "updateAdminCollectionGroup", "Update a group using the observed library ordering revision.", true), reg.updateAdminCollectionGroup)
	Register(reg, adminCollectionOperation(http.MethodDelete, "/admin/collection-groups/{id}", "deleteAdminCollectionGroup", "Delete a regular group; its collections become ungrouped.", true), reg.deleteAdminCollectionGroup)
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/libraries/{library_id}/collection-groups/order", "getAdminCollectionGroupOrder", "Read the complete group order including the ungrouped sentinel.", false), reg.getAdminCollectionGroupOrder)
	Register(reg, adminCollectionOperation(http.MethodPut, "/admin/libraries/{library_id}/collection-groups/order", "reorderAdminCollectionGroups", "Replace the library group order including its ungrouped position.", true), reg.reorderAdminCollectionGroups)
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/collection-groups/{group_id}/collections/order", "getAdminGroupCollectionOrder", "Read a destination group before moving or reordering collections.", false), reg.getAdminGroupCollectionOrder)
	Register(reg, adminCollectionOperation(http.MethodPut, "/admin/collection-groups/{group_id}/collections/order", "moveAndReorderAdminGroupCollections", "Move and reorder collections. Omitted destination members move to ungrouped unless strict handling is requested.", true), reg.moveAdminGroupCollections)
}
func idsToStrings(ids []ID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}
func (reg *Registry) listAdminCollectionGroups(ctx context.Context, in *AdminLibraryGroupsInput) (*AdminGroupsOutput, error) {
	if reg.deps.AdminCollectionGroups == nil {
		return nil, unavailable("admin collection groups")
	}
	id, p := libraryID(in.LibraryID)
	if p != nil {
		return nil, p
	}
	v, e := reg.deps.AdminCollectionGroups.ListAdminCollectionGroups(ctx, id)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	out := &AdminGroupsOutput{Body: AdminGroups{Items: make([]AdminCollectionGroup, 0, len(v.Groups)), UngroupedSortOrder: v.UngroupedSortOrder}}
	for _, g := range v.Groups {
		out.Body.Items = append(out.Body.Items, adminGroupOf(g))
	}
	return out, nil
}
func (reg *Registry) createAdminCollectionGroup(ctx context.Context, in *AdminGroupCreateInput) (*AdminGroupCreatedOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	if reg.deps.AdminCollectionGroups == nil {
		return nil, unavailable("admin collection groups")
	}
	id, p := libraryID(in.LibraryID)
	if p != nil {
		return nil, p
	}
	g, e := reg.deps.AdminCollectionGroups.CreateAdminCollectionGroup(ctx, id, handlers.AdminCollectionGroupCreate{Name: in.Body.Name, Slug: in.Body.Slug, DefaultSortMode: in.Body.DefaultSortMode})
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return &AdminGroupCreatedOutput{Location: Prefix + "/admin/collection-groups/" + g.ID, Body: adminGroupOf(g)}, nil
}
func (reg *Registry) getAdminCollectionGroup(ctx context.Context, in *AdminCollectionIDInput) (*AdminGroupOutput, error) {
	if reg.deps.AdminCollectionGroups == nil {
		return nil, unavailable("admin collection groups")
	}
	g, rev, e := reg.deps.AdminCollectionGroups.GetAdminCollectionGroup(ctx, string(in.ID))
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return &AdminGroupOutput{ETag: adminCollectionTag(ctx, "group", string(in.ID), rev).String(), Body: adminGroupOf(g)}, nil
}
func (reg *Registry) adminGroupGuard(ctx context.Context, id, match, none string) (context.Context, *Problem) {
	if reg.deps.AdminCollectionGroups == nil {
		return ctx, unavailable("admin collection groups")
	}
	_, rev, e := reg.deps.AdminCollectionGroups.GetAdminCollectionGroup(ctx, id)
	if e != nil {
		return ctx, adminCollectionError(e)
	}
	return applyAdminCollectionGuard(ctx, "group", id, rev, match, none)
}
func (reg *Registry) adminGroupMutationError(ctx context.Context, id string, err error) error {
	if errors.Is(err, catalogsvc.ErrLibraryCollectionRevisionMismatch) {
		now, e := reg.getAdminCollectionGroup(ctx, &AdminCollectionIDInput{ID: ID(id)})
		if e != nil {
			return e
		}
		tag, _ := ParseEntityTag(now.ETag)
		return StaleVersionProblem(tag)
	}
	return adminCollectionError(err)
}
func (reg *Registry) updateAdminCollectionGroup(ctx context.Context, in *AdminGroupUpdateInput) (*AdminGroupOutput, error) {
	guarded, p := reg.adminGroupGuard(ctx, string(in.ID), in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	_, e := reg.deps.AdminCollectionGroups.UpdateAdminCollectionGroup(guarded, string(in.ID), handlers.AdminCollectionGroupUpdate{Name: in.Body.Name, Slug: in.Body.Slug, DefaultSortMode: in.Body.DefaultSortMode})
	if e != nil {
		return nil, reg.adminGroupMutationError(ctx, string(in.ID), e)
	}
	return reg.getAdminCollectionGroup(ctx, &AdminCollectionIDInput{ID: in.ID})
}
func (reg *Registry) deleteAdminCollectionGroup(ctx context.Context, in *AdminCollectionIDInput) (*struct{}, error) {
	guarded, p := reg.adminGroupGuard(ctx, string(in.ID), in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	if e := reg.deps.AdminCollectionGroups.DeleteAdminCollectionGroup(guarded, string(in.ID)); e != nil {
		return nil, reg.adminGroupMutationError(ctx, string(in.ID), e)
	}
	return nil, nil
}
func (reg *Registry) getAdminCollectionGroupOrder(ctx context.Context, in *AdminLibraryGroupsInput) (*AdminCollectionOrderOutput, error) {
	if reg.deps.AdminCollectionGroups == nil {
		return nil, unavailable("admin collection groups")
	}
	lib, p := libraryID(in.LibraryID)
	if p != nil {
		return nil, p
	}
	v, e := reg.deps.AdminCollectionGroups.AdminCollectionGroupOrder(ctx, lib)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return &AdminCollectionOrderOutput{ETag: adminCollectionTag(ctx, "groups-order", strconv.Itoa(lib), v.Revision).String(), Body: adminOrderOf(v)}, nil
}
func (reg *Registry) reorderAdminCollectionGroups(ctx context.Context, in *AdminGroupOrderWriteInput) (*AdminCollectionOrderOutput, error) {
	if reg.deps.AdminCollectionGroups == nil {
		return nil, unavailable("admin collection groups")
	}
	lib, p := libraryID(in.LibraryID)
	if p != nil {
		return nil, p
	}
	v, e := reg.deps.AdminCollectionGroups.AdminCollectionGroupOrder(ctx, lib)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	guarded, p := applyAdminCollectionGuard(ctx, "groups-order", strconv.Itoa(lib), v.Revision, in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	e = reg.deps.AdminCollectionGroups.ReorderAdminCollectionGroups(guarded, lib, idsToStrings(in.Body.OrderedIDs))
	if errors.Is(e, catalogsvc.ErrLibraryCollectionRevisionMismatch) {
		now, re := reg.getAdminCollectionGroupOrder(ctx, &AdminLibraryGroupsInput{LibraryID: in.LibraryID})
		if re != nil {
			return nil, re
		}
		tag, _ := ParseEntityTag(now.ETag)
		return nil, StaleVersionProblem(tag)
	}
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return reg.getAdminCollectionGroupOrder(ctx, &AdminLibraryGroupsInput{LibraryID: in.LibraryID})
}
func adminMoveLibrary(id ID) (int, *Problem) {
	if id == "" {
		return 0, nil
	}
	return libraryID(id)
}
func (reg *Registry) getAdminGroupCollectionOrder(ctx context.Context, in *AdminMoveOrderInput) (*AdminCollectionOrderOutput, error) {
	if reg.deps.AdminCollectionGroups == nil {
		return nil, unavailable("admin collection groups")
	}
	lib, p := adminMoveLibrary(in.LibraryID)
	if p != nil {
		return nil, p
	}
	v, e := reg.deps.AdminCollectionGroups.AdminGroupCollectionOrder(ctx, string(in.GroupID), lib)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return &AdminCollectionOrderOutput{ETag: adminCollectionTag(ctx, "move-order", strconv.Itoa(v.LibraryID)+":"+string(in.GroupID), v.Revision).String(), Body: adminOrderOf(v)}, nil
}
func (reg *Registry) moveAdminGroupCollections(ctx context.Context, in *AdminMoveOrderWriteInput) (*AdminCollectionOrderOutput, error) {
	if reg.deps.AdminCollectionGroups == nil {
		return nil, unavailable("admin collection groups")
	}
	lib, p := adminMoveLibrary(in.LibraryID)
	if p != nil {
		return nil, p
	}
	v, e := reg.deps.AdminCollectionGroups.AdminGroupCollectionOrder(ctx, string(in.GroupID), lib)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	guarded, p := applyAdminCollectionGuard(ctx, "move-order", strconv.Itoa(v.LibraryID)+":"+string(in.GroupID), v.Revision, in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	e = reg.deps.AdminCollectionGroups.MoveAdminGroupCollections(guarded, string(in.GroupID), v.LibraryID, idsToStrings(in.Body.OrderedIDs), in.MoveOmitted != "" && in.MoveOmitted != "ungrouped")
	read := &AdminMoveOrderInput{GroupID: in.GroupID, LibraryID: in.LibraryID}
	if errors.Is(e, catalogsvc.ErrLibraryCollectionRevisionMismatch) {
		now, re := reg.getAdminGroupCollectionOrder(ctx, read)
		if re != nil {
			return nil, re
		}
		tag, _ := ParseEntityTag(now.ETag)
		return nil, StaleVersionProblem(tag)
	}
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return reg.getAdminGroupCollectionOrder(ctx, read)
}
