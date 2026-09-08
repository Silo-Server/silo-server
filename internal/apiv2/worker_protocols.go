package apiv2

import (
	"reflect"
	"strings"

	"github.com/Silo-Server/silo-server/internal/proxy"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

const workerProtocolsExtension = "x-silo-worker-protocols"

type workerProtocolRegistry struct {
	Operations []workerprotocol.Operation `json:"operations"`
	Schemas    map[string]*huma.Schema    `json:"schemas"`
}

// Worker paths cannot enter the native Paths map: identical paths can describe
// different listeners and they are not served by the API. Keep the owning
// registry in the single artifact, with locally scoped schema references.
func describeWorkerProtocols() workerProtocolRegistry {
	schemas := huma.NewMapRegistry("#/"+workerProtocolsExtension+"/schemas/", func(t reflect.Type, hint string) string {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		name := huma.DefaultSchemaNamer(t, hint)
		if pkg := t.PkgPath(); pkg != "" {
			parts := strings.Split(pkg, "/")
			return parts[len(parts)-1] + "_" + name
		}
		return name
	})
	operations := append(proxy.ProtocolReads(schemas), transcodenode.ProtocolReads(schemas)...)
	operations = append(operations, proxy.ProtocolControls(schemas)...)
	operations = append(operations, transcodenode.ProtocolControls(schemas)...)
	operations = append(operations, transcodenode.ProtocolChapterExtraction(schemas))
	operations = append(operations, transcodenode.ProtocolArtifacts()...)
	operations = append(operations, proxy.ProtocolSubtitles(schemas)...)
	operations = append(operations, transcodenode.ProtocolSegmentAcknowledgement())
	operations = append(operations, transcodenode.ProtocolDownloadPreparation(schemas))
	operations = append(operations, proxy.ProtocolDownloads()...)
	operations = append(operations, transcodenode.ProtocolLegacyStop())
	operations = append(operations, transcodenode.ProtocolTranscodeStart(schemas))
	operations = append(operations, proxy.ProtocolDirectPlayback()...)
	operations = append(operations, proxy.ProtocolStreaming()...)
	operations = append(operations, transcodenode.ProtocolStreaming()...)
	return workerProtocolRegistry{Operations: operations, Schemas: schemas.Map()}
}
