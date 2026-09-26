package apiv2

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/historyimport"
)

// AdminHistoryImportService keeps administrative configuration separate from
// account-owned import credentials and runs.
type AdminHistoryImportService interface {
	ListAdminRunsPage(context.Context, *int, *historyimport.RunKey, int) ([]historyimport.Run, bool, error)
	ListAdminSources(context.Context) ([]historyimport.Source, error)
	GetAdminSource(context.Context, int) (*historyimport.Source, error)
	CreateSource(context.Context, historyimport.CreateSourceInput) (*historyimport.Source, error)
	UpdateSourceConditional(context.Context, int, historyimport.UpdateSourceInput, int64) (*historyimport.Source, error)
	DeleteSourceConditional(context.Context, int, int64) error
	SetSourceAdminTokenConditional(context.Context, int, string, int64) (*historyimport.Source, error)
	ClearSourceAdminTokenConditional(context.Context, int, int64) (*historyimport.Source, error)
	ListMappings(context.Context, int) ([]historyimport.UserMapping, error)
	GetMapping(context.Context, int) (*historyimport.UserMapping, error)
	CreateMapping(context.Context, historyimport.CreateMappingInput) (*historyimport.UserMapping, error)
	UpdateMappingConditional(context.Context, int, historyimport.UpdateMappingInput, int64) (*historyimport.UserMapping, error)
	DeleteMappingConditional(context.Context, int, int64) error
	DiscoverExternalUsers(context.Context, int) ([]historyimport.ExternalUser, error)
	AuthenticatePlex(context.Context, string, string) (string, error)
	CreateAdminRun(context.Context, int) (*historyimport.Run, error)
	BulkCreateAdminRuns(context.Context, int) (*historyimport.BulkRunResult, error)
	GetAdminRun(context.Context, string) (*historyimport.Run, error)
	CancelAdminRun(context.Context, string) error
}

// Canonical editors contain only configuration. Bookkeeping and enriched user
// labels do not participate in their validators; credentials are never returned.
type AdminHistoryImportSource struct {
	NeedsReconfiguration bool   `json:"needs_reconfiguration" doc:"Legacy source address contains unsupported credential or URL components; review and save a safe address."`
	ID                   ID     `json:"id"`
	Name                 string `json:"name"`
	SourceType           string `json:"source_type" enum:"emby,jellyfin,plex"`
	BaseURL              string `json:"base_url"`
	SystemID             string `json:"system_id"`
	Enabled              bool   `json:"enabled"`
	SortOrder            int    `json:"sort_order"`
	HasAdminToken        bool   `json:"has_admin_token"`
}
type AdminHistoryImportMapping struct {
	ID               ID     `json:"id"`
	SourceID         ID     `json:"source_id"`
	ExternalUserID   string `json:"external_user_id"`
	ExternalUserName string `json:"external_user_name"`
	SiloUserID       ID     `json:"silo_user_id"`
	SiloProfileID    ID     `json:"silo_profile_id"`
}
type AdminHistoryImportIDInput struct {
	ID ID `path:"id" pattern:"^[1-9][0-9]*$"`
}
type AdminHistoryImportGuardedInput struct {
	AdminHistoryImportIDInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminHistoryImportSourceOutput struct {
	Location string `header:"Location"`
	ETag     string `header:"ETag"`
	Body     AdminHistoryImportSource
}
type AdminHistoryImportMappingOutput struct {
	Location string `header:"Location"`
	ETag     string `header:"ETag"`
	Body     AdminHistoryImportMapping
}

func adminHistorySourceOf(s *historyimport.Source) AdminHistoryImportSource {
	safeURL, needsReview := adminHistorySafeSourceURL(s.BaseURL)
	return AdminHistoryImportSource{NeedsReconfiguration: needsReview, ID: ID(strconv.Itoa(s.ID)), Name: s.Name, SourceType: s.SourceType, BaseURL: safeURL, SystemID: s.SystemID, Enabled: s.Enabled, SortOrder: s.SortOrder, HasAdminToken: s.HasAdminToken}
}
func adminHistoryMappingOf(m *historyimport.UserMapping) AdminHistoryImportMapping {
	return AdminHistoryImportMapping{ID: ID(strconv.Itoa(m.ID)), SourceID: ID(strconv.Itoa(m.SourceID)), ExternalUserID: m.ExternalUserID, ExternalUserName: m.ExternalUserName, SiloUserID: ID(strconv.Itoa(m.SiloUserID)), SiloProfileID: ID(m.SiloProfileID)}
}
func adminHistoryTag(ctx context.Context, kind string, id int, revision int64) EntityTag {
	return RenderETag("admin-history-imports/"+strconv.Itoa(claimsFrom(ctx).UserID)+"/"+profileFrom(ctx)+"/"+kind, strconv.Itoa(id), revision)
}
func (reg *Registry) adminHistoryImports() (AdminHistoryImportService, *Problem) {
	if reg.deps.AdminHistoryImports == nil {
		return nil, unavailable("history import administration")
	}
	return reg.deps.AdminHistoryImports, nil
}

// adminHistoryDiscoveryProblem answers an administrative call that reaches
// out to a source server. A sentinel adminHistoryProblem recognizes keeps the
// status it decided - a missing source is 404, a source without a stored
// admin token is 409 - so a misconfiguration is not reported as an outage.
// A source server that did answer, below 500, rejected the stored admin
// credential or the configured address; discovery is a GET, so that is a
// state conflict the administrator resolves in the source configuration
// rather than a body validation failure. Only a source server that could not
// answer stays the fail-closed dependency problem.
func adminHistoryDiscoveryProblem(err error) *Problem {
	if p := adminHistoryProblem(err); p.Status != http.StatusInternalServerError {
		return p
	}
	if status := historyimport.UpstreamHTTPStatus(err); status > 0 && status < 500 {
		return NewProblem(TypeConflict, "The source server rejected the request; check the source address and its stored admin credential.")
	}
	return NewProblem(TypeDependencyUnavailable, "External users could not be loaded; check the source and its credential.")
}

// adminHistoryPlexLoginProblem maps a plex.tv sign-in failure. The ratified
// split runs first: a plex.tv answer below 500 is a validation failure
// carrying its message, and one at or above 500, or an unreachable plex.tv,
// is the dependency problem. An answer the split does not recognize - a 200
// with no token, a rate limiter refusing the call - is still plex.tv
// misbehaving rather than Silo failing, so it reports the dependency too.
func adminHistoryPlexLoginProblem(err error) *Problem {
	if p := historyImportProblem(adminHistoryUpstreamError(err)); p.Status != http.StatusInternalServerError {
		return p
	}
	return NewProblem(TypeDependencyUnavailable, "Plex sign-in did not complete; check the credentials and try again.")
}

// adminHistoryUpstreamError re-shapes a raw source-server failure into the
// *handlers.APIError the shared history-import mapping reads. The personal
// seam converts before it returns; the administrative service returns the
// client's own error, so the conversion happens here rather than duplicating
// the upstream split in a second mapper.
func adminHistoryUpstreamError(err error) error {
	if status := historyimport.UpstreamHTTPStatus(err); status > 0 {
		return handlers.HistoryImportUpstreamAPIError(status)
	}
	if historyimport.IsReachabilityError(err) {
		return handlers.HistoryImportUpstreamAPIError(http.StatusBadGateway)
	}
	return err
}

func adminHistoryProblem(err error) *Problem {
	switch {
	case errors.Is(err, historyimport.ErrSourceNotFound), errors.Is(err, historyimport.ErrMappingNotFound), errors.Is(err, historyimport.ErrRunNotFound):
		return NewProblem(TypeNotFound, "The history import resource was not found.")
	case errors.Is(err, historyimport.ErrStaleRevision):
		return NewProblem(TypePreconditionFailed, "The configuration changed; reload before saving.")
	case errors.Is(err, historyimport.ErrCredentialScopeChanged):
		return NewProblem(TypeValidationFailed, "Replace or clear the saved credential when changing the source address or system ID.")
	case errors.Is(err, historyimport.ErrInvalidInput), errors.Is(err, historyimport.ErrProfileNotFound):
		return NewProblem(TypeValidationFailed, "Check the source configuration and the selected account and profile.")
	case errors.Is(err, historyimport.ErrSourceDisabled), errors.Is(err, historyimport.ErrMappingDuplicate), errors.Is(err, historyimport.ErrSourceInUse), errors.Is(err, historyimport.ErrActiveRunExists), errors.Is(err, historyimport.ErrNoAdminToken):
		return NewProblem(TypeConflict, "The current source, mapping, or run state does not permit this action.")
	default:
		return NewProblem(TypeInternalError, "The history import operation could not be completed.")
	}
}

type AdminHistoryImportSourceCreateInput struct {
	Body struct {
		Name       string `json:"name" minLength:"1"`
		SourceType string `json:"source_type" enum:"emby,jellyfin,plex"`
		BaseURL    string `json:"base_url" minLength:"1"`
		SystemID   string `json:"system_id,omitempty"`
		Enabled    bool   `json:"enabled"`
		SortOrder  int    `json:"sort_order"`
		AdminToken string `json:"admin_token,omitempty"`
	}
}
type AdminHistoryImportSourceUpdateInput struct {
	RawBody []byte
	AdminHistoryImportIDInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		Name       *string `json:"name,omitempty" nullable:"false" minLength:"1"`
		BaseURL    *string `json:"base_url,omitempty" nullable:"false" minLength:"1"`
		SystemID   *string `json:"system_id,omitempty" nullable:"false"`
		Enabled    *bool   `json:"enabled,omitempty" nullable:"false"`
		SortOrder  *int    `json:"sort_order,omitempty" nullable:"false"`
		AdminToken *string `json:"admin_token,omitempty" nullable:"false" doc:"Omit to preserve, empty string to clear, nonempty string to replace."`
	}
}
type AdminHistoryImportTokenInput struct {
	AdminHistoryImportIDInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		Token string `json:"token" minLength:"1"`
	}
}
type AdminHistoryImportCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminHistoryImportCapabilitiesOutputBody
}

type AdminHistoryImportCapabilitiesOutputBody struct {
	Capability
	Available            bool `json:"available"`
	GuardedConfiguration bool `json:"guarded_configuration"`
	DurableRuns          bool `json:"durable_runs"`
	MaxBulkMappings      int  `json:"max_bulk_mappings"`
}

func adminHistoryID(id ID) (int, *Problem) {
	n, p := id.positive("path.id")
	if p != nil {
		return 0, NewProblem(TypeValidationFailed, "Invalid history import identifier.")
	}
	return n, nil
}
func (reg *Registry) getAdminHistorySource(ctx context.Context, in *AdminHistoryImportIDInput) (*AdminHistoryImportSourceOutput, error) {
	s, p := reg.adminHistoryImports()
	if p != nil {
		return nil, p
	}
	id, p := adminHistoryID(in.ID)
	if p != nil {
		return nil, p
	}
	row, err := s.GetAdminSource(ctx, id)
	if err != nil {
		return nil, adminHistoryProblem(err)
	}
	return &AdminHistoryImportSourceOutput{ETag: adminHistoryTag(ctx, "source", id, row.Revision).String(), Body: adminHistorySourceOf(row)}, nil
}
func (reg *Registry) getAdminHistoryMapping(ctx context.Context, in *AdminHistoryImportIDInput) (*AdminHistoryImportMappingOutput, error) {
	s, p := reg.adminHistoryImports()
	if p != nil {
		return nil, p
	}
	id, p := adminHistoryID(in.ID)
	if p != nil {
		return nil, p
	}
	row, err := s.GetMapping(ctx, id)
	if err != nil {
		return nil, adminHistoryProblem(err)
	}
	return &AdminHistoryImportMappingOutput{ETag: adminHistoryTag(ctx, "mapping", id, row.Revision).String(), Body: adminHistoryMappingOf(row)}, nil
}
func (reg *Registry) adminHistorySourceVersion(ctx context.Context, id int, match, none string) (int64, *Problem) {
	s, p := reg.adminHistoryImports()
	if p != nil {
		return 0, p
	}
	row, err := s.GetAdminSource(ctx, id)
	if err != nil {
		return 0, adminHistoryProblem(err)
	}
	return adminHistoryGuard(match, none, adminHistoryTag(ctx, "source", id, row.Revision), row.Revision)
}
func (reg *Registry) adminHistorySourceWriteResult(ctx context.Context, id int, row *historyimport.Source, err error) (*AdminHistoryImportSourceOutput, error) {
	if errors.Is(err, historyimport.ErrStaleRevision) {
		current, e := reg.getAdminHistorySource(ctx, &AdminHistoryImportIDInput{ID: ID(strconv.Itoa(id))})
		if e != nil {
			return nil, e
		}
		return nil, adminHistoryProblem(err).WithHeader("ETag", current.ETag)
	}
	if err != nil {
		return nil, adminHistoryProblem(err)
	}
	return &AdminHistoryImportSourceOutput{ETag: adminHistoryTag(ctx, "source", id, row.Revision).String(), Body: adminHistorySourceOf(row)}, nil
}
func (reg *Registry) createAdminHistorySource(ctx context.Context, in *AdminHistoryImportSourceCreateInput) (*AdminHistoryImportSourceOutput, error) {
	s, p := reg.adminHistoryImports()
	if p != nil {
		return nil, p
	}
	row, err := s.CreateSource(ctx, historyimport.CreateSourceInput{Name: in.Body.Name, SourceType: in.Body.SourceType, BaseURL: in.Body.BaseURL, SystemID: in.Body.SystemID, Enabled: in.Body.Enabled, SortOrder: in.Body.SortOrder, AdminToken: in.Body.AdminToken})
	if err != nil {
		return nil, adminHistoryProblem(err)
	}
	out, err := reg.adminHistorySourceWriteResult(ctx, row.ID, row, nil)
	if err == nil {
		out.Location = Prefix + "/admin/history-import-sources/" + strconv.Itoa(row.ID)
	}
	return out, err
}
func (reg *Registry) updateAdminHistorySource(ctx context.Context, in *AdminHistoryImportSourceUpdateInput) (*AdminHistoryImportSourceOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	s, p := reg.adminHistoryImports()
	if p != nil {
		return nil, p
	}
	id, p := adminHistoryID(in.ID)
	if p != nil {
		return nil, p
	}
	expected, p := reg.adminHistorySourceVersion(ctx, id, in.IfMatch, in.IfNoneMatch)
	if p != nil {
		return nil, p
	}
	row, err := s.UpdateSourceConditional(ctx, id, historyimport.UpdateSourceInput{Name: in.Body.Name, BaseURL: in.Body.BaseURL, SystemID: in.Body.SystemID, Enabled: in.Body.Enabled, SortOrder: in.Body.SortOrder, AdminToken: in.Body.AdminToken}, expected)
	return reg.adminHistorySourceWriteResult(ctx, id, row, err)
}

func registerAdminHistoryImports(reg *Registry) {
	op := func(method, path, id string, guard bool) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "admin", "Manage history import configuration and execution."), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, Guarded: guard}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	registerAdminHistoryImportLists(reg, op)
	registerAdminHistoryImportMappings(reg, op)
	registerAdminHistoryImportRuns(reg, op)
	Register(reg, op(http.MethodGet, "/admin/history-imports/capabilities", "getAdminHistoryImportCapabilities", false), func(_ context.Context, _ *CapabilityInput) (*AdminHistoryImportCapabilitiesOutput, error) {
		out := new(AdminHistoryImportCapabilitiesOutput)
		available := reg.deps.AdminHistoryImports != nil
		out.Body.Available = available
		out.Body.GuardedConfiguration = available
		out.Body.DurableRuns = available
		out.Body.MaxBulkMappings = 200
		return out, nil
	})
	Register(reg, op(http.MethodGet, "/admin/history-import-sources/{id}", "getAdminHistoryImportSource", false), reg.getAdminHistorySource)
	Register(reg, op(http.MethodGet, "/admin/history-imports/mappings/{id}", "getAdminHistoryImportMapping", false), reg.getAdminHistoryMapping)
	create := op(http.MethodPost, "/admin/history-import-sources", "createAdminHistoryImportSource", false)
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, reg.createAdminHistorySource)
	Register(reg, op(http.MethodPut, "/admin/history-import-sources/{id}", "updateAdminHistoryImportSource", true), reg.updateAdminHistorySource)
	Register(reg, op(http.MethodDelete, "/admin/history-import-sources/{id}", "deleteAdminHistoryImportSource", true), func(ctx context.Context, in *AdminHistoryImportGuardedInput) (*struct{}, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		id, p := adminHistoryID(in.ID)
		if p != nil {
			return nil, p
		}
		expected, p := reg.adminHistorySourceVersion(ctx, id, in.IfMatch, in.IfNoneMatch)
		if p != nil {
			return nil, p
		}
		if err := s.DeleteSourceConditional(ctx, id, expected); err != nil {
			if errors.Is(err, historyimport.ErrStaleRevision) {
				_, err = reg.adminHistorySourceWriteResult(ctx, id, nil, err)
				return nil, err
			}
			return nil, adminHistoryProblem(err)
		}
		return nil, nil
	})
	Register(reg, op(http.MethodPut, "/admin/history-imports/sources/{id}/token", "setAdminHistoryImportToken", true), func(ctx context.Context, in *AdminHistoryImportTokenInput) (*AdminHistoryImportSourceOutput, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		id, p := adminHistoryID(in.ID)
		if p != nil {
			return nil, p
		}
		expected, p := reg.adminHistorySourceVersion(ctx, id, in.IfMatch, in.IfNoneMatch)
		if p != nil {
			return nil, p
		}
		row, err := s.SetSourceAdminTokenConditional(ctx, id, in.Body.Token, expected)
		return reg.adminHistorySourceWriteResult(ctx, id, row, err)
	})
	Register(reg, op(http.MethodDelete, "/admin/history-imports/sources/{id}/token", "clearAdminHistoryImportToken", true), func(ctx context.Context, in *AdminHistoryImportGuardedInput) (*struct{}, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		id, p := adminHistoryID(in.ID)
		if p != nil {
			return nil, p
		}
		expected, p := reg.adminHistorySourceVersion(ctx, id, in.IfMatch, in.IfNoneMatch)
		if p != nil {
			return nil, p
		}
		_, err := s.ClearSourceAdminTokenConditional(ctx, id, expected)
		if err != nil {
			if errors.Is(err, historyimport.ErrStaleRevision) {
				_, err = reg.adminHistorySourceWriteResult(ctx, id, nil, err)
				return nil, err
			}
			return nil, adminHistoryProblem(err)
		}
		return nil, nil
	})
}

func adminHistoryGuard(match, none string, tag EntityTag, revision int64) (int64, *Problem) {
	if p := EvaluateGuardedPreconditions(match, none, tag); p != nil {
		return 0, p
	}
	if match == "*" {
		return -1, nil
	}
	return revision, nil
}

func adminHistorySafeSourceURL(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", true
	}
	unsafe := parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != ""
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String(), unsafe
}

func (c AdminHistoryImportCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
