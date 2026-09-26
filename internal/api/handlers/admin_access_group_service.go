package handlers

import (
	"context"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
)

var ErrInvalidAccessGroup = errors.New("invalid access group configuration")
var ErrAccessGroupUnavailable = errors.New("access group administration unavailable")

type guardedAccessGroupStore interface {
	ListPage(context.Context, *access.GroupPageKey, int) ([]access.Group, bool, error)
	UpdateConditional(context.Context, int64, access.UpdateGroupInput, access.GroupPrecondition) (*access.Group, error)
	DeleteConditional(context.Context, int64, access.GroupPrecondition) error
}

func (h *AccessGroupHandler) GetAdminAccessGroup(ctx context.Context, id int64) (*access.Group, error) {
	if h == nil || h.store == nil {
		return nil, ErrAccessGroupUnavailable
	}
	return h.store.Get(ctx, id)
}
func (h *AccessGroupHandler) ListAdminAccessGroupsPage(ctx context.Context, after *access.GroupPageKey, limit int) ([]access.Group, bool, error) {
	s, ok := guardedGroupStore(h)
	if !ok {
		return nil, false, ErrAccessGroupUnavailable
	}
	return s.ListPage(ctx, after, limit)
}
func (h *AccessGroupHandler) CreateAdminAccessGroup(ctx context.Context, in access.CreateGroupInput) (*access.Group, error) {
	if h == nil || h.store == nil {
		return nil, ErrAccessGroupUnavailable
	}
	update := access.UpdateGroupInput{Name: &in.Name, LibraryIDs: &in.LibraryIDs, MaxPlaybackQuality: &in.MaxPlaybackQuality, MaxStreams: &in.MaxStreams, MaxTranscodes: &in.MaxTranscodes, MaxRemoteStreamBitrateKbps: &in.MaxRemoteStreamBitrateKbps, MaxLocalStreamBitrateKbps: &in.MaxLocalStreamBitrateKbps, AllowedPermissions: &in.AllowedPermissions}
	if err := normalizeAdminGroupInput(&update); err != nil {
		return nil, err
	}
	in.Name = *update.Name
	in.MaxPlaybackQuality = *update.MaxPlaybackQuality
	in.AllowedPermissions = *update.AllowedPermissions
	return h.store.Create(ctx, in)
}
func (h *AccessGroupHandler) UpdateAdminAccessGroup(ctx context.Context, id int64, in access.UpdateGroupInput, guard access.GroupPrecondition) (*access.Group, error) {
	s, ok := guardedGroupStore(h)
	if !ok {
		return nil, ErrAccessGroupUnavailable
	}
	if err := normalizeAdminGroupInput(&in); err != nil {
		return nil, err
	}
	return s.UpdateConditional(ctx, id, in, guard)
}
func (h *AccessGroupHandler) DeleteAdminAccessGroup(ctx context.Context, id int64, guard access.GroupPrecondition) error {
	s, ok := guardedGroupStore(h)
	if !ok {
		return ErrAccessGroupUnavailable
	}
	return s.DeleteConditional(ctx, id, guard)
}
func normalizeAdminGroupInput(in *access.UpdateGroupInput) error {
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return ErrInvalidAccessGroup
		}
		in.Name = &name
	}
	if in.LibraryIDs != nil {
		for _, id := range *in.LibraryIDs {
			if id <= 0 {
				return ErrInvalidAccessGroup
			}
		}
	}
	if in.MaxStreams != nil && *in.MaxStreams < 0 || in.MaxTranscodes != nil && *in.MaxTranscodes < 0 || in.MaxRemoteStreamBitrateKbps != nil && *in.MaxRemoteStreamBitrateKbps < 0 || in.MaxLocalStreamBitrateKbps != nil && *in.MaxLocalStreamBitrateKbps < 0 {
		return ErrInvalidAccessGroup
	}
	if in.MaxPlaybackQuality != nil {
		quality, ok := access.ParsePlaybackQualityPreset(*in.MaxPlaybackQuality)
		if !ok {
			return ErrInvalidAccessGroup
		}
		in.MaxPlaybackQuality = &quality
	}
	if in.AllowedPermissions != nil {
		permissions, err := auth.NormalizePermissions(*in.AllowedPermissions)
		if err != nil {
			return ErrInvalidAccessGroup
		}
		in.AllowedPermissions = &permissions
	}
	return nil
}

func guardedGroupStore(h *AccessGroupHandler) (guardedAccessGroupStore, bool) {
	if h == nil {
		return nil, false
	}
	s, ok := h.store.(guardedAccessGroupStore)
	return s, ok
}
