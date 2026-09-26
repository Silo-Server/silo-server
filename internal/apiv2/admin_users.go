package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// The admin users domain: every login account as an administrator sees it.

// EffectivePolicy is an account's resolved access policy: its own overrides
// layered on its access group's values.
type EffectivePolicy struct {
	LibraryIDs                 []ID         `json:"library_ids" nullable:"true" doc:"Libraries the account may see; null means every library, empty means none" example:"[\"1\",\"2\"]"`
	MaxPlaybackQuality         string       `json:"max_playback_quality" doc:"Playback ceiling; empty means none" example:"1080p"`
	MaxStreams                 int          `json:"max_streams" doc:"Concurrent stream limit; 0 means unlimited" example:"2"`
	MaxTranscodes              int          `json:"max_transcodes" doc:"Concurrent transcode limit; 0 means unlimited" example:"0"`
	MaxRemoteStreamBitrateKbps int          `json:"max_remote_stream_bitrate_kbps" minimum:"0" doc:"Remote per-stream bitrate limit in kbps; 0 means unlimited" example:"0"`
	MaxLocalStreamBitrateKbps  int          `json:"max_local_stream_bitrate_kbps" minimum:"0" doc:"Local per-stream bitrate limit in kbps; 0 means unlimited" example:"0"`
	TranscodeAllowed           bool         `json:"transcode_allowed" example:"true"`
	AudioTranscodeAllowed      bool         `json:"audio_transcode_allowed" example:"false"`
	DownloadAllowed            bool         `json:"download_allowed" example:"true"`
	DownloadTranscodeAllowed   bool         `json:"download_transcode_allowed" example:"false"`
	RequestsAllowed            bool         `json:"requests_allowed" example:"false"`
	Permissions                []Permission `json:"permissions" doc:"Effective assignable permissions" example:"[\"marker_edit\"]"`
}

// AdminUser is one login account with its own policy overrides (null =
// inherit from the access group) and the effective result.
type AdminUser struct {
	ID                         ID              `json:"id" example:"1"`
	Username                   string          `json:"username" example:"alice"`
	Email                      string          `json:"email" doc:"Contact email; empty when none is set" example:"alice@example.test"`
	Role                       string          `json:"role" enum:"admin,user" example:"user"`
	Permissions                []Permission    `json:"permissions" doc:"Permissions assigned directly to the account" example:"[\"marker_edit\"]"`
	Enabled                    bool            `json:"enabled" example:"true"`
	LibraryIDs                 []ID            `json:"library_ids" nullable:"true" doc:"Explicit library allowlist; null inherits the group's, empty means none" example:"[\"1\",\"2\"]"`
	MaxPlaybackQuality         *string         `json:"max_playback_quality" nullable:"true" doc:"Playback ceiling override; null inherits, empty string means no ceiling" example:"1080p"`
	MaxStreams                 *int            `json:"max_streams" nullable:"true" doc:"Stream limit override; null inherits, 0 means unlimited" example:"2"`
	MaxTranscodes              *int            `json:"max_transcodes" nullable:"true" doc:"Transcode limit override; null inherits, 0 means unlimited" example:"0"`
	MaxRemoteStreamBitrateKbps *int            `json:"max_remote_stream_bitrate_kbps" nullable:"true" minimum:"0" doc:"Remote per-stream bitrate override in kbps; null inherits, 0 means unlimited" example:"0"`
	MaxLocalStreamBitrateKbps  *int            `json:"max_local_stream_bitrate_kbps" nullable:"true" minimum:"0" doc:"Local per-stream bitrate override in kbps; null inherits, 0 means unlimited" example:"0"`
	TranscodeAllowed           *bool           `json:"transcode_allowed" nullable:"true" doc:"Override; null inherits" example:"true"`
	AudioTranscodeAllowed      *bool           `json:"audio_transcode_allowed" nullable:"true" doc:"Override; null inherits" example:"false"`
	MaxProfiles                int             `json:"max_profiles" doc:"Household profile limit" example:"5"`
	DownloadAllowed            *bool           `json:"download_allowed" nullable:"true" doc:"Override; null inherits" example:"true"`
	DownloadTranscodeAllowed   *bool           `json:"download_transcode_allowed" nullable:"true" doc:"Override; null inherits" example:"false"`
	RequestsAllowed            *bool           `json:"requests_allowed" nullable:"true" doc:"Override; null inherits" example:"false"`
	AccessGroupID              *ID             `json:"access_group_id" nullable:"true" doc:"The access group the account belongs to; null when none" example:"2"`
	PasswordLogin              bool            `json:"password_login" doc:"Whether the account signs in with a local password. False when an external authentication provider manages its sign-in; password actions do not apply then" example:"true"`
	PasswordChangeRequired     bool            `json:"password_change_required" doc:"Whether the account holds a temporary password it must replace at its next sign-in" example:"false"`
	IsOwner                    bool            `json:"is_owner" doc:"Whether the account is the server Owner: the account created at first-run setup, or on servers set up before the Owner existed, the earliest-created enabled admin. Other admins cannot edit, reset the password of, impersonate or manage API keys for it, and it cannot be demoted, disabled or deleted" example:"false"`
	EffectivePolicy            EffectivePolicy `json:"effective_policy" doc:"The resolved policy the server enforces"`
	CreatedAt                  Instant         `json:"created_at" example:"2026-01-02T03:04:05.678Z"`
	UpdatedAt                  Instant         `json:"updated_at" example:"2026-01-02T03:04:05.678Z"`
	LastActiveAt               NullableInstant `json:"last_active_at" doc:"Most recent recorded activity; null when the account has none" example:"2026-01-02T03:04:05.678Z"`
}

// AdminUserListInput is the listAdminUsers query.
type AdminUserListInput struct {
	CursorListInput
	Identity string `query:"identity" minLength:"1" maxLength:"320" doc:"Exact case-insensitive match against username or email; whitespace is trimmed"`
}

// AdminUserCollectionOutput is the listAdminUsers response.
type AdminUserCollectionOutput struct {
	Body AdminUserCollection
}

// AdminUserCollection is the named envelope the contract carries.
type AdminUserCollection struct {
	Collection[AdminUser]
}

// adminUserPosition is the cursor payload: the id of the last account the
// previous page emitted. Ids are unique and only ever grow, so the next page
// resumes strictly after it with no repeats or gaps.
type adminUserPosition struct {
	ID int `json:"i"`
}

// opListAdminUsers is the operation id; the cursor scope is bound to it.
const opListAdminUsers = "listAdminUsers"

func registerAdminUsers(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/admin/users", opListAdminUsers, "admin",
			"List login accounts with their policy overrides and effective policy, in account id order."),
		Class: ClassActingAdmin,

		ServiceBacked: true,
	}, func(ctx context.Context, in *AdminUserListInput) (*AdminUserCollectionOutput, error) {
		return reg.listAdminUsers(ctx, cursors, in)
	})
}

// listAdminUsers answers from the same account listing v1 GET /admin/users
// uses, one keyset page at a time.
func (reg *Registry) listAdminUsers(ctx context.Context, cursors *Cursors, in *AdminUserListInput) (*AdminUserCollectionOutput, error) {
	if reg.deps.AdminUsers == nil {
		return nil, unavailable("administration")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	identity := ""
	if in.Identity != "" {
		identity = auth.NormalizeUsername(in.Identity)
		if identity == "" {
			return nil, NewProblem(TypeValidationFailed, "Identity must not be blank.")
		}
	}
	scope := CursorScope{
		OperationID: opListAdminUsers,
		// Every admin sees every account, so the cursor is bound to the
		// acting account only, not to a viewer policy.
		Security:   strconv.Itoa(claims.UserID),
		Filter:     identity,
		Sort:       "id",
		Tiebreaker: "id",
	}
	afterID := 0
	if in.Cursor != "" {
		var pos adminUserPosition
		if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
			return nil, p
		}
		afterID = pos.ID
	}
	views, hasMore, err := reg.deps.AdminUsers.ListAdminUsersPage(ctx, afterID, in.Limit, identity)
	if err != nil {
		return nil, serviceProblem(err)
	}
	next := ""
	if hasMore && len(views) > 0 {
		next, err = cursors.Encode(scope, adminUserPosition{ID: views[len(views)-1].ID})
		if err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	items := make([]AdminUser, 0, len(views))
	for i := range views {
		items = append(items, adminUserFromView(views[i]))
	}
	return &AdminUserCollectionOutput{Body: AdminUserCollection{Collection: Paginated(items, next)}}, nil
}

func adminUserFromView(v handlers.AdminUserView) AdminUser {
	out := AdminUser{
		ID:                         IDFromInt(int64(v.ID)),
		Username:                   v.Username,
		Email:                      v.Email,
		Role:                       roleOf(v.Role),
		Permissions:                permissionsOf(v.Permissions),
		Enabled:                    v.Enabled,
		LibraryIDs:                 idsOfInts(v.LibraryIDs),
		MaxPlaybackQuality:         v.MaxPlaybackQuality,
		MaxStreams:                 v.MaxStreams,
		MaxTranscodes:              v.MaxTranscodes,
		MaxRemoteStreamBitrateKbps: v.MaxRemoteStreamBitrateKbps,
		MaxLocalStreamBitrateKbps:  v.MaxLocalStreamBitrateKbps,
		TranscodeAllowed:           v.TranscodeAllowed,
		AudioTranscodeAllowed:      v.AudioTranscodeAllowed,
		MaxProfiles:                v.MaxProfiles,
		DownloadAllowed:            v.DownloadAllowed,
		DownloadTranscodeAllowed:   v.DownloadTranscodeAllowed,
		RequestsAllowed:            v.RequestsAllowed,
		PasswordLogin:              v.PasswordLogin,
		PasswordChangeRequired:     v.PasswordChangeRequired,
		IsOwner:                    v.IsOwner,
		EffectivePolicy: EffectivePolicy{
			LibraryIDs:                 idsOfInts(v.EffectivePolicy.LibraryIDs),
			MaxPlaybackQuality:         v.EffectivePolicy.MaxPlaybackQuality,
			MaxStreams:                 v.EffectivePolicy.MaxStreams,
			MaxTranscodes:              v.EffectivePolicy.MaxTranscodes,
			MaxRemoteStreamBitrateKbps: v.EffectivePolicy.MaxRemoteStreamBitrateKbps,
			MaxLocalStreamBitrateKbps:  v.EffectivePolicy.MaxLocalStreamBitrateKbps,
			TranscodeAllowed:           v.EffectivePolicy.TranscodeAllowed,
			AudioTranscodeAllowed:      v.EffectivePolicy.AudioTranscodeAllowed,
			DownloadAllowed:            v.EffectivePolicy.DownloadAllowed,
			DownloadTranscodeAllowed:   v.EffectivePolicy.DownloadTranscodeAllowed,
			RequestsAllowed:            v.EffectivePolicy.RequestsAllowed,
			Permissions:                permissionsOf(v.EffectivePolicy.Permissions),
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
