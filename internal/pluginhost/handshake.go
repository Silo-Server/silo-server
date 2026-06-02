package pluginhost

import (
	"time"

	"github.com/hashicorp/go-plugin"

	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

const (
	DefaultHealthCheckInterval = 30 * time.Second
	DefaultHealthFailureLimit  = 3
	DefaultMetadataTimeout     = 30 * time.Second
	DefaultAnalyzerTimeout     = 5 * time.Minute
	DefaultControlTimeout      = 10 * time.Second
	// DefaultScanSourceTimeout covers a single PollChanges call, which makes the
	// plugin poll an external arr API (Sonarr/Radarr can take ~30s); the generic
	// 10s control timeout would risk spurious timeouts.
	DefaultScanSourceTimeout = 2 * time.Minute
	DefaultEventTimeout      = 10 * time.Second
	DefaultAuthTimeout       = 10 * time.Second
	DefaultRouteTimeout      = 10 * time.Second
)

func HandshakeConfig() plugin.HandshakeConfig {
	return sdkruntime.HandshakeConfig()
}
