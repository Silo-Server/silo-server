package proxy

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/bandwidthprobe"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
)

// ProtocolBandwidthProbe describes the media-path connection measurement.
func ProtocolBandwidthProbe() workerprotocol.Operation {
	const (
		listener       = "proxy"
		publicClass    = "public"
		tokenParameter = "token"
		pathParameter  = "path"
		queryParameter = "query"
		binaryFormat   = "binary"
	)
	maximum := float64(bandwidthprobe.MaxSize)
	minimum := float64(1)
	return workerprotocol.Operation{
		Listener: listener, Method: http.MethodGet, Path: "/stream/bandwidth-probe/{token}", Handler: "(*internal/proxy.Server).handleBandwidthProbe", AuthClass: publicClass,
		Description: "Requires an expiring token with bandwidth-probe purpose and an account. Returns random, uncompressed bytes with Cache-Control: no-store. Playback tokens are refused. Limits concurrent probes and bytes per account; rate limits return Retry-After.",
		Parameters: []*huma.Param{
			{Name: tokenParameter, In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}},
			{Name: "size", In: queryParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeInteger, Minimum: &minimum, Maximum: &maximum}},
		},
		Responses: map[string]*huma.Response{
			"200": {Description: "Probe bytes", Content: map[string]*huma.MediaType{"application/octet-stream": {Schema: &huma.Schema{Type: huma.TypeString, Format: binaryFormat}}}},
			"400": {Description: "Invalid size"}, "401": {Description: "Invalid or expired token"}, "403": {Description: "Wrong token purpose or account"},
			"429": {Description: "Rate limited", Headers: map[string]*huma.Param{"Retry-After": {Schema: &huma.Schema{Type: huma.TypeString}}}},
		},
	}
}
