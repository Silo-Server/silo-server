package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminAccessGroupService interface {
	GetAdminAccessGroup(context.Context, int64) (*access.Group, error)
	ListAdminAccessGroupsPage(context.Context, *access.GroupPageKey, int) ([]access.Group, bool, error)
	CreateAdminAccessGroup(context.Context, access.CreateGroupInput) (*access.Group, error)
	UpdateAdminAccessGroup(context.Context, int64, access.UpdateGroupInput, access.GroupPrecondition) (*access.Group, error)
	DeleteAdminAccessGroup(context.Context, int64, access.GroupPrecondition) error
}

// AdminAccessGroup is the revision-tracked editor projection. Member counts
// are list decorations and do not participate in its validator.
type AdminAccessGroup struct {
	ID                         ID       `json:"id"`
	Name                       string   `json:"name"`
	Description                string   `json:"description"`
	LibraryIDs                 []ID     `json:"library_ids" nullable:"true"`
	MaxPlaybackQuality         string   `json:"max_playback_quality"`
	DownloadAllowed            bool     `json:"download_allowed"`
	DownloadTranscodeAllowed   bool     `json:"download_transcode_allowed"`
	TranscodeAllowed           bool     `json:"transcode_allowed"`
	AudioTranscodeAllowed      bool     `json:"audio_transcode_allowed"`
	MaxStreams                 int      `json:"max_streams"`
	MaxTranscodes              int      `json:"max_transcodes"`
	MaxRemoteStreamBitrateKbps int      `json:"max_remote_stream_bitrate_kbps" minimum:"0" doc:"Remote per-stream bitrate ceiling in kbps; 0 means unlimited"`
	MaxLocalStreamBitrateKbps  int      `json:"max_local_stream_bitrate_kbps" minimum:"0" doc:"Local per-stream bitrate ceiling in kbps; 0 means unlimited"`
	AllowedPermissions         []string `json:"allowed_permissions" nullable:"true"`
	RequestsAllowed            bool     `json:"requests_allowed"`
	IsDefault                  bool     `json:"is_default"`
	CreatedAt                  Instant  `json:"created_at"`
	UpdatedAt                  Instant  `json:"updated_at"`
}
type AdminAccessGroupListItem struct {
	AdminAccessGroup
	MemberCount int `json:"member_count"`
}
type AdminAccessGroupListOutput struct {
	Body Collection[AdminAccessGroupListItem]
}
type AdminAccessGroupOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   AdminAccessGroup
}
type AdminAccessGroupCreatedOutput struct {
	Location string `header:"Location"`
	Body     AdminAccessGroup
}
type AdminAccessGroupIDInput struct {
	ID          ID     `path:"id" pattern:"^[1-9][0-9]*$"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}

// Nullable arrays retain the existing unrestricted/default policy semantics.
type AdminAccessGroupBody struct {
	Name                       *string   `json:"name,omitempty" minLength:"1"`
	Description                *string   `json:"description,omitempty"`
	LibraryIDs                 *[]ID     `json:"library_ids,omitempty" nullable:"true"`
	MaxPlaybackQuality         *string   `json:"max_playback_quality,omitempty"`
	DownloadAllowed            *bool     `json:"download_allowed,omitempty"`
	DownloadTranscodeAllowed   *bool     `json:"download_transcode_allowed,omitempty"`
	TranscodeAllowed           *bool     `json:"transcode_allowed,omitempty"`
	AudioTranscodeAllowed      *bool     `json:"audio_transcode_allowed,omitempty"`
	MaxStreams                 *int      `json:"max_streams,omitempty" minimum:"0"`
	MaxTranscodes              *int      `json:"max_transcodes,omitempty" minimum:"0"`
	MaxRemoteStreamBitrateKbps *int      `json:"max_remote_stream_bitrate_kbps,omitempty" minimum:"0"`
	MaxLocalStreamBitrateKbps  *int      `json:"max_local_stream_bitrate_kbps,omitempty" minimum:"0"`
	AllowedPermissions         *[]string `json:"allowed_permissions,omitempty" nullable:"true"`
	RequestsAllowed            *bool     `json:"requests_allowed,omitempty"`
	IsDefault                  *bool     `json:"is_default,omitempty"`
}
type AdminAccessGroupCreateInput struct {
	RawBody []byte
	Body    AdminAccessGroupBody
}
type AdminAccessGroupUpdateInput struct {
	RawBody     []byte
	ID          ID     `path:"id" pattern:"^[1-9][0-9]*$"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminAccessGroupBody
}

func adminAccessGroupOf(g *access.Group) AdminAccessGroup {
	var ids []ID
	if g.LibraryIDs != nil {
		ids = make([]ID, 0, len(g.LibraryIDs))
		for _, id := range g.LibraryIDs {
			ids = append(ids, IDFromInt(int64(id)))
		}
	}
	return AdminAccessGroup{ID: IDFromInt(g.ID), Name: g.Name, Description: g.Description, LibraryIDs: ids, MaxPlaybackQuality: g.MaxPlaybackQuality, DownloadAllowed: g.DownloadAllowed, DownloadTranscodeAllowed: g.DownloadTranscodeAllowed, TranscodeAllowed: g.TranscodeAllowed, AudioTranscodeAllowed: g.AudioTranscodeAllowed, MaxStreams: g.MaxStreams, MaxTranscodes: g.MaxTranscodes, MaxRemoteStreamBitrateKbps: g.MaxRemoteStreamBitrateKbps, MaxLocalStreamBitrateKbps: g.MaxLocalStreamBitrateKbps, AllowedPermissions: g.AllowedPermissions, RequestsAllowed: g.RequestsAllowed, IsDefault: g.IsDefault, CreatedAt: NewInstant(g.CreatedAt), UpdatedAt: NewInstant(g.UpdatedAt)}
}
func groupTag(ctx context.Context, g *access.Group) EntityTag {
	return RenderETag("admin-access-groups/"+strconv.Itoa(claimsFrom(ctx).UserID)+"/"+profileFrom(ctx), strconv.FormatInt(g.ID, 10), g.Revision)
}
func groupProblem(ctx context.Context, err error) *Problem {
	if conflict, ok := errors.AsType[*access.GroupRevisionConflict](err); ok && conflict.Current != nil {
		return NewProblem(TypePreconditionFailed, "The access group changed; reload before saving.").WithHeader("ETag", groupTag(ctx, conflict.Current).String())
	}
	switch {
	case errors.Is(err, access.ErrGroupNotFound):
		return NewProblem(TypeNotFound, "Access group not found.")
	case errors.Is(err, access.ErrGroupDuplicate):
		return NewProblem(TypeConflict, "An access group already uses this name.")
	case errors.Is(err, access.ErrDefaultGroupRequired):
		return NewProblem(TypeConflict, "Make another group the default before removing this default.")
	case errors.Is(err, handlers.ErrInvalidAccessGroup):
		return NewProblem(TypeValidationFailed, "Invalid access group configuration.")
	case errors.Is(err, handlers.ErrAccessGroupUnavailable):
		return NewProblem(TypeDependencyUnavailable, "Access groups are unavailable.")
	default:
		return NewProblem(TypeInternalError, "Unable to manage access group.")
	}
}
func groupGuard(ctx context.Context, match, none string, g *access.Group) (access.GroupPrecondition, *Problem) {
	if p := EvaluateGuardedPreconditions(match, none, groupTag(ctx, g)); p != nil {
		return access.GroupPrecondition{}, p
	}
	if strings.TrimSpace(match) == "*" {
		return access.GroupPrecondition{Any: true}, nil
	}
	return access.GroupPrecondition{Revision: g.Revision}, nil
}
func (reg *Registry) accessGroups() (AdminAccessGroupService, *Problem) {
	if reg.deps.AdminAccessGroups == nil {
		return nil, NewProblem(TypeDependencyUnavailable, "Access groups are unavailable.")
	}
	return reg.deps.AdminAccessGroups, nil
}

const opListAdminAccessGroups = "listAdminAccessGroups"

func registerAdminAccessGroups(reg *Registry) {
	op := func(method, path, id string, guard bool) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "admin", "Manage access groups."), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, Guarded: guard}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, op(http.MethodGet, "/admin/access-groups", opListAdminAccessGroups, false), func(ctx context.Context, in *CursorListInput) (*AdminAccessGroupListOutput, error) {
		svc, p := reg.accessGroups()
		if p != nil {
			return nil, p
		}
		scope := CursorScope{OperationID: opListAdminAccessGroups, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: "v1/limit=" + strconv.Itoa(in.Limit), Sort: "id", Tiebreaker: "id"}
		var after *access.GroupPageKey
		if in.Cursor != "" {
			after = new(access.GroupPageKey)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
			if after.ID <= 0 {
				return nil, NewProblem(TypeInvalidCursor, "Invalid access group cursor.")
			}
		}
		rows, more, err := svc.ListAdminAccessGroupsPage(ctx, after, in.Limit)
		if err != nil {
			return nil, groupProblem(ctx, err)
		}
		items := make([]AdminAccessGroupListItem, 0, len(rows))
		for _, g := range rows {
			items = append(items, AdminAccessGroupListItem{AdminAccessGroup: adminAccessGroupOf(&g), MemberCount: g.MemberCount})
		}
		next := ""
		if more {
			if len(rows) == 0 {
				return nil, NewProblem(TypeInternalError, "Unable to page access groups.")
			}
			next, err = cursors.Encode(scope, access.GroupPageKey{ID: rows[len(rows)-1].ID})
			if err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
			}
		}
		return &AdminAccessGroupListOutput{Body: Paginated(items, next)}, nil
	})
	get := op(http.MethodGet, "/admin/access-groups/{id}", "getAdminAccessGroup", false)
	get.Conditional = true
	Register(reg, get, func(ctx context.Context, in *AdminAccessGroupIDInput) (*AdminAccessGroupOutput, error) {
		svc, p := reg.accessGroups()
		if p != nil {
			return nil, p
		}
		id, p := adminGroupID(in.ID)
		if p != nil {
			return nil, p
		}
		g, err := svc.GetAdminAccessGroup(ctx, id)
		if err != nil {
			return nil, groupProblem(ctx, err)
		}
		tag := groupTag(ctx, g)
		out := &AdminAccessGroupOutput{ETag: tag.String(), Body: adminAccessGroupOf(g)}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	create := op(http.MethodPost, "/admin/access-groups", "createAdminAccessGroup", false)
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, func(ctx context.Context, in *AdminAccessGroupCreateInput) (*AdminAccessGroupCreatedOutput, error) {
		svc, p := reg.accessGroups()
		if p != nil {
			return nil, p
		}
		update, p := groupInput(in.Body, in.RawBody)
		if p != nil {
			return nil, p
		}
		if update.Name == nil {
			return nil, NewProblem(TypeValidationFailed, "Access group name is required.")
		}
		body := access.CreateGroupInput{Name: *update.Name, DownloadAllowed: true, DownloadTranscodeAllowed: true, TranscodeAllowed: true, AudioTranscodeAllowed: true, RequestsAllowed: true}
		applyGroupCreate(&body, update)
		g, err := svc.CreateAdminAccessGroup(ctx, body)
		if err != nil {
			return nil, groupProblem(ctx, err)
		}
		return &AdminAccessGroupCreatedOutput{Location: Prefix + "/admin/access-groups/" + strconv.FormatInt(g.ID, 10), Body: adminAccessGroupOf(g)}, nil
	})
	Register(reg, op(http.MethodPut, "/admin/access-groups/{id}", "updateAdminAccessGroup", true), func(ctx context.Context, in *AdminAccessGroupUpdateInput) (*AdminAccessGroupOutput, error) {
		svc, p := reg.accessGroups()
		if p != nil {
			return nil, p
		}
		id, p := adminGroupID(in.ID)
		if p != nil {
			return nil, p
		}
		body, p := groupInput(in.Body, in.RawBody)
		if p != nil {
			return nil, p
		}
		g, err := svc.GetAdminAccessGroup(ctx, id)
		if err != nil {
			return nil, groupProblem(ctx, err)
		}
		guard, p := groupGuard(ctx, in.IfMatch, in.IfNoneMatch, g)
		if p != nil {
			return nil, p
		}
		g, err = svc.UpdateAdminAccessGroup(ctx, id, body, guard)
		if err != nil {
			return nil, groupProblem(ctx, err)
		}
		return &AdminAccessGroupOutput{ETag: groupTag(ctx, g).String(), Body: adminAccessGroupOf(g)}, nil
	})
	Register(reg, op(http.MethodDelete, "/admin/access-groups/{id}", "deleteAdminAccessGroup", true), func(ctx context.Context, in *AdminAccessGroupIDInput) (*struct{}, error) {
		svc, p := reg.accessGroups()
		if p != nil {
			return nil, p
		}
		id, p := adminGroupID(in.ID)
		if p != nil {
			return nil, p
		}
		g, err := svc.GetAdminAccessGroup(ctx, id)
		if err != nil {
			return nil, groupProblem(ctx, err)
		}
		guard, p := groupGuard(ctx, in.IfMatch, in.IfNoneMatch, g)
		if p != nil {
			return nil, p
		}
		if err = svc.DeleteAdminAccessGroup(ctx, id, guard); err != nil {
			return nil, groupProblem(ctx, err)
		}
		return nil, nil
	})
}
