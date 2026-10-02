package proxy

import (
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolDownloads describes the signed download-token surface, which is
// distinct from playback egress/grant authorization and node bearer commands.
func ProtocolDownloads() []workerprotocol.Operation {
	const (
		plain              = "text/plain"
		publicClass        = "public"
		rangeHeader        = "Range"
		listener           = "proxy"
		pathParameter      = "path"
		anyMedia           = "*/*"
		conditionNoneMatch = "If-None-Match"
		header             = "header"
	)
	binary := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}
	text := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString}}
	var operations []workerprotocol.Operation
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		op := workerprotocol.Operation{
			Listener: listener, Method: method, Path: "/downloads/file/{token}", Handler: "(*internal/proxy.Server).handleDownloadFile", AuthClass: publicClass,
			Description: "Signed, expiring download or attested-download token required despite public outer middleware. Playback tokens are refused. Local files use extension-derived media and ServeContent conditions/ranges; remote artifacts use a node-bearer hop with filtered headers. Attested remote delivery must match the token fingerprint and size. Missing remote artifacts may report exact node/artifact unavailability; read interruption can truncate committed bytes. No preparation, durable transfer or automatic replay is implied.",
			Parameters:  []*huma.Param{{Name: "token", In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Signed download authority, including local path or remote artifact and optional exact execution attestation."}},
			Responses:   map[string]*huma.Response{},
		}
		for _, name := range []string{rangeHeader, "If-Range", "If-Match", conditionNoneMatch, "If-Modified-Since", "If-Unmodified-Since"} {
			op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: header, Schema: &huma.Schema{Type: huma.TypeString}})
		}
		headers := map[string]*huma.Param{}
		for _, name := range []string{"Content-Type", "Content-Disposition", "Content-Length", "Accept-Ranges", "Content-Range", "ETag", "Last-Modified"} {
			headers[name] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}, Description: "Content or conditional metadata when available; local downloads do not generate a strong ETag."}
		}
		// The existing remote relay explicitly accepts every 2xx. The wildcard
		// media reflects local extension media and the filtered origin Content-Type.
		op.Responses["2XX"] = &huma.Response{Description: "File or allowed remote response; 200 full bytes, 206 single/multipart ranges. Other remote 2xx are preserved.", Headers: headers, Content: map[string]*huma.MediaType{anyMedia: binary}}
		op.Responses["304"] = &huma.Response{Description: "Not Modified", Headers: headers}
		op.Responses["412"] = &huma.Response{Description: "Precondition Failed; remote response body may be forwarded", Content: map[string]*huma.MediaType{anyMedia: binary}}
		op.Responses["416"] = &huma.Response{Description: "Range Not Satisfiable; remote response body may be forwarded", Headers: headers, Content: map[string]*huma.MediaType{anyMedia: binary}}
		for _, status := range []int{401, 404, 500, 502, 503} {
			op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{plain: text}}
		}
		if method == http.MethodHead {
			op.Description += " HEAD is a preflight and does not create active transfer tracking. The HTTP server suppresses response bytes."
			for _, response := range op.Responses {
				response.Content = nil
			}
		}
		operations = append(operations, op)
	}
	return operations
}
