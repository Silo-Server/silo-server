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
	)
}
