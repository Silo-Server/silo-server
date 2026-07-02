package watchtogether

import (
	"context"
	"time"
)

type GuestControlPolicy string

const (
	GuestControlPolicyHostOnly       GuestControlPolicy = "host_only"
	GuestControlPolicyGuestPlayPause GuestControlPolicy = "guest_play_pause"
)

type RoomPhase string

const (
	RoomPhaseLobby   RoomPhase = "lobby"
	RoomPhasePlaying RoomPhase = "playing"
	RoomPhaseEnded   RoomPhase = "ended"
)

type RoomPlaybackState string

const (
	RoomPlaybackStateIdle    RoomPlaybackState = "idle"
	RoomPlaybackStateWaiting RoomPlaybackState = "waiting"
	RoomPlaybackStatePaused  RoomPlaybackState = "paused"
	RoomPlaybackStatePlaying RoomPlaybackState = "playing"
)

type RoomSelectionMode string

const (
	RoomSelectionModeHostPick RoomSelectionMode = "host_pick"
	RoomSelectionModeVote     RoomSelectionMode = "vote"
)

type MemberRole string

const (
	MemberRoleHost  MemberRole = "host"
	MemberRoleGuest MemberRole = "guest"
)

type TransportAction string

const (
	TransportActionPlay  TransportAction = "play"
	TransportActionPause TransportAction = "pause"
	TransportActionSeek  TransportAction = "seek"
)

type Room struct {
	ID                    string
	Code                  string
	JoinToken             string
	HostUserID            int
	HostProfileID         string
	Phase                 RoomPhase
	PlaybackState         RoomPlaybackState
	ResumeOnReady         bool
	SelectionMode         RoomSelectionMode
	SelectionRevision     int64
	SelectedContentID     *string
	SelectedFileID        *int
	SelectedLibraryID     *int
	GuestControlPolicy    GuestControlPolicy
	AnchorPositionSeconds float64
	IsPaused              bool
	AnchorUpdatedAt       time.Time
	Generation            int64
	CreatedAt             time.Time
	ClosedAt              *time.Time
}

// MemberSummary describes one connected room member in a snapshot.
type MemberSummary struct {
	UserID      int    `json:"user_id"`
	ProfileID   string `json:"profile_id"`
	DisplayName string `json:"display_name"`
	IsHost      bool   `json:"is_host"`
	IsSelf      bool   `json:"is_self"`
	Connected   bool   `json:"connected"`
}

type Snapshot struct {
	RoomID                  string             `json:"room_id"`
	Phase                   RoomPhase          `json:"phase"`
	PlaybackState           RoomPlaybackState  `json:"playback_state"`
	SelectionMode           RoomSelectionMode  `json:"selection_mode"`
	SelectionRevision       int64              `json:"selection_revision"`
	SelectedContentID       *string            `json:"selected_content_id,omitempty"`
	SelectedFileID          *int               `json:"selected_file_id,omitempty"`
	SelectedLibraryID       *int               `json:"selected_library_id,omitempty"`
	Code                    string             `json:"code"`
	GuestControlPolicy      GuestControlPolicy `json:"guest_control_policy"`
	IsPaused                bool               `json:"is_paused"`
	AnchorPositionSeconds   float64            `json:"anchor_position_seconds"`
	AnchorUpdatedAt         string             `json:"anchor_updated_at"`
	Generation              int64              `json:"generation"`
	MemberCount             int                `json:"member_count"`
	HostConnected           bool               `json:"host_connected"`
	SelfRole                MemberRole         `json:"self_role"`
	SelfCanControlTransport bool               `json:"self_can_control_transport"`
	SelfCanManageRoom       bool               `json:"self_can_manage_room"`
	SelfIgnoreWait          bool               `json:"self_ignore_wait"`
	AttachedSessionID       string             `json:"attached_session_id,omitempty"`
	InvitePath              string             `json:"invite_path,omitempty"`
	Members                 []MemberSummary    `json:"members,omitempty"`
}

type RoomJoinResult struct {
	Snapshot    Snapshot
	AccessToken string
}

type CreateRoomInput struct {
	HostUserID    int
	HostProfileID string
	SelectionMode RoomSelectionMode
}

type JoinInput struct {
	Code      string
	JoinToken string
}

type SelectItemInput struct {
	ContentID string
	FileID    *int
	LibraryID *int
}

type ResolvedSelection struct {
	ContentID string
	FileID    *int
	LibraryID *int
}

type TransportRequest struct {
	Action          TransportAction
	PositionSeconds *float64
	IsPaused        bool
}

type StateReport struct {
	SessionID       string
	PositionSeconds float64
	IsPaused        bool
}

type TransportCommand struct {
	CommandID         string            `json:"command_id"`
	SessionID         string            `json:"session_id,omitempty"`
	SelectionRevision int64             `json:"selection_revision"`
	Action            TransportAction   `json:"action"`
	PositionSeconds   float64           `json:"position_seconds"`
	ExecuteAt         string            `json:"execute_at"`
	IssuedAt          string            `json:"issued_at"`
	PlaybackState     RoomPlaybackState `json:"playback_state"`
}

// Suggestion represents a content suggestion in a vote-mode room.
type Suggestion struct {
	ID                 string    `json:"id"`
	RoomID             string    `json:"room_id"`
	SuggesterUserID    int       `json:"suggester_user_id"`
	SuggesterProfileID string    `json:"suggester_profile_id"`
	ContentID          string    `json:"content_id"`
	ContentType        string    `json:"content_type"`
	Title              string    `json:"title"`
	Subtitle           string    `json:"subtitle"`
	PosterURL          string    `json:"poster_url"`
	Note               string    `json:"note"`
	VoteCount          int       `json:"vote_count"`
	VotedByMe          bool      `json:"voted_by_me"`
	CreatedAt          time.Time `json:"created_at"`
}

type CreateSuggestionInput struct {
	ContentID   string
	ContentType string
	Title       string
	Subtitle    string
	PosterURL   string
	Note        string
}

// SuggestionStore provides persistence for room suggestions and votes.
type SuggestionStore interface {
	CreateSuggestion(ctx context.Context, s Suggestion) (*Suggestion, error)
	GetSuggestion(ctx context.Context, id string) (*Suggestion, error)
	ListSuggestions(ctx context.Context, roomID string, voterProfileID string) ([]Suggestion, error)
	DeleteSuggestion(ctx context.Context, id string) error
	AddVote(ctx context.Context, suggestionID string, voterProfileID string) error
	RemoveVote(ctx context.Context, suggestionID string, voterProfileID string) error
}
