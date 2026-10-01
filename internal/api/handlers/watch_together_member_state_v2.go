package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

// WatchTogetherAvailable reports whether rooms are served at all; the
// capability document keys off it.
func (h *WatchTogetherHandler) WatchTogetherAvailable() bool {
	return h != nil && h.Service != nil && h.TokenService != nil
}

// RoomMemberState classifies accessible content for the room's connected
// members. The caller must already hold room proof.
func (h *WatchTogetherHandler) RoomMemberState(ctx context.Context, room string, user int, profile string, contentIDs []string, filter catalog.AccessFilter) ([]watchtogether.MemberSummary, []watchtogether.ItemMemberState, error) {
	if h == nil || h.Service == nil || h.MemberState == nil || h.MemberStateCatalog == nil {
		return nil, nil, apiError(503, "unavailable", "Watch together member state is unavailable")
	}
	members, err := h.Service.ConnectedMembers(ctx, room, user, profile)
	if err != nil {
		return nil, nil, err
	}
	// Reuse the catalog's mixed item/episode access query. Checking only
	// media_items would drop episodes; checking only their parent would miss
	// episode library restrictions.
	visible, err := h.MemberStateCatalog.GetSearchItemsByIDsWithAccess(ctx, contentIDs, filter)
	if err != nil {
		return nil, nil, err
	}
	allowed := make(map[string]bool, len(visible))
	for _, item := range visible {
		allowed[item.ContentID] = true
	}
	ids := make([]string, 0, len(contentIDs))
	for _, id := range contentIDs {
		if allowed[id] {
			ids = append(ids, id)
		}
	}
	items, err := h.MemberState.MemberState(ctx, members, ids)
	if err != nil {
		return nil, nil, err
	}
	return members, items, nil
}

// RoomPicker computes the together rows for the room and resolves them to
// items the caller may see.
func (h *WatchTogetherHandler) RoomPicker(ctx context.Context, room string, user int, profile string, filter catalog.AccessFilter) (watchtogether.PickerView, error) {
	if h == nil || h.Service == nil || h.MemberState == nil || h.Details == nil {
		return watchtogether.PickerView{}, apiError(503, "unavailable", "Watch together picker is unavailable")
	}
	members, err := h.Service.ConnectedMembers(ctx, room, user, profile)
	if err != nil {
		return watchtogether.PickerView{}, err
	}
	rows, err := h.MemberState.Picker(ctx, members)
	if err != nil {
		return watchtogether.PickerView{}, err
	}
	return watchtogether.ResolvePicker(ctx, h.Details, filter, members, rows)
}
