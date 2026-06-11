package runtime

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// Compile-time: manifestRuntime satisfies the Runtime server contract.
var _ pluginv1.RuntimeServer = (*manifestRuntime)(nil)

func TestManifestRuntimeServesManifestAndConfigure(t *testing.T) {
	m := &pluginv1.PluginManifest{PluginId: "silo.test", Version: "1.0.0"}
	rt := &manifestRuntime{manifest: m}

	resp, err := rt.GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil || resp.GetManifest().GetPluginId() != "silo.test" {
		t.Fatalf("GetManifest: resp=%v err=%v", resp, err)
	}
	if _, err := rt.Configure(context.Background(), &pluginv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	bresp, err := rt.BindHostBroker(context.Background(), &pluginv1.BindHostBrokerRequest{BrokerId: 7})
	if err != nil || bresp == nil {
		t.Fatalf("BindHostBroker: resp=%v err=%v", bresp, err)
	}
}
