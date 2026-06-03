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

// capturingResolver yields a fixed capturingClient.
type capturingResolver struct{ client *capturingClient }

func (r capturingResolver) ScanSourceClient(context.Context, int, string) (PollChangesClient, error) {
	return r.client, nil
}

// TestPluginProviderPopulatesConnection asserts the resolved connection is
// delivered to the plugin on the PollChangesRequest.
func TestPluginProviderPopulatesConnection(t *testing.T) {
	client := &capturingClient{}
	prov := NewPluginProvider(capturingResolver{client})

	conn := ResolvedConnection{BaseURL: "https://arr.example", APIKey: "secret-key"}
	paths, next, err := prov.PollChanges(context.Background(), 1, "cap", "m1", conn)
	if err != nil {
		t.Fatalf("PollChanges: %v", err)
	}
	if len(paths) != 1 || next != "m2" {
		t.Fatalf("unexpected provider result: paths=%v next=%q", paths, next)
	}
	if client.last == nil {
		t.Fatal("expected a PollChangesRequest to be sent")
	}
	if client.last.GetCapabilityId() != "cap" || client.last.GetMarker() != "m1" {
		t.Fatalf("unexpected request fields: cap=%q marker=%q", client.last.GetCapabilityId(), client.last.GetMarker())
	}
	rc := client.last.GetConnection()
	if rc == nil {
		t.Fatal("expected connection to be populated on the request")
	}
	if rc.GetBaseUrl() != conn.BaseURL || rc.GetApiKey() != conn.APIKey {
		t.Fatalf("connection not delivered: base_url=%q api_key=%q", rc.GetBaseUrl(), rc.GetApiKey())
	}
}
