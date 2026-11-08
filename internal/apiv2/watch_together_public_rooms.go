package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

// watchTogetherNoStore keeps room listings and request state out of caches.
const watchTogetherNoStore = "no-store"

// WatchTogetherPublicRoomsService lists public rooms and carries the request
// a profile makes to join one without its code.
type WatchTogetherPublicRoomsService interface {
	UpdateWatchTogetherVisibility(context.Context, string, int, string, watchtogether.RoomVisibility) (watchtogether.Snapshot, string, error)
	ListWatchTogetherPublicRooms(context.Context, int, string) ([]watchtogether.PublicRoom, error)
	RequestWatchTogetherJoin(context.Context, string, int, string) (watchtogether.JoinRequest, error)
	ListWatchTogetherJoinRequests(context.Context, string, int, string) ([]watchtogether.JoinRequest, error)
	DecideWatchTogetherJoinRequest(context.Context, string, string, int, string, watchtogether.JoinRequestStatus) (watchtogether.JoinRequest, error)
	ReadWatchTogetherJoinRequest(context.Context, string, string, int, string) (watchtogether.JoinRequest, *watchtogether.Snapshot, string, error)
}

type WatchTogetherVisibilityInput struct {
	RoomID string `path:"room_id"`
	Body   struct {
		Visibility watchtogether.RoomVisibility `json:"visibility" enum:"private,public"`
	}
}

type WatchTogetherJoinRequest struct {
	ID          ID                              `json:"id"`
	RoomID      ID                              `json:"room_id"`
	UserID      ID                              `json:"user_id"`
	ProfileID   string                          `json:"profile_id"`
	DisplayName string                          `json:"display_name"`
	Status      watchtogether.JoinRequestStatus `json:"status" enum:"pending,approved,denied"`
	CreatedAt   Instant                         `json:"created_at"`
	DecidedAt   *Instant                        `json:"decided_at,omitempty"`
}

type WatchTogetherPublicRoom struct {
	RoomID            ID                              `json:"room_id"`
	HostUserID        ID                              `json:"host_user_id"`
	HostProfileID     string                          `json:"host_profile_id"`
	HostDisplayName   string                          `json:"host_display_name"`
	HostConnected     bool                            `json:"host_connected"`
	Phase             watchtogether.RoomPhase         `json:"phase" enum:"lobby,playing" doc:"lobby while the room is choosing what to watch; playing once it has started"`
	PlaybackState     watchtogether.RoomPlaybackState `json:"playback_state" enum:"idle,waiting,paused,playing"`
	SelectionMode     watchtogether.RoomSelectionMode `json:"selection_mode" enum:"host_pick,vote"`
	SelectedContentID *ID                             `json:"selected_content_id,omitempty" doc:"The staged or playing item. Resolve it through the catalog, which applies the viewer's library access."`
	MemberCount       int                             `json:"member_count"`
	CreatedAt         Instant                         `json:"created_at"`
	IsHost            bool                            `json:"is_host" doc:"The viewer hosts this room"`
	JoinRequest       *WatchTogetherJoinRequest       `json:"join_request,omitempty" doc:"The viewer's own request to join this room, when it has made one"`
}

type WatchTogetherPublicRoomsOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		Rooms []WatchTogetherPublicRoom `json:"rooms" doc:"Open public rooms, newest first, at most 100"`
	}
}

type WatchTogetherJoinRequestRoomInput struct {
	RoomID string `path:"room_id"`
}

type WatchTogetherJoinRequestInput struct {
	RoomID    string `path:"room_id"`
	RequestID string `path:"request_id" maxLength:"64"`
}

type WatchTogetherJoinRequestDecisionInput struct {
	RoomID    string `path:"room_id"`
	RequestID string `path:"request_id" maxLength:"64"`
	Body      struct {
		Status watchtogether.JoinRequestStatus `json:"status" enum:"approved,denied"`
	}
}

type WatchTogetherJoinRequestOutput struct {
	Body struct {
		JoinRequest WatchTogetherJoinRequest `json:"join_request"`
	}
}

type WatchTogetherJoinRequestListOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		JoinRequests []WatchTogetherJoinRequest `json:"join_requests" doc:"Pending requests, oldest first"`
	}
}

type WatchTogetherJoinRequestReadOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		JoinRequest     WatchTogetherJoinRequest   `json:"join_request"`
		Room            *WatchTogetherRoomSnapshot `json:"room,omitempty" doc:"Present once the host approves the request"`
		RoomAccessToken string                     `json:"room_access_token,omitempty" doc:"Room proof, present once the host approves the request"`
	}
}

// watchTogetherJoinRequestOf converts a stored request to its wire shape.
func watchTogetherJoinRequestOf(row watchtogether.JoinRequest) WatchTogetherJoinRequest {
	out := WatchTogetherJoinRequest{
		ID: ID(row.ID), RoomID: ID(row.RoomID), UserID: ID(strconv.Itoa(row.UserID)), ProfileID: row.ProfileID,
		DisplayName: row.DisplayName, Status: row.Status, CreatedAt: NewInstant(row.CreatedAt),
	}
	if row.DecidedAt != nil {
		out.DecidedAt = new(NewInstant(*row.DecidedAt))
	}
	return out
}

// publicRoomsProblem maps public-room service errors to problem responses.
func publicRoomsProblem(err error) error {
	switch {
	case errors.Is(err, watchtogether.ErrPublicRoomsUnavailable):
		return unavailable("public watch party")
	case errors.Is(err, watchtogether.ErrRoomForbidden):
		return NewProblem(TypePermissionDenied, "Only the host account and profile may manage who joins the room.")
	case errors.Is(err, watchtogether.ErrRoomNotPublic):
		return NewProblem(TypeConflict, "The room is not public.")
	case errors.Is(err, watchtogether.ErrJoinRequestApproved):
		return NewProblem(TypeConflict, "The request is already approved. An approval cannot be withdrawn.")
	case errors.Is(err, watchtogether.ErrJoinRequestNotFound):
		return NewProblem(TypeNotFound, "The join request was not found.")
	case errors.Is(err, watchtogether.ErrInvalidJoinRequest):
		return NewProblem(TypeValidationFailed, "The host is already in this room.")
	case errors.Is(err, watchtogether.ErrInvalidVisibility):
		return NewProblem(TypeValidationFailed, "Invalid room visibility.")
	default:
		return suggestionProblem(err)
	}
}

// publicRoomsIdentity resolves the service and the caller's account and profile.
func (reg *Registry) publicRoomsIdentity(ctx context.Context) (WatchTogetherPublicRoomsService, int, string, error) {
	svc := reg.deps.WatchTogetherPublicRooms
	if svc == nil {
		return nil, 0, "", unavailable("public watch party")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, 0, "", p
	}
	return svc, user, profile, nil
}

const watchTogetherRoomPath = Prefix + "/watch-together/rooms/{room_id}"

// registerWatchTogetherPublicRooms registers the visibility and join-request operations.
func registerWatchTogetherPublicRooms(reg *Registry) {
	registerWatchTogetherVisibility(reg)
	registerWatchTogetherJoinRequestCreation(reg)
	registerWatchTogetherJoinRequestReview(reg)
}

// registerWatchTogetherVisibility registers the visibility switch and the open-room list.
func registerWatchTogetherVisibility(reg *Registry) {
	const room = watchTogetherRoomPath
	visibility := Operation{Operation: humaOp(http.MethodPatch, room+"/visibility", "updateWatchTogetherRoomVisibility", "realtime", "List or unlist a room as the host. A public room appears in the open-party list to every profile on the server, which may ask to join without the code; the host still approves each request. Unlisting keeps pending requests for the host to answer."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	visibility.MaxBodyBytes = 1024
	visibility.Errors = []int{409}
	Register(reg, visibility, func(ctx context.Context, in *WatchTogetherVisibilityInput) (*WatchTogetherRoomReadOutput, error) {
		svc, user, profile, err := reg.publicRoomsIdentity(ctx)
		if err != nil {
			return nil, err
		}
		row, token, err := svc.UpdateWatchTogetherVisibility(ctx, in.RoomID, user, profile, in.Body.Visibility)
		if err != nil {
			return nil, publicRoomsProblem(err)
		}
		return watchTogetherRoomOutput(row, token)
	})

	list := Operation{Operation: humaOp(http.MethodGet, Prefix+"/watch-together/public-rooms", "listWatchTogetherPublicRooms", "realtime", "List open public rooms on this server, newest first, with whether each is still choosing or already playing. Room codes are never listed; ask the host with a join request."), Class: ClassProfileScoped, ServiceBacked: true}
	Register(reg, list, func(ctx context.Context, _ *struct{}) (*WatchTogetherPublicRoomsOutput, error) {
		svc, user, profile, err := reg.publicRoomsIdentity(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := svc.ListWatchTogetherPublicRooms(ctx, user, profile)
		if err != nil {
			return nil, publicRoomsProblem(err)
		}
		out := &WatchTogetherPublicRoomsOutput{CacheControl: watchTogetherNoStore}
		out.Body.Rooms = make([]WatchTogetherPublicRoom, 0, len(rows))
		for _, row := range rows {
			listed := WatchTogetherPublicRoom{
				RoomID: ID(row.RoomID), HostUserID: ID(strconv.Itoa(row.HostUserID)), HostProfileID: row.HostProfileID,
				HostDisplayName: row.HostDisplayName, HostConnected: row.HostConnected, Phase: row.Phase,
				PlaybackState: row.PlaybackState, SelectionMode: row.SelectionMode, MemberCount: row.MemberCount,
				CreatedAt: NewInstant(row.CreatedAt), IsHost: row.IsHost,
			}
			if row.SelectedContentID != nil {
				listed.SelectedContentID = new(ID(*row.SelectedContentID))
			}
			if row.Request != nil {
				listed.JoinRequest = new(watchTogetherJoinRequestOf(*row.Request))
			}
			out.Body.Rooms = append(out.Body.Rooms, listed)
		}
		return out, nil
	})

}

// registerWatchTogetherJoinRequestCreation registers the requester's side:
// asking to join and reading the answer.
func registerWatchTogetherJoinRequestCreation(reg *Registry) {
	const room = watchTogetherRoomPath
	create := Operation{Operation: humaOp(http.MethodPost, room+"/join-requests", "createWatchTogetherJoinRequest", "realtime", "Ask the host of a public room to let this profile in. The host is notified on the room socket. Asking again returns the existing request; a denied request becomes pending again. Read the request until the host approves it, which returns room proof."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	create.Errors = []int{409}
	Register(reg, create, func(ctx context.Context, in *WatchTogetherJoinRequestRoomInput) (*WatchTogetherJoinRequestOutput, error) {
		svc, user, profile, err := reg.publicRoomsIdentity(ctx)
		if err != nil {
			return nil, err
		}
		row, err := svc.RequestWatchTogetherJoin(ctx, in.RoomID, user, profile)
		if err != nil {
			return nil, publicRoomsProblem(err)
		}
		out := new(WatchTogetherJoinRequestOutput)
		out.Body.JoinRequest = watchTogetherJoinRequestOf(row)
		return out, nil
	})

	registerWatchTogetherJoinRequestRead(reg)
}

// registerWatchTogetherJoinRequestReview registers the host's side.
func registerWatchTogetherJoinRequestReview(reg *Registry) {
	const room = watchTogetherRoomPath
	pending := Operation{Operation: humaOp(http.MethodGet, room+"/join-requests", "listWatchTogetherJoinRequests", "realtime", "List the room's pending join requests as the host, oldest first. The room socket sends the same list as join_requests_update whenever it changes."), Class: ClassProfileScoped, ServiceBacked: true}
	pending.Errors = []int{409}
	Register(reg, pending, func(ctx context.Context, in *WatchTogetherJoinRequestRoomInput) (*WatchTogetherJoinRequestListOutput, error) {
		svc, user, profile, err := reg.publicRoomsIdentity(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := svc.ListWatchTogetherJoinRequests(ctx, in.RoomID, user, profile)
		if err != nil {
			return nil, publicRoomsProblem(err)
		}
		out := &WatchTogetherJoinRequestListOutput{CacheControl: watchTogetherNoStore}
		out.Body.JoinRequests = make([]WatchTogetherJoinRequest, 0, len(rows))
		for _, row := range rows {
			out.Body.JoinRequests = append(out.Body.JoinRequests, watchTogetherJoinRequestOf(row))
		}
		return out, nil
	})

	decide := Operation{Operation: humaOp(http.MethodPatch, room+"/join-requests/{request_id}", "decideWatchTogetherJoinRequest", "realtime", "Approve or deny a join request as the host. A denied request may later be approved; an approved request is final, because the requester already holds room proof, and denying it returns 409."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	decide.MaxBodyBytes = 1024
	decide.Errors = []int{409}
	Register(reg, decide, func(ctx context.Context, in *WatchTogetherJoinRequestDecisionInput) (*WatchTogetherJoinRequestOutput, error) {
		svc, user, profile, err := reg.publicRoomsIdentity(ctx)
		if err != nil {
			return nil, err
		}
		row, err := svc.DecideWatchTogetherJoinRequest(ctx, in.RoomID, in.RequestID, user, profile, in.Body.Status)
		if err != nil {
			if errors.Is(err, watchtogether.ErrInvalidJoinRequest) {
				return nil, NewProblem(TypeValidationFailed, "Invalid join request decision.")
			}
			return nil, publicRoomsProblem(err)
		}
		out := new(WatchTogetherJoinRequestOutput)
		out.Body.JoinRequest = watchTogetherJoinRequestOf(row)
		return out, nil
	})

}

// registerWatchTogetherJoinRequestRead registers the requester's read of its own request.
func registerWatchTogetherJoinRequestRead(reg *Registry) {
	const room = watchTogetherRoomPath
	read := Operation{Operation: humaOp(http.MethodGet, room+"/join-requests/{request_id}", "getWatchTogetherJoinRequest", "realtime", "Read this profile's own join request. Once approved, the response also carries the room snapshot and room proof, the same proof joining by code issues. Another profile's request reads as not found."), Class: ClassProfileScoped, ServiceBacked: true}
	read.Errors = []int{409}
	Register(reg, read, func(ctx context.Context, in *WatchTogetherJoinRequestInput) (*WatchTogetherJoinRequestReadOutput, error) {
		svc, user, profile, err := reg.publicRoomsIdentity(ctx)
		if err != nil {
			return nil, err
		}
		row, snapshot, token, err := svc.ReadWatchTogetherJoinRequest(ctx, in.RoomID, in.RequestID, user, profile)
		if err != nil {
			return nil, publicRoomsProblem(err)
		}
		out := &WatchTogetherJoinRequestReadOutput{CacheControl: watchTogetherNoStore}
		out.Body.JoinRequest = watchTogetherJoinRequestOf(row)
		if snapshot != nil {
			roomOut, err := watchTogetherRoomSnapshotOf(*snapshot)
			if err != nil {
				return nil, NewProblem(TypeInternalError, "Invalid room snapshot.")
			}
			out.Body.Room = &roomOut
			out.Body.RoomAccessToken = token
		}
		return out, nil
	})
}
