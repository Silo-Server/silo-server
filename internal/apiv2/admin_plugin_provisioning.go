package apiv2

import (
	"context"
	"net/http"
)

type AdminPluginCapabilities struct {
	Capability
	EnvDeclarations bool `json:"env_declarations" doc:"Installation and binding reads can report env_managed ownership, mutations refuse changes to owned fields, and provisioning status is available."`
}

type AdminPluginCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminPluginCapabilities
}

type AdminPluginProvisionStatus struct {
	PluginID string `json:"plugin_id" doc:"The declared manifest plugin ID. An installation need not exist."`
	Version  string `json:"version" doc:"Installed version when applied. Otherwise the requested version, if any."`
	State    string `json:"state" doc:"The latest recorded provisioning outcome for the declaration."`
	Detail   string `json:"detail" doc:"Redacted reason or status detail, empty when there is none."`
}

type AdminPluginProvisionStatusOutput struct {
	Body Collection[AdminPluginProvisionStatus]
}

func registerAdminPluginProvisioning(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/plugins/capabilities", "getAdminPluginCapabilities", "admin-plugins", "Discover plugin administration features."), Class: ClassActingAdmin}, func(context.Context, *CapabilityInput) (*AdminPluginCapabilitiesOutput, error) {
		configured := reg.deps.AdminPluginProvisionStatus != nil
		return &AdminPluginCapabilitiesOutput{Body: AdminPluginCapabilities{
			Capability:      Capability{State: configuredCapabilityState(configured)},
			EnvDeclarations: configured,
		}}, nil
	})
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/plugins/provisioning-status", "listAdminPluginProvisioningStatus", "admin-plugins", "Read recorded environment provisioning status, including declarations with no installation."), Class: ClassActingAdmin, ServiceBacked: true}, func(ctx context.Context, _ *struct{}) (*AdminPluginProvisionStatusOutput, error) {
		if reg.deps.AdminPluginProvisionStatus == nil {
			return nil, unavailable("plugin provisioning status")
		}
		rows, err := reg.deps.AdminPluginProvisionStatus(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		items := make([]AdminPluginProvisionStatus, 0, len(rows))
		for _, row := range rows {
			items = append(items, AdminPluginProvisionStatus{PluginID: row.PluginID, Version: row.Version, State: row.State, Detail: row.Detail})
		}
		return &AdminPluginProvisionStatusOutput{Body: NewCollection(items)}, nil
	})
}
