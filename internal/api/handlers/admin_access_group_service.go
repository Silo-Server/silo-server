package handlers

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
)

var ErrInvalidAccessGroup = errors.New("invalid access group configuration")
var ErrAccessGroupUnavailable = errors.New("access group administration unavailable")

// memberMovingGroupStore deletes a group after moving its members into the
// default group in the same transaction (access.GroupStore).
type memberMovingGroupStore interface {
	DeleteMovingMembers(context.Context, int64, access.GroupPrecondition) error
}

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
	librariesChange := h.groupLibrariesChange(ctx, id, in)
	group, err := s.UpdateConditional(ctx, id, in, guard)
	if err != nil {
		return nil, err
	}
	if librariesChange {
		notifyAccountsScopeChanged(ctx, h.RecWorker, h.groupMembers(ctx, id)...)
	}
	return group, nil
}

// DeleteAdminAccessGroup deletes a group. Its members move into the default
// group in the same transaction, so no regular account is left without a
// group. They stay signed in: the move bumps their access_policy_revision and
// the next request resolves the default group's policy.
func (h *AccessGroupHandler) DeleteAdminAccessGroup(ctx context.Context, id int64, guard access.GroupPrecondition) error {
	s, ok := guardedGroupStore(h)
	if !ok {
		return ErrAccessGroupUnavailable
	}
	// The members are listed first: the delete moves them out of the group.
	members := h.groupMembers(ctx, id)
	var err error
	if mover, ok := h.store.(memberMovingGroupStore); ok {
		// Set-based, so the group-writer lock is not held for per-member statements.
		err = mover.DeleteMovingMembers(ctx, id, guard)
	} else {
		err = s.DeleteConditional(ctx, id, guard)
	}
	if err != nil {
		return err
	}
	notifyAccountsScopeChanged(ctx, h.RecWorker, members...)
	return nil
}

// accessGroupMemberLister lists the accounts in an access group.
// *access.GroupStore implements it.
type accessGroupMemberLister interface {
	MemberIDs(ctx context.Context, id int64) ([]int, error)
}

// groupMembers lists group id's member accounts for a recommendations
// rebuild; without a worker, or when the list fails, it is empty.
func (h *AccessGroupHandler) groupMembers(ctx context.Context, id int64) []int {
	lister, ok := h.store.(accessGroupMemberLister)
	if h.RecWorker == nil || !ok {
		return nil
	}
	members, err := lister.MemberIDs(ctx, id)
	if err != nil {
		slog.WarnContext(ctx, "listing access group members for a recommendations rebuild failed", "component", "api", "access_group_id", id, "error", err)
		return nil
	}
	return members
}

// groupLibrariesChange reports whether in changes group id's libraries. When
// the current group cannot be read it assumes they change.
func (h *AccessGroupHandler) groupLibrariesChange(ctx context.Context, id int64, in access.UpdateGroupInput) bool {
	if in.LibraryIDs == nil || h.RecWorker == nil {
		return false
	}
	current, err := h.store.Get(ctx, id)
	if err != nil || current == nil {
		return true
	}
	return (current.LibraryIDs == nil) != (*in.LibraryIDs == nil) || !sameIntSet(current.LibraryIDs, *in.LibraryIDs)
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
