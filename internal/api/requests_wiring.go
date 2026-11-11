package api

import (
	"context"
	"errors"

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
	if a.Svc == nil {
		return nil, errors.New("request router plugin service is not configured")
	}
	return a.Svc.RequestRouterClient(ctx, installationID, capabilityID)
}

// RouterFeatures reads the optional request_router.v1 features the
// capability's stored manifest declares, without launching the plugin.
func (a PluginRequestRouterAdapter) RouterFeatures(ctx context.Context, installationID int, capabilityID string) (mediarequests.RouterFeatures, error) {
	if a.Svc == nil {
		return mediarequests.RouterFeatures{}, errors.New("request router plugin service is not configured")
	}
	descriptor, err := a.Svc.RequestRouterDescriptor(ctx, installationID, capabilityID)
	if err != nil {
		return mediarequests.RouterFeatures{}, err
	}
	features := mediarequests.RouterFeatures{
		SupportsSeasons:         descriptor.GetSupportsSeasons(),
		ReportsDownloadProgress: descriptor.GetReportsDownloadProgress(),
		APIKeyOptional:          descriptor.GetApiKeyOptional(),
		BaseURLOptional:         descriptor.GetBaseUrlOptional(),
	}
	if declared := descriptor.GetRequesterOptions(); len(declared) > 0 {
		// An option without its own label takes the admin form field's.
		labels, err := a.Svc.RequestRouterFieldLabels(ctx, installationID, capabilityID)
		if err != nil {
			return mediarequests.RouterFeatures{}, err
		}
		for _, option := range declared {
			label := option.GetLabel()
			if label == "" {
				label = labels[option.GetField()]
			}
			features.RequesterOptions = append(features.RequesterOptions, mediarequests.RouterRequesterOption{
				Field:        option.GetField(),
				MediaType:    mediarequests.MediaType(option.GetMediaType()),
				Label:        label,
				ChooserField: option.GetChooserField(),
			})
		}
	}
	return features, nil
}

// AttachRequestRouter wires the plugin-backed router provider onto a requests
// service. Both the HTTP handler and the reconcile task call this so the wiring
// lives in one place. With either dependency absent (e.g. a build without the
// plugin service) it leaves the requests service router-less, so fulfillment
// degrades to the existing "no backend configured" failure instead of panicking.
func AttachRequestRouter(svc *mediarequests.Service, pluginService *plugins.Service) {
	if svc == nil || pluginService == nil {
		return
	}
	svc.SetRouterProvider(mediarequests.NewPluginRouterProvider(PluginRequestRouterAdapter{pluginService}))
}
