// Command exitingplugin is a test fixture for the host's exit watcher: a
// minimal metadata provider that exits with status 3 as soon as a file named
// exit-now appears next to its binary. It also exits with status 4 at startup
// when it can see SILO_TEST_HOST_ONLY, which only the host's environment
// carries.
package main

import (
	_ "embed"
	"os"
	"path/filepath"
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
	if _, ok := os.LookupEnv("SILO_TEST_HOST_ONLY"); ok {
		os.Exit(4)
	}
	exitFile := filepath.Join(filepath.Dir(os.Args[0]), "exit-now")
	go func() {
		for {
			if _, err := os.Stat(exitFile); err == nil {
				os.Exit(3)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
	sdkruntime.ServeManifest(manifestJSON, "0.1.0", sdkruntime.CapabilityServers{
		MetadataProvider: &metadataServer{},
	})
}
