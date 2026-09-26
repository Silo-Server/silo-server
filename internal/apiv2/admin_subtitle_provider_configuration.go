package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type AdminSubtitleProviderConfigurationService interface {
	GetAdminSubtitleProviderConfiguration(context.Context, string) (handlers.AdminSubtitleProviderConfiguration, error)
	SaveAdminSubtitleProviderConfiguration(context.Context, string, subtitles.ProviderConfigChange, *int64) (handlers.AdminSubtitleProviderSaveResult, error)
}
type AdminSubtitleProviderConfiguration struct {
	ProviderName   string `json:"provider_name"`
	Enabled        bool   `json:"enabled"`
	HasAPIKey      bool   `json:"has_api_key"`
	HasCredentials bool   `json:"has_credentials"`
}
type AdminSubtitleProviderConfigurationInput struct {
	Provider    string `path:"provider" minLength:"1" maxLength:"128"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminSubtitleProviderConfigurationOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   AdminSubtitleProviderConfiguration
}
type AdminSubtitleProviderConfigurationChange struct {
	Enabled          bool   `json:"enabled"`
	APIKey           string `json:"api_key,omitempty" maxLength:"8192"`
	Username         string `json:"username,omitempty" maxLength:"1024"`
	Password         string `json:"password,omitempty" maxLength:"8192"`
	ClearCredentials bool   `json:"clear_credentials,omitempty"`
}
type AdminSubtitleProviderConfigurationPutInput struct {
	Provider    string `path:"provider" minLength:"1" maxLength:"128"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	RawBody     []byte
	Body        AdminSubtitleProviderConfigurationChange
}
type AdminSubtitleProviderConfigurationSaved struct {
	SavedRevision        string  `json:"saved_revision" pattern:"^[1-9][0-9]*$"`
	LocalApply           string  `json:"local_apply" enum:"applied,not_configured,unsupported,failed"`
	LocalAppliedRevision *string `json:"local_applied_revision,omitempty" pattern:"^(0|[1-9][0-9]*)$"`
}
type AdminSubtitleProviderConfigurationSaveOutput struct {
	ETag string `header:"ETag"`
	Body AdminSubtitleProviderConfigurationSaved
}

func adminSubtitleProviderConfigurationTag(ctx context.Context, provider string, revision int64) EntityTag {
	return RenderETag("admin-subtitle-provider/"+strconv.Itoa(claimsFrom(ctx).UserID)+"/"+profileFrom(ctx), provider, revision)
}
func adminSubtitleProviderConfigurationProblem(ctx context.Context, provider string, err error) *Problem {
	if conflict, ok := errors.AsType[*subtitles.ProviderConfigRevisionConflict](err); ok {
		return NewProblem(TypePreconditionFailed, "Provider configuration changed; read the current configuration before deciding whether to save again.").WithHeader("ETag", adminSubtitleProviderConfigurationTag(ctx, provider, conflict.CurrentRevision).String())
	}
	if e, ok := errors.AsType[*handlers.APIError](err); ok {
		switch e.Status {
		case http.StatusBadRequest:
			return NewProblem(TypeValidationFailed, "Invalid provider configuration.")
		case http.StatusServiceUnavailable:
			return NewProblem(TypeDependencyUnavailable, "Provider configuration is unavailable.")
		}
	}
	return NewProblem(TypeInternalError, "Unable to confirm provider configuration. Read the saved configuration before deciding whether to retry.")
}
func (reg *Registry) adminSubtitleProviderConfiguration(ctx context.Context, provider string) (handlers.AdminSubtitleProviderConfiguration, error) {
	if reg.deps.AdminSubtitleProviderConfiguration == nil {
		return handlers.AdminSubtitleProviderConfiguration{}, NewProblem(TypeDependencyUnavailable, "Provider configuration is unavailable.")
	}
	row, err := reg.deps.AdminSubtitleProviderConfiguration.GetAdminSubtitleProviderConfiguration(ctx, provider)
	if err != nil {
		return row, adminSubtitleProviderConfigurationProblem(ctx, provider, err)
	}
	return row, nil
}
func registerAdminSubtitleProviderConfiguration(reg *Registry) {
	read := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/subtitle-providers/{provider}", "getAdminSubtitleProviderConfiguration", "admin", "Read redacted canonical provider configuration and its strong edit validator. Missing stored configuration has a disabled virtual representation whose exact validator permits creation."), Class: ClassActingAdmin, ServiceBacked: true, Conditional: true}
	Register(reg, read, func(ctx context.Context, in *AdminSubtitleProviderConfigurationInput) (*AdminSubtitleProviderConfigurationOutput, error) {
		row, err := reg.adminSubtitleProviderConfiguration(ctx, in.Provider)
		if err != nil {
			return nil, err
		}
		tag := adminSubtitleProviderConfigurationTag(ctx, in.Provider, row.Revision)
		out := &AdminSubtitleProviderConfigurationOutput{ETag: tag.String(), Body: AdminSubtitleProviderConfiguration{ProviderName: row.ProviderName, Enabled: row.Enabled, HasAPIKey: row.HasAPIKey, HasCredentials: row.HasCredentials}}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	put := Operation{Operation: humaOp(http.MethodPut, Prefix+"/admin/subtitle-providers/{provider}", "updateAdminSubtitleProviderConfiguration", "admin", "Save configuration with the captured strong validator. Blank credentials preserve stored values; explicit clear disables and removes credentials. If-Match:* updates existing storage only. Confirmed durable save is separate from local apply, which may observe a later revision and does not imply cluster convergence or credential verification. An uncertain save requires explicit reconciliation; never automatically rebase or replay."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, put, func(ctx context.Context, in *AdminSubtitleProviderConfigurationPutInput) (*AdminSubtitleProviderConfigurationSaveOutput, error) {
		row, err := reg.adminSubtitleProviderConfiguration(ctx, in.Provider)
		if err != nil {
			return nil, err
		}
		tag := adminSubtitleProviderConfigurationTag(ctx, in.Provider, row.Revision)
		if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		revision := new(row.Revision)
		if strings.TrimSpace(in.IfMatch) == "*" {
			if row.Revision == 0 {
				return nil, NewProblem(TypePreconditionFailed, "No stored provider configuration exists.").WithHeader("ETag", tag.String())
			}
			revision = nil
		}
		change := subtitles.ProviderConfigChange{Enabled: in.Body.Enabled, APIKey: in.Body.APIKey, Username: in.Body.Username, Password: in.Body.Password, ClearCredentials: in.Body.ClearCredentials}
		saved, err := reg.deps.AdminSubtitleProviderConfiguration.SaveAdminSubtitleProviderConfiguration(ctx, in.Provider, change, revision)
		if err != nil {
			return nil, adminSubtitleProviderConfigurationProblem(ctx, in.Provider, err)
		}
		body := AdminSubtitleProviderConfigurationSaved{SavedRevision: strconv.FormatInt(saved.SavedRevision, 10), LocalApply: string(saved.LocalApply)}
		if saved.LocalAppliedRevision != nil {
			body.LocalAppliedRevision = new(strconv.FormatInt(*saved.LocalAppliedRevision, 10))
		}
		// The validator names the saved revision, which may already be superseded.
		// Editors must explicitly reload before beginning another edit.
		return &AdminSubtitleProviderConfigurationSaveOutput{ETag: adminSubtitleProviderConfigurationTag(ctx, in.Provider, saved.SavedRevision).String(), Body: body}, nil
	})
}
