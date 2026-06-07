package pluginhost

import (
	"time"

	"github.com/hashicorp/go-plugin"

	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

const (
	DefaultHealthCheckInterval   = 30 * time.Second
	DefaultHealthFailureLimit    = 3
	DefaultMetadataTimeout       = 30 * time.Second
	DefaultMarkerProviderTimeout = 30 * time.Second
	DefaultAnalyzerTimeout       = 5 * time.Minute
	DefaultControlTimeout        = 10 * time.Second
	// DefaultScanSourceTimeout covers a single PollChanges call. Most scan
	// sources return quickly, but filesystem-backed sources may need a longer
	// bounded window for an initial baseline; cooperative plugins should still
	// checkpoint progress and return before this deadline.
	DefaultScanSourceTimeout = 5 * time.Minute
	DefaultEventTimeout      = 10 * time.Second
	DefaultAuthTimeout       = 10 * time.Second
	DefaultRouteTimeout      = 10 * time.Second
)

func HandshakeConfig() plugin.HandshakeConfig {
	return sdkruntime.HandshakeConfig()
}
