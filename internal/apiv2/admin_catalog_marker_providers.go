package apiv2

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"net/http"
)

type AdminMarkerProvidersService interface {
	ListMarkerProviders(context.Context) ([]handlers.MarkerProviderConfigView, error)
	UpdateMarkerProvider(context.Context, string, handlers.MarkerProviderUpdate) (handlers.MarkerProviderConfigView, error)
	ValidateMarkerProvider(context.Context, string) (handlers.MarkerProviderValidationView, error)
}
type AdminMarkerProvider struct {
	Provider                string  `json:"provider"`
	DisplayName             string  `json:"display_name,omitempty"`
	SourceType              string  `json:"source_type,omitempty"`
	PluginID                string  `json:"plugin_id,omitempty"`
	PluginInstallationID    *ID     `json:"plugin_installation_id,omitempty"`
	CapabilityID            string  `json:"capability_id,omitempty"`
	IsSubmitter             bool    `json:"is_submitter"`
	FetchEnabled            bool    `json:"fetch_enabled"`
	FetchPriority           int     `json:"fetch_priority"`
	ContributeEnabled       bool    `json:"contribute_enabled"`
	ContributeAutoLocal     bool    `json:"contribute_auto_local"`
	ContributeMinConfidence float64 `json:"contribute_min_confidence" minimum:"0" maximum:"1"`
}
type AdminMarkerProviders struct {
	Providers []AdminMarkerProvider `json:"providers"`
}
type AdminMarkerProvidersOutput struct{ Body AdminMarkerProviders }
type AdminMarkerProviderOutput struct{ Body AdminMarkerProvider }
type AdminMarkerProviderInput struct {
	Provider string `path:"provider" minLength:"1" maxLength:"512"`
}
type AdminMarkerProviderUpdate struct {
	FetchEnabled            *bool    `json:"fetch_enabled,omitempty" nullable:"true"`
	FetchPriority           *int     `json:"fetch_priority,omitempty" nullable:"true"`
	ContributeEnabled       *bool    `json:"contribute_enabled,omitempty" nullable:"true"`
	ContributeAutoLocal     *bool    `json:"contribute_auto_local,omitempty" nullable:"true"`
	ContributeMinConfidence *float64 `json:"contribute_min_confidence,omitempty" nullable:"true" minimum:"0" maximum:"1"`
}
type AdminMarkerProviderUpdateInput struct {
	AdminMarkerProviderInput
	Body AdminMarkerProviderUpdate
}
type AdminMarkerProviderValidation struct {
	Valid bool                  `json:"valid"`
	Error string                `json:"error,omitempty"`
	Stats *AdminMarkerUserStats `json:"stats,omitempty"`
}
type AdminMarkerProviderValidationOutput struct{ Body AdminMarkerProviderValidation }

func adminMarkerProviderOf(v handlers.MarkerProviderConfigView) AdminMarkerProvider {
	out := AdminMarkerProvider{Provider: v.Provider, DisplayName: v.DisplayName, SourceType: v.SourceType, PluginID: v.PluginID, CapabilityID: v.CapabilityID, IsSubmitter: v.IsSubmitter, FetchEnabled: v.FetchEnabled, FetchPriority: v.FetchPriority, ContributeEnabled: v.ContributeEnabled, ContributeAutoLocal: v.ContributeAutoLocal, ContributeMinConfidence: v.ContributeMinConfidence}
	if v.PluginInstallationID != 0 {
		out.PluginInstallationID = new(IDFromInt(int64(v.PluginInstallationID)))
	}
	return out
}
func registerAdminCatalogMarkerProviders(reg *Registry) {
	op := func(method, path, id string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/markers/providers"+path, id, "admin-catalog", "Manage registered marker provider configuration."), Class: ClassActingAdmin, ServiceBacked: true}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	Register(reg, op(http.MethodGet, "", "listAdminMarkerProviders"), func(ctx context.Context, _ *struct{}) (*AdminMarkerProvidersOutput, error) {
		if reg.deps.AdminMarkerProviders == nil {
			return nil, unavailable("marker providers")
		}
		rows, err := reg.deps.AdminMarkerProviders.ListMarkerProviders(ctx)
		if err != nil {
			return nil, collectionProblem(err)
		}
		out := &AdminMarkerProvidersOutput{Body: AdminMarkerProviders{Providers: make([]AdminMarkerProvider, 0, len(rows))}}
		for _, row := range rows {
			out.Body.Providers = append(out.Body.Providers, adminMarkerProviderOf(row))
		}
		return out, nil
	})
	Register(reg, op(http.MethodPut, "/{provider}", "updateAdminMarkerProvider"), func(ctx context.Context, in *AdminMarkerProviderUpdateInput) (*AdminMarkerProviderOutput, error) {
		if reg.deps.AdminMarkerProviders == nil {
			return nil, unavailable("marker providers")
		}
		b := in.Body
		row, err := reg.deps.AdminMarkerProviders.UpdateMarkerProvider(ctx, in.Provider, handlers.MarkerProviderUpdate{FetchEnabled: b.FetchEnabled, FetchPriority: b.FetchPriority, ContributeEnabled: b.ContributeEnabled, ContributeAutoLocal: b.ContributeAutoLocal, ContributeMinConfidence: b.ContributeMinConfidence})
		if err != nil {
			return nil, collectionProblem(err)
		}
		return &AdminMarkerProviderOutput{Body: adminMarkerProviderOf(row)}, nil
	})
	Register(reg, op(http.MethodPost, "/{provider}/validate", "validateAdminMarkerProvider"), func(ctx context.Context, in *AdminMarkerProviderInput) (*AdminMarkerProviderValidationOutput, error) {
		if reg.deps.AdminMarkerProviders == nil {
			return nil, unavailable("marker providers")
		}
		result, err := reg.deps.AdminMarkerProviders.ValidateMarkerProvider(ctx, in.Provider)
		if err != nil {
			return nil, collectionProblem(err)
		}
		out := AdminMarkerProviderValidation{Valid: result.Valid, Stats: nil}
		if result.Stats != nil {
			out.Stats = new(AdminMarkerUserStats(*result.Stats))
		}
		if !result.Valid {
			out.Error = "Provider validation failed"
		}
		return &AdminMarkerProviderValidationOutput{Body: out}, nil
	})
}

type AdminMarkerUserStats handlers.MarkerUserStatsView
