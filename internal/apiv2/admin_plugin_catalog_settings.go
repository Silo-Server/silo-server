package apiv2

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type AdminPluginCatalogSettingsService interface {
	GetCatalogSettings(context.Context) (plugins.CatalogSettings, error)
	SetIncludeApprovedCommunityConditional(context.Context, bool, *bool) (plugins.CatalogSettings, error)
}
type AdminPluginCatalogSettings struct {
	IncludeApprovedCommunityPlugins bool `json:"include_approved_community_plugins"`
}
type AdminPluginCatalogSettingsOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   AdminPluginCatalogSettings
}
type AdminPluginCatalogStatus struct {
	ApprovedCommunityPluginCount  int  `json:"approved_community_plugin_count"`
	InstalledCommunityPluginCount int  `json:"installed_community_plugin_count"`
	MigratedPluginCount           int  `json:"migrated_plugin_count"`
	CommunityUpdatesPaused        bool `json:"community_updates_paused"`
}
type AdminPluginCatalogStatusOutput struct{ Body AdminPluginCatalogStatus }
type AdminPluginCatalogReadInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminPluginCatalogSettingsInput struct {
	RawBody     []byte
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminPluginCatalogSettings
}

func pluginCatalogSettingsTag(ctx context.Context, include bool) EntityTag {
	return RenderETag("admin-plugin-catalog:"+strconv.Itoa(claimsFrom(ctx).UserID)+":"+profileFrom(ctx)+":"+viewerScopeDigest(ctx), strconv.FormatBool(include), 1)
}
func pluginCatalogSettingsOutput(ctx context.Context, s plugins.CatalogSettings) *AdminPluginCatalogSettingsOutput {
	return &AdminPluginCatalogSettingsOutput{ETag: pluginCatalogSettingsTag(ctx, s.IncludeApprovedCommunityPlugins).String(), Body: AdminPluginCatalogSettings{IncludeApprovedCommunityPlugins: s.IncludeApprovedCommunityPlugins}}
}
func pluginCatalogSettingsProblem(err error) error {
	if errors.Is(err, plugins.ErrCatalogSettingsChanged) {
		return NewProblem(TypePreconditionFailed, "Plugin catalog settings changed. Reload before editing.")
	}
	return serviceProblem(err)
}
func registerAdminPluginCatalogSettings(reg *Registry) {
	op := func(method, path, id, summary string) Operation {
		return Operation{Operation: humaOp(method, Prefix+"/admin/plugins/"+path, id, "admin-plugins", summary), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true}
	}
	get := op("GET", "catalog-settings", "getAdminPluginCatalogSettings", "Read canonical plugin catalog configuration and its validator.")
	get.Conditional = true
	Register(reg, get, func(ctx context.Context, in *AdminPluginCatalogReadInput) (*AdminPluginCatalogSettingsOutput, error) {
		if reg.deps.AdminPluginCatalogSettings == nil {
			return nil, unavailable("plugin catalog settings")
		}
		s, err := reg.deps.AdminPluginCatalogSettings.GetCatalogSettings(ctx)
		if err != nil {
			return nil, pluginCatalogSettingsProblem(err)
		}
		out := pluginCatalogSettingsOutput(ctx, s)
		tag := pluginCatalogSettingsTag(ctx, s.IncludeApprovedCommunityPlugins)
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	Register(reg, op("GET", "catalog-status", "getAdminPluginCatalogStatus", "Read plugin catalog counts and update availability separately from editable configuration."), func(ctx context.Context, _ *struct{}) (*AdminPluginCatalogStatusOutput, error) {
		if reg.deps.AdminPluginCatalogSettings == nil {
			return nil, unavailable("plugin catalog settings")
		}
		s, err := reg.deps.AdminPluginCatalogSettings.GetCatalogSettings(ctx)
		if err != nil {
			return nil, pluginCatalogSettingsProblem(err)
		}
		return &AdminPluginCatalogStatusOutput{Body: AdminPluginCatalogStatus{ApprovedCommunityPluginCount: s.ApprovedCommunityPluginCount, InstalledCommunityPluginCount: s.InstalledCommunityPluginCount, MigratedPluginCount: s.MigratedPluginCount, CommunityUpdatesPaused: s.CommunityUpdatesPaused}}, nil
	})
	update := op("PUT", "catalog-settings", "updateAdminPluginCatalogSettings", "Atomically replace captured catalog configuration and reconcile managed repositories.")
	update.Guarded = true
	update.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, update, func(ctx context.Context, in *AdminPluginCatalogSettingsInput) (*AdminPluginCatalogSettingsOutput, error) {
		if reg.deps.AdminPluginCatalogSettings == nil {
			return nil, unavailable("plugin catalog settings")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		s, err := reg.deps.AdminPluginCatalogSettings.GetCatalogSettings(ctx)
		if err != nil {
			return nil, pluginCatalogSettingsProblem(err)
		}
		if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, pluginCatalogSettingsTag(ctx, s.IncludeApprovedCommunityPlugins)); p != nil {
			return nil, p
		}
		expected := new(s.IncludeApprovedCommunityPlugins)
		if strings.TrimSpace(in.IfMatch) == "*" {
			// There are two canonical values. Only drop the row-lock comparison
			// if the other value also satisfies If-None-Match; otherwise a
			// concurrent writer could install a value the caller excluded.
			other := pluginCatalogSettingsTag(ctx, !s.IncludeApprovedCommunityPlugins)
			if EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, other) == nil {
				expected = nil
			}
		}
		saved, err := reg.deps.AdminPluginCatalogSettings.SetIncludeApprovedCommunityConditional(ctx, in.Body.IncludeApprovedCommunityPlugins, expected)
		if conflict, ok := errors.AsType[*plugins.CatalogSettingsConflict](err); ok {
			return nil, StaleVersionProblem(pluginCatalogSettingsTag(ctx, conflict.Actual))
		}
		if err != nil {
			return nil, pluginCatalogSettingsProblem(err)
		}
		return pluginCatalogSettingsOutput(ctx, saved), nil
	})
}
