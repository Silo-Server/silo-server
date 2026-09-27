// Command exitingplugin is a test fixture for the host's exit watcher: a
// minimal metadata provider that exits with status 3 as soon as the file
// named by SILO_TEST_PLUGIN_EXIT_FILE exists.
package main

import (
	_ "embed"
	"os"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

//go:embed manifest.json
var manifestJSON []byte

type metadataServer struct {
	pluginv1.UnimplementedMetadataProviderServer
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
	sdkruntime.ServeManifest(manifestJSON, "0.1.0", sdkruntime.CapabilityServers{
		MetadataProvider: &metadataServer{},
	})
}
