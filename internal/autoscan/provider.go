package autoscan

import (
	"context"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// ScanSourceProvider yields changed paths for one source. The engine calls
// PollChanges; production wraps the plugins.Service scan_source resolver.
type ScanSourceProvider interface {
	PollChanges(ctx context.Context, pluginID, capabilityID, marker string, conn ResolvedConnection, sourceConfig map[string]string) (changes []Change, nextMarker string, err error)
}

// PollChangesClient is the slice of *pluginhost.ScanSourceClient used here. It
// is exported so wiring in the api package can declare an adapter whose
// ScanSourceClient returns it (Go interface method signatures must match
// exactly across packages, and the unexported form could not be named there).
type PollChangesClient interface {
	PollChanges(ctx context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error)
}

// ScanSourceResolver yields a per-(plugin, capability) scan-source client.
// Exported for the same cross-package adapter reason as PollChangesClient.
type ScanSourceResolver interface {
	ScanSourceClient(ctx context.Context, pluginID, capabilityID string) (PollChangesClient, error)
}

type pluginProvider struct{ resolver ScanSourceResolver }

// NewPluginProvider builds the production scan-source provider over the plugins
// resolver.
func NewPluginProvider(resolver ScanSourceResolver) ScanSourceProvider {
	return &pluginProvider{resolver: resolver}
}

func (p *pluginProvider) PollChanges(ctx context.Context, pluginID, capabilityID, marker string, conn ResolvedConnection, sourceConfig map[string]string) ([]Change, string, error) {
	client, err := p.resolver.ScanSourceClient(ctx, pluginID, capabilityID)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.PollChanges(ctx, &pluginv1.PollChangesRequest{
		CapabilityId: capabilityID,
		Marker:       marker,
		Connection:   &pluginv1.ResolvedConnection{BaseUrl: conn.BaseURL, ApiKey: conn.APIKey},
		SourceConfig: sourceConfig,
	})
	if err != nil {
		return nil, "", err
	}
	if structured := resp.GetChanges(); len(structured) > 0 {
		changes := make([]Change, 0, len(structured))
		for _, change := range structured {
			if change == nil {
				continue
			}
			changes = append(changes, Change{
				SourcePath: change.GetSourcePath(),
				Scope:      scanSourceScope(change.GetScope()),
			})
		}
		return changes, resp.GetNextMarker(), nil
	}

	// Legacy plugins return only source_paths. Treat them as auto/file-like
	// paths so the existing parent-directory collapse behavior is preserved.
	changes := make([]Change, 0, len(resp.GetSourcePaths()))
	for _, path := range resp.GetSourcePaths() {
		changes = append(changes, Change{SourcePath: path, Scope: ChangeScopeAuto})
	}
	return changes, resp.GetNextMarker(), nil
}

func scanSourceScope(scope pluginv1.ScanSourceChangeScope) ChangeScope {
	switch scope {
	case pluginv1.ScanSourceChangeScope_SCAN_SOURCE_CHANGE_SCOPE_FILE:
		return ChangeScopeFile
	case pluginv1.ScanSourceChangeScope_SCAN_SOURCE_CHANGE_SCOPE_SUBTREE:
		return ChangeScopeSubtree
	default:
		return ChangeScopeAuto
	}
}
