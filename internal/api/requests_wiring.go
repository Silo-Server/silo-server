package api

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/plugins"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// PluginRequestRouterAdapter adapts plugins.Service to
// mediarequests.RouterClientResolver. The concrete *pluginhost.RequestRouterClient
// satisfies mediarequests.RouterClient, so the adapter names the interface as its
// return type (Go has no return-type covariance).
type PluginRequestRouterAdapter struct {
	Svc *plugins.Service
}

func (a PluginRequestRouterAdapter) RequestRouterClient(ctx context.Context, installationID int, capabilityID string) (mediarequests.RouterClient, error) {
	return a.Svc.RequestRouterClient(ctx, installationID, capabilityID)
}

// AttachRequestRouter wires the plugin-backed router provider onto a requests
// service. Both the HTTP handler and the reconcile task call this so the wiring
// lives in one place.
func AttachRequestRouter(svc *mediarequests.Service, pluginService *plugins.Service) {
	svc.SetRouterProvider(mediarequests.NewPluginRouterProvider(PluginRequestRouterAdapter{pluginService}))
}
