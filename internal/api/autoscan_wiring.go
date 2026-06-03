package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/catalog"
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
	// A reused Requests connection must honor the integration's live state. The
	// v1 poll gated on `WHERE ri.enabled = true`; here we surface a disabled or
	// unconfigured (blank base_url) integration as an error so the engine turns
	// it into a logged skip / RecordError rather than polling an unusable target.
	if err := checkRequestIntegrationUsable(integrationID, integration.Enabled, integration.BaseURL); err != nil {
		return "", "", err
	}
	return integration.BaseURL, integration.APIKeyRef, nil
}

// checkRequestIntegrationUsable returns a non-nil error when a linked Requests
// integration cannot be polled: it is disabled, or it has no base_url. Extracted
// as a pure function so the gating is unit-testable without a DB-backed repo.
func checkRequestIntegrationUsable(integrationID string, enabled bool, baseURL string) error {
	if !enabled {
		return fmt.Errorf("linked requests integration %q is disabled", integrationID)
	}
	if strings.TrimSpace(baseURL) == "" {
		return fmt.Errorf("linked requests integration %q has no base_url configured", integrationID)
	}
	return nil
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
// capability across ALL installed plugins, regardless of enabled state.
//
// Using List (not ListEnabled) is deliberate for orphan detection: a temporarily
// DISABLED-but-installed plugin must NOT be treated as orphaned. If we dropped
// disabled plugins here, PollOnce would prune their sources and skip them with no
// last_error — they'd vanish silently. Keeping them present means PollOnce still
// attempts them; the plugin client fails to load (not running) and the source
// gets a visible RecordError instead. Only a fully UNINSTALLED plugin (gone from
// List entirely) is a true orphan. The Add-source picker shares this all-installed
// set, which is fine — operators can bind sources against an installed capability.
type PluginScanSourceLister struct {
	Store *plugins.InstallationStore
}

func (l PluginScanSourceLister) ListScanSources(ctx context.Context) ([]autoscan.DiscoveredSource, error) {
	installations, err := l.Store.List(ctx)
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
				PluginID:       inst.PluginID,
				DisplayName:    scanSourceDisplayName(inst.PluginID, c),
			})
		}
	}
	return out, nil
}

// scanSourceDisplayName derives a human-friendly label for a scan_source
// capability: the capability manifest's display_name when present, else the
// plugin id (with the capability id appended when it adds information).
func scanSourceDisplayName(pluginID string, c *plugins.Capability) string {
	if c != nil && c.Metadata != nil {
		if name, ok := c.Metadata["display_name"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	switch {
	case pluginID != "" && c != nil && c.ID != "":
		return pluginID + " / " + c.ID
	case pluginID != "":
		return pluginID
	case c != nil:
		return c.ID
	default:
		return ""
	}
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
	folderRepo *catalog.FolderRepository,
	queue autoscanQueuer,
	redisClient *redis.Client,
) *autoscan.Service {
	provider := autoscan.NewPluginProvider(PluginScanSourceAdapter{pluginService})
	connRes := autoscan.NewConnectionResolver(RequestIntegrationLookup{requestsRepo}, secrets)
	svc := autoscan.NewService(
		repo,
		provider,
		connRes,
		scantrigger.NewResolver(folderRepo),
		queue,
		autoscan.NewRedisSuppressor(redisClient),
		PluginScanSourceLister{installationStore},
	)
	// Wire the connection-test + rewrite-suggester deps: a (long-timeout)
	// arr root-folder/status client and a Silo media-folder lister.
	svc.SetSuggesterDeps(
		autoscan.NewArrRootFolderClient(nil),
		autoscan.NewCatalogFolderLister(folderRepo),
	)
	return svc
}
