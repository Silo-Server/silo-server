package apiv2

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherRoomReadService interface {
	ReadWatchTogetherRoom(context.Context, string, int, string, string) (watchtogether.Snapshot, string, error)
}
type WatchTogetherRoomReadInput struct {
	RoomID    string `path:"room_id"`
	RoomToken string `header:"X-Room-Token" maxLength:"4096"`
}
type WatchTogetherRoomMember struct {
	UserID      ID     `json:"user_id"`
	ProfileID   string `json:"profile_id"`
	DisplayName string `json:"display_name"`
	IsHost      bool   `json:"is_host"`
	IsSelf      bool   `json:"is_self"`
	Connected   bool   `json:"connected"`
}
type WatchTogetherRoomSnapshot struct {
	RoomID                  ID                               `json:"room_id"`
	Phase                   watchtogether.RoomPhase          `json:"phase" enum:"lobby,playing,ended"`
	PlaybackState           watchtogether.RoomPlaybackState  `json:"playback_state" enum:"idle,waiting,paused,playing"`
	SelectionMode           watchtogether.RoomSelectionMode  `json:"selection_mode" enum:"host_pick,vote"`
	SelectionRevision       int64                            `json:"selection_revision"`
	SelectedContentID       *ID                              `json:"selected_content_id,omitempty"`
	SelectedFileID          *ID                              `json:"selected_file_id,omitempty"`
	SelectedLibraryID       *ID                              `json:"selected_library_id,omitempty"`
	Code                    string                           `json:"code"`
	GuestControlPolicy      watchtogether.GuestControlPolicy `json:"guest_control_policy" enum:"host_only,guest_play_pause"`
	IsPaused                bool                             `json:"is_paused"`
	AnchorPositionSeconds   float64                          `json:"anchor_position_seconds"`
	AnchorUpdatedAt         Instant                          `json:"anchor_updated_at"`
	Generation              int64                            `json:"generation"`
	MemberCount             int                              `json:"member_count"`
	HostConnected           bool                             `json:"host_connected"`
	SelfRole                watchtogether.MemberRole         `json:"self_role" enum:"host,guest"`
	SelfCanControlTransport bool                             `json:"self_can_control_transport"`
	SelfCanManageRoom       bool                             `json:"self_can_manage_room"`
	SelfIgnoreWait          bool                             `json:"self_ignore_wait"`
	AttachedSessionID       string                           `json:"attached_session_id,omitempty"`
	InvitePath              string                           `json:"invite_path,omitempty"`
	Members                 []WatchTogetherRoomMember        `json:"members,omitempty"`
}
type WatchTogetherRoomReadOutput struct {
	Body struct {
		Room            WatchTogetherRoomSnapshot `json:"room"`
		RoomAccessToken string                    `json:"room_access_token"`
	}
}

func watchTogetherRoomOutput(row watchtogether.Snapshot, token string) (*WatchTogetherRoomReadOutput, error) {
	snapshot, err := watchTogetherRoomSnapshotOf(row)
	if err != nil {
		return nil, NewProblem(TypeInternalError, "Invalid room snapshot.")
	}
	out := new(WatchTogetherRoomReadOutput)
	out.Body.Room = snapshot
	out.Body.RoomAccessToken = token
	return out, nil
}

func watchTogetherRoomSnapshotOf(row watchtogether.Snapshot) (WatchTogetherRoomSnapshot, error) {
	instant, err := time.Parse(time.RFC3339Nano, row.AnchorUpdatedAt)
	if err != nil {
		return WatchTogetherRoomSnapshot{}, err
	}
	out := WatchTogetherRoomSnapshot{RoomID: ID(row.RoomID), AnchorUpdatedAt: NewInstant(instant),
		Phase:                   row.Phase,
		PlaybackState:           row.PlaybackState,
		SelectionMode:           row.SelectionMode,
		SelectionRevision:       row.SelectionRevision,
		Code:                    row.Code,
		GuestControlPolicy:      row.GuestControlPolicy,
		IsPaused:                row.IsPaused,
		AnchorPositionSeconds:   row.AnchorPositionSeconds,
		Generation:              row.Generation,
		MemberCount:             row.MemberCount,
		HostConnected:           row.HostConnected,
		SelfRole:                row.SelfRole,
		SelfCanControlTransport: row.SelfCanControlTransport,
		SelfCanManageRoom:       row.SelfCanManageRoom,
		SelfIgnoreWait:          row.SelfIgnoreWait,
		AttachedSessionID:       row.AttachedSessionID,
		InvitePath:              row.InvitePath,
	}
	if row.SelectedContentID != nil {
		out.SelectedContentID = new(ID(*row.SelectedContentID))
	}
	if row.SelectedFileID != nil {
		out.SelectedFileID = new(ID(strconv.Itoa(*row.SelectedFileID)))
	}
	if row.SelectedLibraryID != nil {
		out.SelectedLibraryID = new(ID(strconv.Itoa(*row.SelectedLibraryID)))
	}
	out.Members = make([]WatchTogetherRoomMember, 0, len(row.Members))
	for _, m := range row.Members {
		out.Members = append(out.Members, WatchTogetherRoomMember{UserID: ID(strconv.Itoa(m.UserID)), ProfileID: m.ProfileID, DisplayName: m.DisplayName, IsHost: m.IsHost, IsSelf: m.IsSelf, Connected: m.Connected})
	}
	return out, nil
}
func registerWatchTogetherRoomRead(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/watch-together/rooms/{room_id}", "getWatchTogetherRoom", "realtime", "Read the room snapshot and renew existing room proof. This proof is not a session-bound socket credential."), Class: ClassProfileScoped, ServiceBacked: true}
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherRoomReadInput) (*WatchTogetherRoomReadOutput, error) {
		if reg.deps.WatchTogetherRoomRead == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		row, token, err := reg.deps.WatchTogetherRoomRead.ReadWatchTogetherRoom(ctx, in.RoomID, user, profile, in.RoomToken)
		if err != nil {
			return nil, suggestionProblem(err)
		}
		return watchTogetherRoomOutput(row, token)
	})
}
