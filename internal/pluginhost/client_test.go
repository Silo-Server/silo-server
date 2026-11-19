package pluginhost

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// makeTestClient constructs a Client with the given declared capabilities.
// It creates a lazy (non-connecting) gRPC ClientConn so that accessors which
// call c.rpc.<Capability>() do not panic — no real connection is made.
func makeTestClient(t *testing.T, capabilities []*pluginv1.CapabilityDescriptor) *Client {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	rpc := sdkruntime.NewClient(conn)
	manifest := &pluginv1.PluginManifest{Capabilities: capabilities}
	return newClient(0, rpc, manifest, 0)
}
