package autoscan

import (
	"context"

	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// capturingResolver yields a fixed PollChangesClient and records the requested
// stable scan-source identity.
type capturingResolver struct {
	client       PollChangesClient
	lastPluginID string
	lastCapID    string
}

func (r *capturingResolver) ScanSourceClient(_ context.Context, pluginID, capabilityID string) (PollChangesClient, error) {
	r.lastPluginID = pluginID
	r.lastCapID = capabilityID
	return r.client, nil
}

func TestPluginProviderPrefersStructuredChanges(t *testing.T) {
	client := &capturingStructuredClient{}
	prov := NewPluginProvider(&capturingResolver{client: client})

	changes, next, err := prov.PollChanges(context.Background(), "silo.autoscan.arr", "cap", "m1", ResolvedConnection{}, nil)
	if err != nil {
		t.Fatalf("PollChanges: %v", err)
	}
	if next != "m3" || len(changes) != 2 {
		t.Fatalf("unexpected provider result: changes=%v next=%q", changes, next)
	}
	if changes[0] != (Change{SourcePath: "/ceph/movie/Movie", Scope: ChangeScopeSubtree}) {
		t.Fatalf("first change = %#v", changes[0])
	}
	if changes[1] != (Change{SourcePath: "/ceph/show/S01/E01.mkv", Scope: ChangeScopeFile}) {
		t.Fatalf("second change = %#v", changes[1])
	}
}

type capturingStructuredClient struct {
	last *pluginv1.PollChangesRequest
}

func (c *capturingStructuredClient) PollChanges(_ context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error) {
	c.last = req
	return &pluginv1.PollChangesResponse{
		SourcePaths: []string{"/legacy/ignored.mkv"},
		NextMarker:  "m3",
		Changes: []*pluginv1.ScanSourceChange{
			{
				SourcePath: "/ceph/movie/Movie",
				Scope:      pluginv1.ScanSourceChangeScope_SCAN_SOURCE_CHANGE_SCOPE_SUBTREE,
			},
			{
				SourcePath: "/ceph/show/S01/E01.mkv",
				Scope:      pluginv1.ScanSourceChangeScope_SCAN_SOURCE_CHANGE_SCOPE_FILE,
			},
		},
	}, nil
}
