package apiv2

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

const adminActionApprove = "approve"
const adminActionDecline = "decline"
const adminActionCancel = "cancel"
const adminActionRetry = "retry"
const adminRequestSort = "-created_at"
const opGetAdminRequestCapabilities = "getAdminRequestCapabilities"
const opAdminApproveRequest = "adminApproveRequest"
const opAdminDeclineRequest = "adminDeclineRequest"
const opAdminCancelRequest = "adminCancelRequest"
const opAdminRetryRequest = "adminRetryRequest"
const opGetAdminRequestSettings = "getAdminRequestSettings"
const opUpdateAdminRequestSettings = "updateAdminRequestSettings"
const opGetAdminRequestUserLimit = "getAdminRequestUserLimit"
const opUpdateAdminRequestUserLimit = "updateAdminRequestUserLimit"
const opGetRequestIntegration = "getRequestIntegration"
const opCreateRequestIntegration = "createRequestIntegration"
const opUpdateRequestIntegration = "updateRequestIntegration"
const opDeleteRequestIntegration = "deleteRequestIntegration"
const opLoadRequestIntegrationOptions = "loadRequestIntegrationOptions"

const opListAdminRequests = "listAdminRequests"
const opListRequestIntegrations = "listRequestIntegrations"

type AdminRequestPreconditions struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}

type guardedAdminRequests interface {
	GetIntegration(context.Context, mediarequests.Viewer, string) (*mediarequests.Integration, error)
	UpdateSettingsConditional(context.Context, mediarequests.Viewer, mediarequests.Settings, int64) (mediarequests.Settings, error)
	UpsertUserLimitConditional(context.Context, mediarequests.Viewer, mediarequests.UserLimit, int64) (*mediarequests.UserLimit, error)
	UpdateIntegrationConditional(context.Context, mediarequests.Viewer, mediarequests.Integration, int64) (*mediarequests.Integration, error)
	DeleteIntegrationConditional(context.Context, mediarequests.Viewer, string, int64) error
}

type AdminRequestSettings struct {
	RequestsEnabled           bool `json:"requests_enabled"`
	GlobalMaxRequests         int  `json:"global_max_requests" minimum:"0"`
	GlobalWindowDays          int  `json:"global_window_days" minimum:"1"`
	GlobalAutoApprovalEnabled bool `json:"global_auto_approval_enabled"`
	ForceDualQuality          bool `json:"force_dual_quality"`
}
type AdminRequestSettingsOutput struct {
	ETag string `header:"ETag"`
	Body AdminRequestSettings
}
type AdminRequestSettingsInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminRequestSettings
}

type AdminRequestUserLimit struct {
	UserID       ID     `json:"user_id"`
	LimitMode    string `json:"limit_mode" enum:"inherit,custom,unlimited,blocked"`
	MaxRequests  *int   `json:"max_requests" nullable:"true" minimum:"0"`
	WindowDays   *int   `json:"window_days" nullable:"true" minimum:"1"`
	ApprovalMode string `json:"approval_mode" enum:"inherit,manual,auto,blocked"`
}
type AdminRequestLimitBody struct {
	LimitMode    string `json:"limit_mode" enum:"inherit,custom,unlimited,blocked"`
	MaxRequests  *int   `json:"max_requests" nullable:"true" minimum:"0"`
	WindowDays   *int   `json:"window_days" nullable:"true" minimum:"1"`
	ApprovalMode string `json:"approval_mode" enum:"inherit,manual,auto,blocked"`
}
type AdminRequestUserInput struct {
	UserID ID `path:"user_id" pattern:"^[1-9][0-9]*$"`
}
type AdminRequestLimitInput struct {
	AdminRequestUserInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminRequestLimitBody
}
type AdminRequestLimitOutput struct {
	ETag string `header:"ETag"`
	Body AdminRequestUserLimit
}

type AdminRequestIntegrationBody struct {
	Name                string         `json:"name" minLength:"1"`
	CapabilityID        string         `json:"capability_id" minLength:"1"`
	InstallationID      ID             `json:"installation_id" pattern:"^[1-9][0-9]*$"`
	SupportedMediaTypes []string       `json:"supported_media_types"`
	PluginConfig        map[string]any `json:"plugin_config"`
	Enabled             bool           `json:"enabled"`
	BaseURL             string         `json:"base_url"`
	APIKey              string         `json:"api_key_ref,omitempty" writeOnly:"true" doc:"New credential; omitted or blank preserves the saved credential on update"`
}
type AdminRequestIntegration struct {
	ID                  ID              `json:"id"`
	Name                string          `json:"name"`
	CapabilityID        string          `json:"capability_id"`
	InstallationID      *ID             `json:"installation_id" nullable:"true"`
	SupportedMediaTypes []string        `json:"supported_media_types"`
	PluginConfig        map[string]any  `json:"plugin_config"`
	Enabled             bool            `json:"enabled"`
	BaseURL             string          `json:"base_url"`
	HasAPIKey           bool            `json:"has_api_key"`
	LastCheckAt         NullableInstant `json:"last_check_at"`
	LastCheckStatus     string          `json:"last_check_status"`
	LastCheckError      string          `json:"last_check_error"`
	UpdatedAt           Instant         `json:"updated_at"`
}
type AdminRequestIntegrationOutput struct {
	ETag string `header:"ETag"`
	Body AdminRequestIntegration
}
type AdminRequestIntegrationCreateOutput struct {
	Location string `header:"Location"`
	ETag     string `header:"ETag"`
	Body     AdminRequestIntegration
}
type AdminRequestIntegrationCreateInput struct{ Body AdminRequestIntegrationBody }
type AdminRequestIntegrationInput struct {
	MediaRequestGetInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminRequestIntegrationBody
}
type AdminRequestIntegrationDeleteInput struct {
	MediaRequestGetInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminRequestIntegrationCollectionOutput struct {
	Body Collection[AdminRequestIntegration]
}
type AdminRequestOptionsInput struct {
	MediaRequestGetInput
	Body struct {
		Name           string         `json:"name,omitempty"`
		CapabilityID   string         `json:"capability_id,omitempty"`
		InstallationID *ID            `json:"installation_id,omitempty" pattern:"^[1-9][0-9]*$"`
		BaseURL        string         `json:"base_url,omitempty"`
		APIKey         string         `json:"api_key_ref,omitempty" writeOnly:"true"`
		PluginConfig   map[string]any `json:"plugin_config,omitempty"`
	}
}
type AdminRequestOptions struct {
	Options map[string][]mediarequests.RouterOption `json:"options"`
}
type AdminRequestOptionsOutput struct{ Body AdminRequestOptions }
type AdminRequestActionInput struct {
	MediaRequestGetInput
	Body struct {
		Reason string `json:"reason,omitempty"`
	}
}
type AdminRequestCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminRequestCapabilitiesOutputBody
}

type AdminRequestCapabilitiesOutputBody struct {
	Capability
	Available            bool `json:"available"`
	GuardedConfiguration bool `json:"guarded_configuration"`
}

func adminRequestViewer(ctx context.Context) mediarequests.Viewer {
	return mediarequests.Viewer{UserID: claimsFrom(ctx).UserID, ProfileID: profileFrom(ctx), IsAdmin: true}
}
func (reg *Registry) adminRequestService() (handlers.RequestService, *Problem) {
	if reg.deps.AdminRequests == nil {
		return nil, unavailable("request administration")
	}
	return reg.deps.AdminRequests, nil
}
func (reg *Registry) guardedAdminRequestService() (guardedAdminRequests, *Problem) {
	s, ok := reg.deps.AdminRequests.(guardedAdminRequests)
	if !ok {
		return nil, unavailable("guarded request configuration")
	}
	return s, nil
}
func adminRequestTag(ctx context.Context, kind, id string, revision int64) EntityTag {
	return RenderETag("admin-requests/"+strconv.Itoa(claimsFrom(ctx).UserID)+"/"+profileFrom(ctx)+"/"+kind, id, revision)
}
func adminRequestGuard(headers AdminRequestPreconditions, tag EntityTag, revision int64) (int64, *Problem) {
	if p := EvaluateGuardedPreconditions(headers.IfMatch, headers.IfNoneMatch, tag); p != nil {
		return 0, p
	}
	if headers.IfMatch == "*" {
		return -1, nil
	}
	return revision, nil
}
func registerAdminRequests(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := func(method, path, id string, guard bool) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "admin", "Manage media requests and their configuration."), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, Guarded: guard}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	Register(reg, op(http.MethodGet, "/admin/requests/capabilities", opGetAdminRequestCapabilities, false), func(_ context.Context, _ *CapabilityInput) (*AdminRequestCapabilitiesOutput, error) {
		out := new(AdminRequestCapabilitiesOutput)
		out.Body.Available = reg.deps.AdminRequests != nil
		_, out.Body.GuardedConfiguration = reg.deps.AdminRequests.(guardedAdminRequests)
		return out, nil
	})
	Register(reg, op(http.MethodGet, "/admin/requests", opListAdminRequests, false), func(ctx context.Context, in *MediaRequestListInput) (*MediaRequestCollectionOutput, error) {
		return reg.listAdminRequests(ctx, cursors, in)
	})
	for _, action := range []string{adminActionApprove, adminActionDecline, adminActionCancel, adminActionRetry} {
		Register(reg, op(http.MethodPost, "/admin/requests/{id}/"+action, "admin"+strings.ToUpper(action[:1])+action[1:]+"Request", false), func(ctx context.Context, in *AdminRequestActionInput) (*MediaRequestOutput, error) {
			s, p := reg.adminRequestService()
			if p != nil {
				return nil, p
			}
			v := adminRequestViewer(ctx)
			var r *mediarequests.Request
			var err error
			switch action {
			case adminActionApprove:
				r, err = s.Approve(ctx, v, string(in.ID))
			case adminActionDecline:
				r, err = s.Decline(ctx, v, string(in.ID), in.Body.Reason)
			case adminActionCancel:
				r, err = s.Cancel(ctx, v, string(in.ID), in.Body.Reason)
			case adminActionRetry:
				r, err = s.Retry(ctx, v, string(in.ID))
			}
			if err != nil {
				return nil, requestProblem(err)
			}
			return &MediaRequestOutput{Body: mediaRequestOf(r)}, nil
		})
	}
	Register(reg, op(http.MethodGet, "/admin/request-settings", opGetAdminRequestSettings, false), reg.getAdminRequestSettings)
	Register(reg, op(http.MethodPut, "/admin/request-settings", opUpdateAdminRequestSettings, true), reg.updateAdminRequestSettings)
	Register(reg, op(http.MethodGet, "/admin/request-users/{user_id}/limit", opGetAdminRequestUserLimit, false), reg.getAdminRequestUserLimit)
	Register(reg, op(http.MethodPut, "/admin/request-users/{user_id}/limit", opUpdateAdminRequestUserLimit, true), reg.updateAdminRequestUserLimit)
	Register(reg, op(http.MethodGet, "/admin/request-integrations", opListRequestIntegrations, false), func(ctx context.Context, in *CursorListInput) (*AdminRequestIntegrationCollectionOutput, error) {
		return reg.listAdminRequestIntegrations(ctx, cursors, in)
	})
	Register(reg, op(http.MethodGet, "/admin/request-integrations/{id}", opGetRequestIntegration, false), reg.getAdminRequestIntegration)
	create := op(http.MethodPost, "/admin/request-integrations", opCreateRequestIntegration, false)
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, reg.createAdminRequestIntegration)
	Register(reg, op(http.MethodPut, "/admin/request-integrations/{id}", opUpdateRequestIntegration, true), reg.updateAdminRequestIntegration)
	Register(reg, op(http.MethodDelete, "/admin/request-integrations/{id}", opDeleteRequestIntegration, true), reg.deleteAdminRequestIntegration)
	Register(reg, op(http.MethodPost, "/admin/request-integrations/{id}/options", opLoadRequestIntegrationOptions, false), reg.loadAdminRequestOptions)
}

func (reg *Registry) listAdminRequests(ctx context.Context, cursors *Cursors, in *MediaRequestListInput) (*MediaRequestCollectionOutput, error) {
	s, p := reg.adminRequestService()
	if p != nil {
		return nil, p
	}
	v := adminRequestViewer(ctx)
	scope := CursorScope{OperationID: opListAdminRequests, Security: strconv.Itoa(v.UserID) + "/" + v.ProfileID, Filter: in.Status + "|" + in.Outcome, Sort: adminRequestSort, Tiebreaker: "id"}
	var before *mediarequests.RequestPageKey
	if in.Cursor != "" {
		before = new(mediarequests.RequestPageKey)
		if p := cursors.Decode(scope, in.Cursor, before); p != nil {
			return nil, p
		}
		if before.ID == "" || before.CreatedAt.IsZero() {
			return nil, NewProblem(TypeInvalidCursor, "The cursor position is invalid.")
		}
	}
	rows, err := s.ListAdmin(ctx, v, mediarequests.ListFilter{Status: mediarequests.Status(in.Status), Outcome: mediarequests.Outcome(in.Outcome), Limit: in.Limit + 1, Before: before})
	if err != nil {
		return nil, requestProblem(err)
	}
	next := ""
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		last := rows[len(rows)-1]
		next, err = cursors.Encode(scope, mediarequests.RequestPageKey{ID: last.ID, CreatedAt: last.CreatedAt})
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
		}
	}
	items := make([]MediaRequest, 0, len(rows))
	for _, r := range rows {
		items = append(items, mediaRequestOf(r))
	}
	return &MediaRequestCollectionOutput{Body: MediaRequestCollection{Collection: Paginated(items, next)}}, nil
}
func adminSettingsOf(s mediarequests.Settings) AdminRequestSettings {
	return AdminRequestSettings{s.RequestsEnabled, s.GlobalMaxRequests, s.GlobalWindowDays, s.GlobalAutoApprovalEnabled, s.ForceDualQuality}
}
func (b AdminRequestSettings) domain() mediarequests.Settings {
	return mediarequests.Settings{RequestsEnabled: b.RequestsEnabled, GlobalMaxRequests: b.GlobalMaxRequests, GlobalWindowDays: b.GlobalWindowDays, GlobalAutoApprovalEnabled: b.GlobalAutoApprovalEnabled, ForceDualQuality: b.ForceDualQuality}
}
func (reg *Registry) getAdminRequestSettings(ctx context.Context, _ *struct{}) (*AdminRequestSettingsOutput, error) {
	s, p := reg.adminRequestService()
	if p != nil {
		return nil, p
	}
	r, err := s.GetSettings(ctx, adminRequestViewer(ctx))
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestSettingsOutput{ETag: adminRequestTag(ctx, "settings", "global", r.Revision).String(), Body: adminSettingsOf(r)}, nil
}
func (reg *Registry) updateAdminRequestSettings(ctx context.Context, in *AdminRequestSettingsInput) (*AdminRequestSettingsOutput, error) {
	s, p := reg.adminRequestService()
	if p != nil {
		return nil, p
	}
	g, p := reg.guardedAdminRequestService()
	if p != nil {
		return nil, p
	}
	v := adminRequestViewer(ctx)
	r, err := s.GetSettings(ctx, v)
	if err != nil {
		return nil, requestProblem(err)
	}
	rev, p := adminRequestGuard(AdminRequestPreconditions{in.IfMatch, in.IfNoneMatch}, adminRequestTag(ctx, "settings", "global", r.Revision), r.Revision)
	if p != nil {
		return nil, p
	}
	r, err = g.UpdateSettingsConditional(ctx, v, in.Body.domain(), rev)
	if errors.Is(err, mediarequests.ErrStaleRevision) {
		current, e := reg.getAdminRequestSettings(ctx, &struct{}{})
		if e != nil {
			return nil, e
		}
		return nil, NewProblem(TypePreconditionFailed, "Request settings changed; reload before saving.").WithHeader("ETag", current.ETag)
	}
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestSettingsOutput{ETag: adminRequestTag(ctx, "settings", "global", r.Revision).String(), Body: adminSettingsOf(r)}, nil
}
func adminLimitOf(r *mediarequests.UserLimit) AdminRequestUserLimit {
	return AdminRequestUserLimit{ID(strconv.Itoa(r.UserID)), string(r.LimitMode), r.MaxRequests, r.WindowDays, string(r.ApprovalMode)}
}
func (reg *Registry) getAdminRequestUserLimit(ctx context.Context, in *AdminRequestUserInput) (*AdminRequestLimitOutput, error) {
	s, p := reg.adminRequestService()
	if p != nil {
		return nil, p
	}
	id, err := strconv.Atoi(string(in.UserID))
	if err != nil || id <= 0 {
		return nil, NewProblem(TypeValidationFailed, "Invalid account ID.")
	}
	r, err := s.GetUserLimit(ctx, adminRequestViewer(ctx), id)
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestLimitOutput{ETag: adminRequestTag(ctx, "limit", string(in.UserID), r.Revision).String(), Body: adminLimitOf(r)}, nil
}
func (reg *Registry) updateAdminRequestUserLimit(ctx context.Context, in *AdminRequestLimitInput) (*AdminRequestLimitOutput, error) {
	s, p := reg.adminRequestService()
	if p != nil {
		return nil, p
	}
	g, p := reg.guardedAdminRequestService()
	if p != nil {
		return nil, p
	}
	id, err := strconv.Atoi(string(in.UserID))
	if err != nil || id <= 0 {
		return nil, NewProblem(TypeValidationFailed, "Invalid account ID.")
	}
	v := adminRequestViewer(ctx)
	r, err := s.GetUserLimit(ctx, v, id)
	if err != nil {
		return nil, requestProblem(err)
	}
	rev, p := adminRequestGuard(AdminRequestPreconditions{in.IfMatch, in.IfNoneMatch}, adminRequestTag(ctx, "limit", string(in.UserID), r.Revision), r.Revision)
	if p != nil {
		return nil, p
	}
	b := in.Body
	r, err = g.UpsertUserLimitConditional(ctx, v, mediarequests.UserLimit{UserID: id, LimitMode: mediarequests.LimitMode(b.LimitMode), MaxRequests: b.MaxRequests, WindowDays: b.WindowDays, ApprovalMode: mediarequests.ApprovalMode(b.ApprovalMode)}, rev)
	if errors.Is(err, mediarequests.ErrStaleRevision) {
		current, e := reg.getAdminRequestUserLimit(ctx, &in.AdminRequestUserInput)
		if e != nil {
			return nil, e
		}
		return nil, NewProblem(TypePreconditionFailed, "The account limit changed; reload before saving.").WithHeader("ETag", current.ETag)
	}
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestLimitOutput{ETag: adminRequestTag(ctx, "limit", string(in.UserID), r.Revision).String(), Body: adminLimitOf(r)}, nil
}
func adminIntegrationOf(r mediarequests.Integration) AdminRequestIntegration {
	var install *ID
	if r.InstallationID != nil {
		install = new(ID(strconv.Itoa(*r.InstallationID)))
	}
	types := r.SupportedMediaTypes
	if types == nil {
		types = []string{}
	}
	config := r.PluginConfig
	if config == nil {
		config = map[string]any{}
	}
	var checked NullableInstant
	if r.LastCheckAt != nil {
		checked = NullableInstant{Time: NewInstant(*r.LastCheckAt), Valid: true}
	}
	return AdminRequestIntegration{ID: ID(r.ID), Name: r.Name, CapabilityID: r.CapabilityID, InstallationID: install, SupportedMediaTypes: types, PluginConfig: config, Enabled: r.Enabled, BaseURL: r.BaseURL, HasAPIKey: strings.TrimSpace(r.APIKeyRef) != "", LastCheckAt: checked, LastCheckStatus: r.LastCheckStatus, LastCheckError: r.LastCheckError, UpdatedAt: NewInstant(r.UpdatedAt)}
}
func (b AdminRequestIntegrationBody) domain() (mediarequests.Integration, *Problem) {
	id, err := strconv.Atoi(string(b.InstallationID))
	if err != nil || id <= 0 {
		return mediarequests.Integration{}, NewProblem(TypeValidationFailed, "Invalid installation ID.")
	}
	return mediarequests.Integration{Name: b.Name, CapabilityID: b.CapabilityID, InstallationID: &id, SupportedMediaTypes: b.SupportedMediaTypes, PluginConfig: b.PluginConfig, Enabled: b.Enabled, BaseURL: b.BaseURL, APIKeyRef: b.APIKey}, nil
}
func (reg *Registry) listAdminRequestIntegrations(ctx context.Context, cursors *Cursors, in *CursorListInput) (*AdminRequestIntegrationCollectionOutput, error) {
	s, p := reg.adminRequestService()
	if p != nil {
		return nil, p
	}
	v := adminRequestViewer(ctx)
	scope := CursorScope{OperationID: opListRequestIntegrations, Security: strconv.Itoa(v.UserID) + "/" + v.ProfileID, Sort: "id", Tiebreaker: "id"}
	var after string
	if in.Cursor != "" {
		if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
			return nil, p
		}
		if after == "" {
			return nil, NewProblem(TypeInvalidCursor, "Invalid integration cursor.")
		}
	}
	rows, err := s.ListIntegrations(ctx, v)
	if err != nil {
		return nil, requestProblem(err)
	}
	slices.SortFunc(rows, func(a, b mediarequests.Integration) int { return cmp.Compare(a.ID, b.ID) })
	items := make([]AdminRequestIntegration, 0, in.Limit)
	next := ""
	for _, r := range rows {
		if r.ID <= after {
			continue
		}
		if len(items) == in.Limit {
			next, err = cursors.Encode(scope, string(items[len(items)-1].ID))
			if err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
			}
			break
		}
		items = append(items, adminIntegrationOf(r))
	}
	return &AdminRequestIntegrationCollectionOutput{Body: Paginated(items, next)}, nil
}
func (reg *Registry) getAdminRequestIntegration(ctx context.Context, in *MediaRequestGetInput) (*AdminRequestIntegrationOutput, error) {
	g, p := reg.guardedAdminRequestService()
	if p != nil {
		return nil, p
	}
	r, err := g.GetIntegration(ctx, adminRequestViewer(ctx), string(in.ID))
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestIntegrationOutput{ETag: adminRequestTag(ctx, "integration", r.ID, r.Revision).String(), Body: adminIntegrationOf(*r)}, nil
}
func (reg *Registry) createAdminRequestIntegration(ctx context.Context, in *AdminRequestIntegrationCreateInput) (*AdminRequestIntegrationCreateOutput, error) {
	s, p := reg.adminRequestService()
	if p != nil {
		return nil, p
	}
	b, p := in.Body.domain()
	if p != nil {
		return nil, p
	}
	r, err := s.CreateIntegration(ctx, adminRequestViewer(ctx), b)
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestIntegrationCreateOutput{Location: Prefix + "/admin/request-integrations/" + r.ID, ETag: adminRequestTag(ctx, "integration", r.ID, r.Revision).String(), Body: adminIntegrationOf(*r)}, nil
}
func (reg *Registry) updateAdminRequestIntegration(ctx context.Context, in *AdminRequestIntegrationInput) (*AdminRequestIntegrationOutput, error) {
	g, p := reg.guardedAdminRequestService()
	if p != nil {
		return nil, p
	}
	v := adminRequestViewer(ctx)
	r, err := g.GetIntegration(ctx, v, string(in.ID))
	if err != nil {
		return nil, requestProblem(err)
	}
	rev, p := adminRequestGuard(AdminRequestPreconditions{in.IfMatch, in.IfNoneMatch}, adminRequestTag(ctx, "integration", r.ID, r.Revision), r.Revision)
	if p != nil {
		return nil, p
	}
	b, p := in.Body.domain()
	if p != nil {
		return nil, p
	}
	b.ID = string(in.ID)
	r, err = g.UpdateIntegrationConditional(ctx, v, b, rev)
	if errors.Is(err, mediarequests.ErrStaleRevision) {
		current, e := reg.getAdminRequestIntegration(ctx, &in.MediaRequestGetInput)
		if e != nil {
			return nil, e
		}
		return nil, NewProblem(TypePreconditionFailed, "The integration changed; reload before saving.").WithHeader("ETag", current.ETag)
	}
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestIntegrationOutput{ETag: adminRequestTag(ctx, "integration", r.ID, r.Revision).String(), Body: adminIntegrationOf(*r)}, nil
}
func (reg *Registry) deleteAdminRequestIntegration(ctx context.Context, in *AdminRequestIntegrationDeleteInput) (*struct{}, error) {
	g, p := reg.guardedAdminRequestService()
	if p != nil {
		return nil, p
	}
	v := adminRequestViewer(ctx)
	r, err := g.GetIntegration(ctx, v, string(in.ID))
	if err != nil {
		return nil, requestProblem(err)
	}
	rev, p := adminRequestGuard(AdminRequestPreconditions{in.IfMatch, in.IfNoneMatch}, adminRequestTag(ctx, "integration", r.ID, r.Revision), r.Revision)
	if p != nil {
		return nil, p
	}
	err = g.DeleteIntegrationConditional(ctx, v, r.ID, rev)
	if errors.Is(err, mediarequests.ErrStaleRevision) {
		current, e := reg.getAdminRequestIntegration(ctx, &in.MediaRequestGetInput)
		if e != nil {
			return nil, e
		}
		return nil, NewProblem(TypePreconditionFailed, "The integration changed; reload before deleting.").WithHeader("ETag", current.ETag)
	}
	if err != nil {
		return nil, requestProblem(err)
	}
	return nil, nil
}
func (reg *Registry) loadAdminRequestOptions(ctx context.Context, in *AdminRequestOptionsInput) (*AdminRequestOptionsOutput, error) {
	s, p := reg.adminRequestService()
	if p != nil {
		return nil, p
	}
	b := in.Body
	var install *int
	if b.InstallationID != nil {
		id, e := strconv.Atoi(string(*b.InstallationID))
		if e != nil || id <= 0 {
			return nil, NewProblem(TypeValidationFailed, "Invalid installation ID.")
		}
		install = &id
	}
	options, err := s.LoadIntegrationOptions(ctx, adminRequestViewer(ctx), mediarequests.Integration{ID: string(in.ID), Name: b.Name, CapabilityID: b.CapabilityID, InstallationID: install, BaseURL: b.BaseURL, APIKeyRef: b.APIKey, PluginConfig: b.PluginConfig})
	if err != nil {
		return nil, requestProblem(err)
	}
	if options == nil {
		options = map[string][]mediarequests.RouterOption{}
	}
	return &AdminRequestOptionsOutput{Body: AdminRequestOptions{Options: options}}, nil
}

var adminRequestOperationIDs = []string{opGetAdminRequestCapabilities, opListAdminRequests, opAdminApproveRequest, opAdminDeclineRequest, opAdminCancelRequest, opAdminRetryRequest, opGetAdminRequestSettings, opUpdateAdminRequestSettings, opGetAdminRequestUserLimit, opUpdateAdminRequestUserLimit, opListRequestIntegrations, opGetRequestIntegration, opCreateRequestIntegration, opUpdateRequestIntegration, opDeleteRequestIntegration, opLoadRequestIntegrationOptions}

func (c AdminRequestCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
