package proxy

import (
	"maps"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolDirectPlayback describes signed direct-file delivery on the proxy.
func ProtocolDirectPlayback() []workerprotocol.Operation {
	const (
		listener        = "proxy"
		publicClass     = "public"
		pathParameter   = "path"
		headerParameter = "header"
		textMedia       = "text/plain"
		binaryFormat    = "binary"
		tokenParameter  = "token"
		acceptRanges    = "Accept-Ranges"
		contentLength   = "Content-Length"
		contentRange    = "Content-Range"
		entityTag       = "ETag"
		modified        = "Last-Modified"
		multipartRanges = "multipart/byteranges"
	)
	var operations []workerprotocol.Operation
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		binary := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: binaryFormat}}
		media := map[string]*huma.MediaType{}
		for _, extension := range []string{".mp4", ".mkv", ".webm", ".avi", ".mov", ".ts", ".flv", ".wmv", ".m4a", ".mp3", ".flac", ".ogg", ".wav", ".aac", ".unknown"} {
			media[playback.MimeFromExtension(extension)] = binary
		}
		rangeMedia := maps.Clone(media)
		rangeMedia[multipartRanges] = binary
		op := workerprotocol.Operation{
			Listener: listener, Method: method, Path: "/stream/direct/{token}", Handler: "(*internal/proxy.Server).handleDirectPlay", AuthClass: publicClass,
			Description: "Signed direct-play token required despite public outer middleware. Committed routing must select this proxy and direct-play recipe; legacy empty routing retains only bounded original method authority. Delegates to ServeDirectPlay with file-derived strong ETag, standard conditional/range handling, rolling write deadlines and viewer tracking. Partial routing returns 409; wrong egress or recipe family returns 503. Read interruption may truncate a committed response; no durable transfer or reconstruction guarantee.",
			Parameters:  []*huma.Param{{Name: tokenParameter, In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Signed expiring direct-play authority; download/remux/transcode tokens are not interchangeable."}},
			Responses: map[string]*huma.Response{
				"200": {Description: "Direct file bytes", Content: media},
				"206": {Description: "Single or multipart byte ranges", Content: rangeMedia},
				"304": {Description: "Not Modified"}, "412": {Description: "Precondition Failed"},
			},
		}
		for _, name := range directProtocolConditions() {
			op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: headerParameter, Schema: &huma.Schema{Type: huma.TypeString}})
		}
		for _, status := range []int{401, 404, 409, 416, 500, 503} {
			op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{textMedia: {Schema: &huma.Schema{Type: huma.TypeString}}}}
		}
		headers := map[string]*huma.Param{}
		for _, name := range []string{entityTag, modified, acceptRanges, contentRange, contentLength} {
			headers[name] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}, Description: "ServeDirectPlay representation/range metadata when applicable."}
		}
		for _, status := range []string{"200", "206", "304", "412", "416"} {
			op.Responses[status].Headers = headers
		}
		if method == http.MethodHead {
			op.Description += " HEAD suppresses response bytes but retains existing viewer tracking."
			for _, response := range op.Responses {
				response.Content = nil
			}
		}
		operations = append(operations, op)
	}
	return operations
}

func directProtocolConditions() []string {
	const (
		rangeHeader       = "Range"
		ifRange           = "If-Range"
		ifMatch           = "If-Match"
		ifNoneMatch       = "If-None-Match"
		ifModifiedSince   = "If-Modified-Since"
		ifUnmodifiedSince = "If-Unmodified-Since"
	)
	return []string{rangeHeader, ifRange, ifMatch, ifNoneMatch, ifModifiedSince, ifUnmodifiedSince}
}
