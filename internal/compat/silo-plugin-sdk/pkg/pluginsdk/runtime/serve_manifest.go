package runtime

import (
	"context"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

// manifestRuntime is the default Runtime capability server installed by
// ServeManifest: it answers GetManifest with the embedded manifest, treats
// Configure as a no-op, and wires the host broker so runtime.Host() works.
//
// BindHostBroker is implemented inline (calling the same-package SetHostBrokerID)
// rather than by embedding runtimedefault.Server, which would create a
// runtime -> runtimedefault -> runtime import cycle.
type manifestRuntime struct {
	pluginv1.UnimplementedRuntimeServer
	manifest *pluginv1.PluginManifest
}

func (s *manifestRuntime) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

func (s *manifestRuntime) Configure(context.Context, *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	return &pluginv1.ConfigureResponse{}, nil
}

func (s *manifestRuntime) BindHostBroker(_ context.Context, req *pluginv1.BindHostBrokerRequest) (*pluginv1.BindHostBrokerResponse, error) {
	SetHostBrokerID(req.GetBrokerId())
	return &pluginv1.BindHostBrokerResponse{}, nil
}

// ServeManifest loads + checksums the embedded manifest, installs the default
// manifestRuntime as the Runtime server, and serves the given capability
// servers (the caller supplies only the non-Runtime servers). It never returns;
// a fatal manifest error panics, matching a misbuilt plugin's old main().
func ServeManifest(manifestBytes []byte, version string, servers CapabilityServers) {
	m, err := manifest.LoadWithChecksum(manifestBytes, version)
	if err != nil {
		panic(err)
	}
	servers.Runtime = &manifestRuntime{manifest: m}
	Serve(ServeConfig{Servers: servers})
}
