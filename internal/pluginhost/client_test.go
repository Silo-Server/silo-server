package pluginhost

import (
	"errors"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	sdkmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

// makeTestClient constructs a Client with the given declared capabilities.
// It creates a lazy (non-connecting) gRPC ClientConn so that accessors which
// call c.rpc.<Capability>() do not panic — no real connection is made.
func makeTestClient(t *testing.T, capabilities []*pluginv1.CapabilityDescriptor) *Client {
	t.Helper()
	return makeTestClientWithManifest(t, &pluginv1.PluginManifest{Capabilities: capabilities})
}

func makeTestClientWithManifest(t *testing.T, manifest *pluginv1.PluginManifest) *Client {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	rpc := sdkruntime.NewClient(conn)
	return newClient(0, rpc, manifest, 0)
}

func TestClientConfigurationChecksRequiresDeclarationAndHealthyClient(t *testing.T) {
	undeclared := makeTestClientWithManifest(t, &pluginv1.PluginManifest{})
	if _, err := undeclared.ConfigurationChecks(); !errors.Is(err, ErrCapabilityNotFound) {
		t.Fatalf("undeclared ConfigurationChecks() err = %v, want ErrCapabilityNotFound", err)
	}

	meta, err := structpb.NewStruct(map[string]any{sdkmanifest.ConfigurationCheckKey: true})
	if err != nil {
		t.Fatal(err)
	}
	declared := makeTestClientWithManifest(t, &pluginv1.PluginManifest{Metadata: meta})
	checks, err := declared.ConfigurationChecks()
	if err != nil || checks == nil {
		t.Fatalf("declared ConfigurationChecks() = (%v, %v), want non-nil client", checks, err)
	}

	declared.markUnhealthy()
	if _, err := declared.ConfigurationChecks(); !errors.Is(err, ErrPluginUnhealthy) {
		t.Fatalf("unhealthy ConfigurationChecks() err = %v, want ErrPluginUnhealthy", err)
	}
}
