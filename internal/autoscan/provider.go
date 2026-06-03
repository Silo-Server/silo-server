package autoscan

import (
	"context"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// ScanSourceProvider yields changed paths for one source. The engine calls
// PollChanges; production wraps the plugins.Service scan_source resolver.
type ScanSourceProvider interface {
	PollChanges(ctx context.Context, installationID int, capabilityID, marker string, conn ResolvedConnection) (paths []string, nextMarker string, err error)
}

// PollChangesClient is the slice of *pluginhost.ScanSourceClient used here. It
// is exported so wiring in the api package can declare an adapter whose
// ScanSourceClient returns it (Go interface method signatures must match
// exactly across packages, and the unexported form could not be named there).
type PollChangesClient interface {
	PollChanges(ctx context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error)
}

// ScanSourceResolver yields a per-(installation, capability) scan-source client.
// Exported for the same cross-package adapter reason as PollChangesClient.
type ScanSourceResolver interface {
	ScanSourceClient(ctx context.Context, installationID int, capabilityID string) (PollChangesClient, error)
}

type pluginProvider struct{ resolver ScanSourceResolver }

// NewPluginProvider builds the production scan-source provider over the plugins
// resolver.
func NewPluginProvider(resolver ScanSourceResolver) ScanSourceProvider {
	return &pluginProvider{resolver: resolver}
}

func (p *pluginProvider) PollChanges(ctx context.Context, installationID int, capabilityID, marker string, conn ResolvedConnection) ([]string, string, error) {
	client, err := p.resolver.ScanSourceClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.PollChanges(ctx, &pluginv1.PollChangesRequest{
		CapabilityId: capabilityID,
		Marker:       marker,
		Connection:   &pluginv1.ResolvedConnection{BaseUrl: conn.BaseURL, ApiKey: conn.APIKey},
	})
	if err != nil {
		return nil, "", err
	}
	// The merged scan_source contract renamed changed_paths -> source_paths: the
	// plugin now returns RAW source-namespace paths. The host applies per-source
	// path rewrites (see service.PollOnce) before resolving/enqueueing them.
	return resp.GetSourcePaths(), resp.GetNextMarker(), nil
}
