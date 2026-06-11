// Package runtimedefault provides a minimal Runtime server that plugins can
// embed to handle BindHostBroker without writing it themselves.
//
// Usage in a plugin:
//
//	type myRuntime struct {
//		runtimedefault.Server
//		// plugin-specific fields
//	}
//
// When the host invokes Runtime.BindHostBroker, the embedded BindHostBroker
// handler stores the broker ID via runtime.SetHostBrokerID. After that point,
// any call to runtime.Host() in capability-handler code returns a working
// *runtimehost.Client.
package runtimedefault

import (
	"context"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

// Server is meant to be embedded into a plugin's Runtime server
// implementation. Its only role is to handle BindHostBroker; plugin authors
// implement GetManifest and Configure on the embedding struct.
type Server struct {
	pluginv1.UnimplementedRuntimeServer
}

// BindHostBroker stores the broker stream ID into the runtime package's
// plugin-side singleton, enabling subsequent runtime.Host() calls to dial the
// silo host's RuntimeHost service.
func (s *Server) BindHostBroker(_ context.Context, req *pluginv1.BindHostBrokerRequest) (*pluginv1.BindHostBrokerResponse, error) {
	sdkruntime.SetHostBrokerID(req.GetBrokerId())
	return &pluginv1.BindHostBrokerResponse{}, nil
}
