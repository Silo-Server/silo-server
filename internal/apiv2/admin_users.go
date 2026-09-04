package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// The admin users domain: every login account as an administrator sees it.

// EffectivePolicy is an account's resolved access policy: its own overrides
// layered on its access group's values.
type EffectivePolicy struct {
	LibraryIDs               []ID     `json:"library_ids" doc:"Libraries the account may see; empty means every library"`
	MaxPlaybackQuality       string   `json:"max_playback_quality" doc:"Playback ceiling; empty means none"`
	MaxStreams               int      `json:"max_streams" doc:"Concurrent stream limit; 0 means unlimited"`
	MaxTranscodes            int      `json:"max_transcodes" doc:"Concurrent transcode limit; 0 means unlimited"`
	TranscodeAllowed         bool     `json:"transcode_allowed"`
	AudioTranscodeAllowed    bool     `json:"audio_transcode_allowed"`
	DownloadAllowed          bool     `json:"download_allowed"`
	DownloadTranscodeAllowed bool     `json:"download_transcode_allowed"`
	RequestsAllowed          bool     `json:"requests_allowed"`
	Permissions              []string `json:"permissions" doc:"Effective assignable permissions"`
}

// AdminUser is one login account with its own policy overrides (null =
// inherit from the access group) and the effective result.
type AdminUser struct {
	ID                       ID              `json:"id"`
	Username                 string          `json:"username"`
	Email                    string          `json:"email" doc:"Contact email; empty when none is set"`
	Role                     string          `json:"role" enum:"admin,user"`
	Permissions              []string        `json:"permissions" doc:"Permissions assigned directly to the account"`
	Enabled                  bool            `json:"enabled"`
	LibraryIDs               []ID            `json:"library_ids" nullable:"true" doc:"Explicit library allowlist; null inherits the group's, empty means none"`
	MaxPlaybackQuality       *string         `json:"max_playback_quality" nullable:"true" doc:"Playback ceiling override; null inherits, empty string means no ceiling"`
	MaxStreams               *int            `json:"max_streams" nullable:"true" doc:"Stream limit override; null inherits, 0 means unlimited"`
	MaxTranscodes            *int            `json:"max_transcodes" nullable:"true" doc:"Transcode limit override; null inherits, 0 means unlimited"`
	TranscodeAllowed         *bool           `json:"transcode_allowed" nullable:"true" doc:"Override; null inherits"`
	AudioTranscodeAllowed    *bool           `json:"audio_transcode_allowed" nullable:"true" doc:"Override; null inherits"`
	MaxProfiles              int             `json:"max_profiles" doc:"Household profile limit"`
	DownloadAllowed          *bool           `json:"download_allowed" nullable:"true" doc:"Override; null inherits"`
	DownloadTranscodeAllowed *bool           `json:"download_transcode_allowed" nullable:"true" doc:"Override; null inherits"`
	RequestsAllowed          *bool           `json:"requests_allowed" nullable:"true" doc:"Override; null inherits"`
	AccessGroupID            *ID             `json:"access_group_id" nullable:"true" doc:"The access group the account belongs to; null when none"`
	EffectivePolicy          EffectivePolicy `json:"effective_policy" doc:"The resolved policy the server enforces"`
	CreatedAt                Instant         `json:"created_at"`
	UpdatedAt                Instant         `json:"updated_at"`
	LastActiveAt             NullableInstant `json:"last_active_at" doc:"Most recent recorded activity; null when the account has none"`
}

// AdminUserCollectionOutput is the listAdminUsers response: a bounded
// collection, so no page object.
type AdminUserCollectionOutput struct {
	Body AdminUserCollection
}

// AdminUserCollection is the named envelope the contract carries.
type AdminUserCollection struct {
	Collection[AdminUser]
}

func registerAdminUsers(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/admin/users", "listAdminUsers", "admin",
			"List every login account with its policy overrides and effective policy."),
		Class:          ClassActingAdmin,
		DemoRestricted: true,
	}, reg.listAdminUsers)
}

// listAdminUsers answers from the same account listing v1 GET /admin/users
// uses.
func (reg *Registry) listAdminUsers(ctx context.Context, _ *struct{}) (*AdminUserCollectionOutput, error) {
	if reg.deps.AdminUsers == nil {
		return nil, unavailable("administration")
	}
	views, err := reg.deps.AdminUsers.ListAdminUsers(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]AdminUser, 0, len(views))
	for i := range views {
		items = append(items, adminUserFromView(views[i]))
	}
	return &AdminUserCollectionOutput{Body: AdminUserCollection{Collection: NewCollection(items)}}, nil
}

func adminUserFromView(v handlers.AdminUserView) AdminUser {
	out := AdminUser{
		ID:                       IDFromInt(int64(v.ID)),
		Username:                 v.Username,
		Email:                    v.Email,
		Role:                     roleOf(v.Role),
		Permissions:              NonNil(v.Permissions),
		Enabled:                  v.Enabled,
		LibraryIDs:               idsOfInts(v.LibraryIDs),
		MaxPlaybackQuality:       v.MaxPlaybackQuality,
		MaxStreams:               v.MaxStreams,
		MaxTranscodes:            v.MaxTranscodes,
		TranscodeAllowed:         v.TranscodeAllowed,
		AudioTranscodeAllowed:    v.AudioTranscodeAllowed,
		MaxProfiles:              v.MaxProfiles,
		DownloadAllowed:          v.DownloadAllowed,
		DownloadTranscodeAllowed: v.DownloadTranscodeAllowed,
		RequestsAllowed:          v.RequestsAllowed,
		EffectivePolicy: EffectivePolicy{
			LibraryIDs:               NonNil(idsOfInts(v.EffectivePolicy.LibraryIDs)),
			MaxPlaybackQuality:       v.EffectivePolicy.MaxPlaybackQuality,
			MaxStreams:               v.EffectivePolicy.MaxStreams,
			MaxTranscodes:            v.EffectivePolicy.MaxTranscodes,
			TranscodeAllowed:         v.EffectivePolicy.TranscodeAllowed,
			AudioTranscodeAllowed:    v.EffectivePolicy.AudioTranscodeAllowed,
			DownloadAllowed:          v.EffectivePolicy.DownloadAllowed,
			DownloadTranscodeAllowed: v.EffectivePolicy.DownloadTranscodeAllowed,
			RequestsAllowed:          v.EffectivePolicy.RequestsAllowed,
			Permissions:              NonNil(v.EffectivePolicy.Permissions),
		},
		CreatedAt: NewInstant(v.CreatedAt),
		UpdatedAt: NewInstant(v.UpdatedAt),
	}
	if v.AccessGroupID != nil {
		out.AccessGroupID = ptr(IDFromInt(*v.AccessGroupID))
	}
	if v.LastActiveAt != nil {
		out.LastActiveAt = NullableInstant{Valid: true, Time: NewInstant(*v.LastActiveAt)}
	}
	return out
}
