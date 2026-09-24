package proxy

import (
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/workerprotocol"
)

// ProtocolThemeAudio describes the routed theme-audio surface. Theme tokens
// are transfers bound to one proxy and one theme recipe; they are refused on
// every video route, and video tokens are refused here.
func ProtocolThemeAudio() []workerprotocol.Operation {
	const (
		plain         = "text/plain"
		header        = "header"
		rangeHeader   = "Range"
		listener      = "proxy"
		pathParameter = "path"
		binaryFormat  = "binary"
		ifMatch       = "If-Match"
		acceptRanges  = "Accept-Ranges"
		notModified   = "Not Modified"
		publicClass   = "public"
		tokenParam    = "token"
		ifRange       = "If-Range"
		ifNoneMatch   = "If-None-Match"
		ifModified    = "If-Modified-Since"
		ifUnmodified  = "If-Unmodified-Since"
		contentType   = "Content-Type"
		contentLength = "Content-Length"
		contentRange  = "Content-Range"
		etag          = "ETag"
		lastModified  = "Last-Modified"
		cacheControl  = "Cache-Control"
	)
	binary := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: binaryFormat}}
	text := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString}}
	var operations []workerprotocol.Operation
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		op := workerprotocol.Operation{
			Listener: listener, Method: method, Path: "/stream/theme/{token}", Handler: "(*internal/proxy.Server).handleThemeAudio", AuthClass: publicClass,
			Description: "Signed, expiring theme token required despite public outer middleware; it must name this proxy as egress and a theme_direct_v1 or theme_aac_v1 recipe. Original themes are served with ServeContent ranges and conditions after the file's size and modification time are checked against the token. AAC conversions stream progressive audio-only fragmented MP4 with no length or ranges, run here or relayed from the reserved transcode node with the seek query forwarded. Theme transfers are not playback sessions.",
			Parameters: []*huma.Param{
				{Name: tokenParam, In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Signed theme authority bound to this proxy, the theme file and its recipe."},
				{Name: "seek", In: "query", Schema: &huma.Schema{Type: huma.TypeNumber}, Description: "Conversion start offset in seconds. Ignored for original themes."},
			},
			Responses: map[string]*huma.Response{},
		}
		for _, name := range []string{rangeHeader, ifRange, ifMatch, ifNoneMatch, ifModified, ifUnmodified} {
			op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: header, Schema: &huma.Schema{Type: huma.TypeString}})
		}
		headers := map[string]*huma.Param{}
		for _, name := range []string{contentType, contentLength, acceptRanges, contentRange, etag, lastModified, cacheControl} {
			headers[name] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}, Description: "Original themes only, except Content-Type and Cache-Control."}
		}
		op.Responses["200"] = &huma.Response{Description: "Original theme, or its progressive AAC conversion", Headers: headers, Content: map[string]*huma.MediaType{"audio/*": binary}}
		op.Responses["206"] = &huma.Response{Description: "Requested byte range of an original theme", Headers: headers, Content: map[string]*huma.MediaType{"audio/*": binary}}
		op.Responses["304"] = &huma.Response{Description: notModified, Headers: headers}
		op.Responses["412"] = &huma.Response{Description: "Precondition Failed", Content: map[string]*huma.MediaType{plain: text}}
		op.Responses["416"] = &huma.Response{Description: "Range Not Satisfiable", Headers: headers, Content: map[string]*huma.MediaType{plain: text}}
		for _, status := range []int{400, 401, 404, 409, 410, 500, 502, 503} {
			op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{plain: text}}
		}
		if method == http.MethodHead {
			op.Description += " HEAD never starts FFmpeg and does not create active transfer tracking. The HTTP server suppresses response bytes."
			for _, response := range op.Responses {
				response.Content = nil
			}
		}
		operations = append(operations, op)
	}
	return operations
}
