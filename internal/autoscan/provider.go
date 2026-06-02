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

// pluginScanSourceClient is the slice of *pluginhost.ScanSourceClient used here.
type pluginScanSourceClient interface {
	PollChanges(ctx context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error)
}

type scanSourceResolver interface {
	ScanSourceClient(ctx context.Context, installationID int, capabilityID string) (pluginScanSourceClient, error)
}

type pluginProvider struct{ resolver scanSourceResolver }

// NewPluginProvider builds the production scan-source provider over the plugins
// resolver.
//
// The resolved connection is delivered to the plugin out-of-band as runtime
// config when the source is configured (the plugin reads its own
// {base_url, api_key}); it is accepted here so a future provider variant could
// inject it per call. For v1 the production path configures the plugin instance
// with the resolved connection at upsert time.
func NewPluginProvider(resolver scanSourceResolver) ScanSourceProvider {
	return &pluginProvider{resolver: resolver}
}

func (p *pluginProvider) PollChanges(ctx context.Context, installationID int, capabilityID, marker string, conn ResolvedConnection) ([]string, string, error) {
	client, err := p.resolver.ScanSourceClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.PollChanges(ctx, &pluginv1.PollChangesRequest{CapabilityId: capabilityID, Marker: marker})
	if err != nil {
		return nil, "", err
	}
	return resp.GetChangedPaths(), resp.GetNextMarker(), nil
}
