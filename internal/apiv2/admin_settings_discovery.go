package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminSettingReadService interface {
	ReadAdminSetting(context.Context, string) (handlers.AdminSettingValue, error)
}
type AdminSettingReadInput struct {
	Key string `path:"key" minLength:"1" maxLength:"256"`
}
type AdminSettingReadOutput struct {
	Body struct {
		Key             string `json:"key"`
		Value           string `json:"value"`
		RestartRequired bool   `json:"restart_required"`
	}
}
type AdminPlaybackRoutingCapabilities struct {
	Capability
	Features             []string `json:"features"`
	Workloads            []string `json:"workloads"`
	ExecutionPreferences []string `json:"execution_preferences"`
	EgressPreferences    []string `json:"egress_preferences"`
}
type AdminPlaybackRoutingCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminPlaybackRoutingCapabilities
}

func registerAdminSettingsDiscovery(reg *Registry) {
	op := func(path, id, summary string) Operation {
		return Operation{Operation: humaOp("GET", Prefix+"/admin/"+path, id, "admin-settings", summary), Class: ClassActingAdmin, ServiceBacked: true}
	}
	Register(reg, op("settings/{key}", "getAdminSetting", "Read one visible stored setting; protected, missing and empty values return not found."), func(ctx context.Context, in *AdminSettingReadInput) (*AdminSettingReadOutput, error) {
		if reg.deps.AdminSettingRead == nil {
			return nil, unavailable("administrator settings")
		}
		value, err := reg.deps.AdminSettingRead.ReadAdminSetting(ctx, in.Key)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := new(AdminSettingReadOutput)
		out.Body.Key = value.Key
		out.Body.Value = value.Value
		out.Body.RestartRequired = value.RestartRequired
		return out, nil
	})
	routing := op("playback-routing/capabilities", "getAdminPlaybackRoutingCapabilities", "Read the stable routing configuration vocabulary, independently of available worker capacity.")
	routing.ServiceBacked = false
	Register(reg, routing, func(context.Context, *CapabilityInput) (*AdminPlaybackRoutingCapabilitiesOutput, error) {
		v := handlers.AdminPlaybackRoutingCapabilities()
		return &AdminPlaybackRoutingCapabilitiesOutput{Body: AdminPlaybackRoutingCapabilities{Features: v.Features, Workloads: v.Workloads, ExecutionPreferences: v.ExecutionPreferences, EgressPreferences: v.EgressPreferences}}, nil
	})
}

func (c AdminPlaybackRoutingCapabilities) capabilityState() string { return StateAvailable }
