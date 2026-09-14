// Command residentplugin is a test fixture for the resident plugin
// supervisor and the network access admin API: it declares
// network_access_provider.v1 (so the host treats it as resident), answers
// Connect/Disconnect/GetStatus from an in-memory state, and exits with status
// 3 as soon as the file named by SILO_TEST_PLUGIN_EXIT_FILE exists, so a test
// can make it crash on demand.
package main

import (
	"context"
	_ "embed"
	"os"
	"sync"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

//go:embed manifest.json
var manifestJSON []byte

// version is the manifest version the fixture reports. Tests that need a
// second release of the same plugin build it with
// -ldflags "-X main.version=0.2.0".
var version = "0.1.0"

// provider is a stub overlay: connecting "succeeds" at once with a fixed
// origin, so tests can observe the state each RPC reports.
type provider struct {
	pluginv1.UnimplementedNetworkAccessProviderServer

	mu        sync.Mutex
	connected bool
}

func (p *provider) status() *pluginv1.NetworkAccessStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := &pluginv1.NetworkAccessStatus{State: "disconnected", ProviderVersion: "stub 0.1.0"}
	if p.connected {
		status.State = "connected"
		status.Hostname = "silo.stub.test"
		status.Origin = "https://silo.stub.test"
		status.Addresses = []string{"100.64.0.7"}
		status.Listeners = []*pluginv1.NetworkAccessListener{{Name: "api", Origin: "https://silo.stub.test"}}
		status.DesiredConnected = true
	}
	return status
}

func (p *provider) Connect(context.Context, *pluginv1.NetworkAccessConnectRequest) (*pluginv1.NetworkAccessStatus, error) {
	p.mu.Lock()
	p.connected = true
	p.mu.Unlock()
	return p.status(), nil
}

func (p *provider) Disconnect(context.Context, *pluginv1.NetworkAccessDisconnectRequest) (*pluginv1.NetworkAccessStatus, error) {
	p.mu.Lock()
	p.connected = false
	p.mu.Unlock()
	return p.status(), nil
}

func (p *provider) GetStatus(context.Context, *pluginv1.NetworkAccessGetStatusRequest) (*pluginv1.NetworkAccessStatus, error) {
	return p.status(), nil
}

func main() {
	if exitFile := os.Getenv("SILO_TEST_PLUGIN_EXIT_FILE"); exitFile != "" {
		go func() {
			for {
				if _, err := os.Stat(exitFile); err == nil {
					os.Exit(3)
				}
				time.Sleep(25 * time.Millisecond)
			}
		}()
	}
	sdkruntime.ServeManifest(manifestJSON, version, sdkruntime.CapabilityServers{
		NetworkAccessProvider: &provider{},
	})
}
