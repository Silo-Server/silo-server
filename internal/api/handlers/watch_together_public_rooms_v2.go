package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

// PublicRoomsAvailable reports whether the public-room operations are served.
func (h *WatchTogetherHandler) PublicRoomsAvailable() bool {
	return h != nil && h.Service != nil && h.TokenService != nil && h.Service.PublicRoomsAvailable()
}

// UpdateWatchTogetherVisibility lists or unlists a room and renews the host's room proof.
func (h *WatchTogetherHandler) UpdateWatchTogetherVisibility(ctx context.Context, room string, user int, profile string, visibility watchtogether.RoomVisibility) (watchtogether.Snapshot, string, error) {
	if !h.PublicRoomsAvailable() {
		return watchtogether.Snapshot{}, "", apiError(503, "unavailable", "Public watch parties are unavailable")
	}
	snapshot, err := h.Service.SetRoomVisibility(ctx, room, user, profile, visibility)
	if err != nil {
		return watchtogether.Snapshot{}, "", err
	}
	response, err := h.buildRoomResponse(ctx, snapshot, user, profile)
	return response.Room, response.RoomAccessToken, err
}

// ListWatchTogetherPublicRooms lists the server's open public rooms.
func (h *WatchTogetherHandler) ListWatchTogetherPublicRooms(ctx context.Context, user int, profile string) ([]watchtogether.PublicRoom, error) {
	if !h.PublicRoomsAvailable() {
		return nil, apiError(503, "unavailable", "Public watch parties are unavailable")
	}
	return h.Service.ListPublicRooms(ctx, user, profile)
}

// RequestWatchTogetherJoin asks a public room's host to let the profile in.
func (h *WatchTogetherHandler) RequestWatchTogetherJoin(ctx context.Context, room string, user int, profile string) (watchtogether.JoinRequest, error) {
	if !h.PublicRoomsAvailable() {
		return watchtogether.JoinRequest{}, apiError(503, "unavailable", "Public watch parties are unavailable")
	}
	return h.Service.RequestToJoin(ctx, room, user, profile)
}

// ListWatchTogetherJoinRequests lists a room's pending requests for its host.
func (h *WatchTogetherHandler) ListWatchTogetherJoinRequests(ctx context.Context, room string, user int, profile string) ([]watchtogether.JoinRequest, error) {
	if !h.PublicRoomsAvailable() {
		return nil, apiError(503, "unavailable", "Public watch parties are unavailable")
	}
	return h.Service.ListJoinRequests(ctx, room, user, profile)
}

// DecideWatchTogetherJoinRequest records the host's answer to a request.
func (h *WatchTogetherHandler) DecideWatchTogetherJoinRequest(ctx context.Context, room, request string, user int, profile string, status watchtogether.JoinRequestStatus) (watchtogether.JoinRequest, error) {
	if !h.PublicRoomsAvailable() {
		return watchtogether.JoinRequest{}, apiError(503, "unavailable", "Public watch parties are unavailable")
	}
	return h.Service.DecideJoinRequest(ctx, room, request, user, profile, status)
}

// ReadWatchTogetherJoinRequest returns the requester's own request. Once the
// host approves it, the read also carries the room snapshot and room proof,
// the same proof joining by code issues.
func (h *WatchTogetherHandler) ReadWatchTogetherJoinRequest(ctx context.Context, room, request string, user int, profile string) (watchtogether.JoinRequest, *watchtogether.Snapshot, string, error) {
	if !h.PublicRoomsAvailable() {
		return watchtogether.JoinRequest{}, nil, "", apiError(503, "unavailable", "Public watch parties are unavailable")
	}
	row, err := h.Service.ReadOwnJoinRequest(ctx, room, request, user, profile)
	if err != nil || row.Status != watchtogether.JoinRequestStatusApproved {
		return row, nil, "", err
	}
	snapshot, err := h.Service.Snapshot(ctx, room, user, profile)
	if err != nil {
		return watchtogether.JoinRequest{}, nil, "", err
	}
	response, err := h.buildRoomResponse(ctx, snapshot, user, profile)
	if err != nil {
		return watchtogether.JoinRequest{}, nil, "", err
	}
	return row, &response.Room, response.RoomAccessToken, nil
}
