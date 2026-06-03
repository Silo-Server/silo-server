package api

import (
	"context"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/plugins"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

// autoscanQueuer is the scan-enqueue surface BuildAutoscanService needs; it is
// satisfied by *scanqueue.Service (autoscan.Queuer's concrete production impl).
type autoscanQueuer = autoscan.Queuer

// RequestIntegrationLookup adapts the Requests repository to the autoscan
// connection resolver's RequestIntegrationLookup: it resolves a soft-linked
// Requests integration to its base URL and (write-only) api-key ref.
type RequestIntegrationLookup struct {
	Repo *mediarequests.Repository
}

func (l RequestIntegrationLookup) Get(ctx context.Context, integrationID string) (baseURL, apiKeyRef string, err error) {
	integration, err := l.Repo.GetIntegration(ctx, integrationID)
	if err != nil {
		return "", "", err
	}
	return integration.BaseURL, integration.APIKeyRef, nil
}

// PluginScanSourceAdapter adapts plugins.Service to autoscan.ScanSourceResolver.
// plugins.Service.ScanSourceClient returns the concrete
// *pluginhost.ScanSourceClient; that concrete type satisfies
// autoscan.PollChangesClient (it has the matching PollChanges method), so the
// adapter declares the exported interface as its return type and returns the
// concrete value (Go has no return-type covariance, so the method signature must
// name the interface exactly to satisfy ScanSourceResolver).
type PluginScanSourceAdapter struct {
	Svc *plugins.Service
}

func (a PluginScanSourceAdapter) ScanSourceClient(ctx context.Context, installationID int, capabilityID string) (autoscan.PollChangesClient, error) {
	return a.Svc.ScanSourceClient(ctx, installationID, capabilityID)
}

// scanSourceCapabilityType is the plugin capability type autoscan discovery
// enumerates.
const scanSourceCapabilityType = "scan_source.v1"

// PluginScanSourceLister adapts the plugin installation store to
// autoscan.ScanSourceLister: it enumerates every installed scan_source.v1
// capability across enabled installations.
type PluginScanSourceLister struct {
	Store *plugins.InstallationStore
}

func (l PluginScanSourceLister) ListScanSources(ctx context.Context) ([]autoscan.DiscoveredSource, error) {
	installations, err := l.Store.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	var out []autoscan.DiscoveredSource
	for _, inst := range installations {
		caps, err := l.Store.ListCapabilities(ctx, inst.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range caps {
			if c == nil || c.Type != scanSourceCapabilityType {
				continue
			}
			out = append(out, autoscan.DiscoveredSource{
				InstallationID: c.InstallationID,
				CapabilityID:   c.ID,
			})
		}
	}
	return out, nil
}

// AutoscanSecretResolver resolves an encrypted api-key reference to plaintext.
// It is satisfied by the server settings repo (Get(ctx, key) (string, error)).
type AutoscanSecretResolver interface {
	Get(ctx context.Context, ref string) (string, error)
}

// BuildAutoscanService wires the v2 autoscan engine from its concrete
// dependencies. Both the HTTP router (manual trigger) and the background poll
// task share this constructor so the adapter wiring lives in exactly one place.
func BuildAutoscanService(
	repo *autoscan.Repository,
	pluginService *plugins.Service,
	installationStore *plugins.InstallationStore,
	requestsRepo *mediarequests.Repository,
	secrets AutoscanSecretResolver,
	folders scantrigger.FolderRepository,
	queue autoscanQueuer,
	redisClient *redis.Client,
) *autoscan.Service {
	provider := autoscan.NewPluginProvider(PluginScanSourceAdapter{pluginService})
	connRes := autoscan.NewConnectionResolver(RequestIntegrationLookup{requestsRepo}, secrets)
	return autoscan.NewService(
		repo,
		provider,
		connRes,
		scantrigger.NewResolver(folders),
		queue,
		autoscan.NewRedisSuppressor(redisClient),
		PluginScanSourceLister{installationStore},
	)
}
