package handlers

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
)

type AccessGroupStore interface {
	List(ctx context.Context) ([]access.Group, error)
	Get(ctx context.Context, id int64) (*access.Group, error)
	Create(ctx context.Context, input access.CreateGroupInput) (*access.Group, error)
	Update(ctx context.Context, id int64, input access.UpdateGroupInput) (*access.Group, error)
	Delete(ctx context.Context, id int64) error
}

type AccessGroupHandler struct {
	store AccessGroupStore
	// RecWorker is told when a group's libraries change or the group is
	// deleted, so its members' recommendations are rebuilt. Optional.
	RecWorker AccountsScopeNotifier
}

func NewAccessGroupHandler(store AccessGroupStore) *AccessGroupHandler {
	return &AccessGroupHandler{store: store}
}

type accessGroupCreateRequest struct {
	Name                     string                      `json:"name"`
	Description              string                      `json:"description"`
	LibraryIDs               accessGroupIntSliceField    `json:"library_ids"`
	MaxPlaybackQuality       string                      `json:"max_playback_quality"`
	DownloadAllowed          *bool                       `json:"download_allowed,omitempty"`
	DownloadTranscodeAllowed *bool                       `json:"download_transcode_allowed,omitempty"`
	TranscodeAllowed         *bool                       `json:"transcode_allowed,omitempty"`
	AudioTranscodeAllowed    *bool                       `json:"audio_transcode_allowed,omitempty"`
	MaxStreams               *int                        `json:"max_streams,omitempty"`
	MaxTranscodes            *int                        `json:"max_transcodes,omitempty"`
	AllowedPermissions       accessGroupStringSliceField `json:"allowed_permissions"`
	RequestsAllowed          *bool                       `json:"requests_allowed,omitempty"`
	IsDefault                bool                        `json:"is_default"`
}

type accessGroupUpdateRequest struct {
	Name                     *string                     `json:"name,omitempty"`
	Description              *string                     `json:"description,omitempty"`
	LibraryIDs               accessGroupIntSliceField    `json:"library_ids,omitempty"`
	MaxPlaybackQuality       *string                     `json:"max_playback_quality,omitempty"`
	DownloadAllowed          *bool                       `json:"download_allowed,omitempty"`
	DownloadTranscodeAllowed *bool                       `json:"download_transcode_allowed,omitempty"`
	TranscodeAllowed         *bool                       `json:"transcode_allowed,omitempty"`
	AudioTranscodeAllowed    *bool                       `json:"audio_transcode_allowed,omitempty"`
	MaxStreams               *int                        `json:"max_streams,omitempty"`
	MaxTranscodes            *int                        `json:"max_transcodes,omitempty"`
	AllowedPermissions       accessGroupStringSliceField `json:"allowed_permissions,omitempty"`
	RequestsAllowed          *bool                       `json:"requests_allowed,omitempty"`
	IsDefault                *bool                       `json:"is_default,omitempty"`
}

type accessGroupResponse struct {
	ID                       int64     `json:"id"`
	Name                     string    `json:"name"`
	Description              string    `json:"description"`
	LibraryIDs               []int     `json:"library_ids"`
	MaxPlaybackQuality       string    `json:"max_playback_quality"`
	DownloadAllowed          bool      `json:"download_allowed"`
	DownloadTranscodeAllowed bool      `json:"download_transcode_allowed"`
	TranscodeAllowed         bool      `json:"transcode_allowed"`
	AudioTranscodeAllowed    bool      `json:"audio_transcode_allowed"`
	MaxStreams               int       `json:"max_streams"`
	MaxTranscodes            int       `json:"max_transcodes"`
	AllowedPermissions       []string  `json:"allowed_permissions"`
	RequestsAllowed          bool      `json:"requests_allowed"`
	IsDefault                bool      `json:"is_default"`
	MemberCount              int       `json:"member_count"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
}

type accessGroupIntSliceField struct {
	Set   bool
	Value []int
}

type accessGroupStringSliceField struct {
	Set   bool
	Value []string
}
