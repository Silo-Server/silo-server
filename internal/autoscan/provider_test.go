package autoscan

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// capturingClient records the last PollChangesRequest it was handed.
type capturingClient struct {
	last *pluginv1.PollChangesRequest
}

func (c *capturingClient) PollChanges(_ context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error) {
	c.last = req
	return &pluginv1.PollChangesResponse{SourcePaths: []string{"/mnt/media/x"}, NextMarker: "m2"}, nil
}

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

// TestPluginProviderPopulatesConnection asserts the resolved connection is
// delivered to the plugin on the PollChangesRequest.
func TestPluginProviderPopulatesConnection(t *testing.T) {
	client := &capturingClient{}
	resolver := &capturingResolver{client: client}
	prov := NewPluginProvider(resolver)

	conn := ResolvedConnection{BaseURL: "https://arr.example", APIKey: "secret-key"}
	sourceConfig := map[string]string{"exclusions": ".downloads"}
	changes, next, err := prov.PollChanges(context.Background(), "silo.autoscan.arr", "cap", "m1", conn, sourceConfig)
	if err != nil {
		t.Fatalf("PollChanges: %v", err)
	}
	if len(changes) != 1 || changes[0].SourcePath != "/mnt/media/x" || changes[0].Scope != ChangeScopeAuto || next != "m2" {
		t.Fatalf("unexpected provider result: changes=%v next=%q", changes, next)
	}
	if client.last == nil {
		t.Fatal("expected a PollChangesRequest to be sent")
	}
	if client.last.GetCapabilityId() != "cap" || client.last.GetMarker() != "m1" {
		t.Fatalf("unexpected request fields: cap=%q marker=%q", client.last.GetCapabilityId(), client.last.GetMarker())
	}
	if resolver.lastPluginID != "silo.autoscan.arr" || resolver.lastCapID != "cap" {
		t.Fatalf("resolver identity = %q/%q", resolver.lastPluginID, resolver.lastCapID)
	}
	rc := client.last.GetConnection()
	if rc == nil {
		t.Fatal("expected connection to be populated on the request")
	}
	if rc.GetBaseUrl() != conn.BaseURL || rc.GetApiKey() != conn.APIKey {
		t.Fatalf("connection not delivered: base_url=%q api_key=%q", rc.GetBaseUrl(), rc.GetApiKey())
	}
	if client.last.GetSourceConfig()["exclusions"] != ".downloads" {
		t.Fatalf("source_config not delivered: %#v", client.last.GetSourceConfig())
	}
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
