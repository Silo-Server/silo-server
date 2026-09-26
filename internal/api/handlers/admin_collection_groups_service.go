package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type AdminCollectionGroupCreate = createGroupRequest
type AdminCollectionGroupUpdate = updateGroupRequest
type AdminCollectionGroupView = libraryCollectionGroupResponse
type AdminCollectionGroupsView = listGroupsResponse

func (h *LibraryCollectionGroupHandler) ListAdminCollectionGroups(ctx context.Context, libraryID int) (AdminCollectionGroupsView, error) {
	groups, err := h.groupRepo.ListByLibrary(ctx, libraryID)
	if err != nil {
		return AdminCollectionGroupsView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load groups")
	}
	order, err := h.groupRepo.GetUngroupedSortOrder(ctx, libraryID)
	if err != nil {
		// Preserve the existing display fallback. Canonical order reads must
		// instead fail if they cannot read the complete ordering state.
		order = 9999
	}
	return AdminCollectionGroupsView{Groups: toLibraryCollectionGroupResponses(groups), UngroupedSortOrder: order}, nil
}

func (h *LibraryCollectionGroupHandler) CreateAdminCollectionGroup(ctx context.Context, libraryID int, req AdminCollectionGroupCreate) (AdminCollectionGroupView, error) {
	if req.Name == "" {
		return AdminCollectionGroupView{}, apiError(http.StatusBadRequest, "bad_request", "name required")
	}
	in := catalog.CreateLibraryCollectionGroupInput{LibraryID: libraryID, Name: req.Name, Kind: models.GroupKindRegular}
	if req.Slug != nil {
		in.Slug = *req.Slug
	}
	if req.DefaultSortMode != nil {
		in.DefaultSortMode = models.GroupSortMode(*req.DefaultSortMode)
	}
	g, err := h.groupRepo.Create(ctx, in)
	if err != nil {
		return AdminCollectionGroupView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to create group")
	}
	return toLibraryCollectionGroupResponse(*g), nil
}

func (h *LibraryCollectionGroupHandler) UpdateAdminCollectionGroup(ctx context.Context, id string, req AdminCollectionGroupUpdate) (AdminCollectionGroupView, error) {
	if id == "" {
		return AdminCollectionGroupView{}, apiError(http.StatusBadRequest, "bad_request", "id required")
	}
	in := catalog.UpdateLibraryCollectionGroupInput{Name: req.Name, Slug: req.Slug, ExpectedRevision: adminCollectionExpectedRevision(ctx)}
	if req.DefaultSortMode != nil {
		in.DefaultSortMode = new(models.GroupSortMode(*req.DefaultSortMode))
	}
	g, err := h.groupRepo.Update(ctx, id, in)
	if errors.Is(err, catalog.ErrLibraryCollectionRevisionMismatch) {
		return AdminCollectionGroupView{}, err
	}
	if errors.Is(err, catalog.ErrLibraryCollectionGroupNotFound) {
		return AdminCollectionGroupView{}, apiError(http.StatusNotFound, "not_found", "Group not found")
	}
	if err != nil {
		return AdminCollectionGroupView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to update group")
	}
	return toLibraryCollectionGroupResponse(*g), nil
}

func (h *LibraryCollectionGroupHandler) DeleteAdminCollectionGroup(ctx context.Context, id string) error {
	if id == "" {
		return apiError(http.StatusBadRequest, "bad_request", "id required")
	}
	var err error
	if rev := adminCollectionExpectedRevision(ctx); rev != nil {
		err = h.groupRepo.DeleteIfRevision(ctx, id, *rev)
	} else {
		err = h.groupRepo.Delete(ctx, id)
	}
	if errors.Is(err, catalog.ErrLibraryCollectionRevisionMismatch) {
		return err
	}
	if errors.Is(err, catalog.ErrLibraryCollectionGroupNotFound) {
		return apiError(http.StatusNotFound, "not_found", "Group not found")
	}
	if err != nil {
		return apiError(http.StatusBadRequest, "bad_request", err.Error())
	}
	return nil
}
