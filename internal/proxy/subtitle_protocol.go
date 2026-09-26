package proxy

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolSubtitles retains playback auxiliary namespaces and their signed route authority.
func ProtocolSubtitles(schemas huma.Registry) []workerprotocol.Operation {
	const listener = "proxy"
	const base = "/stream/subtitles/{token}/{track}"
	const plain = "text/plain"
	const octets = "application/octet-stream"
	const public = "public"
	const (
		ifNoneMatch       = "If-None-Match"
		rangeHeader       = "Range"
		ifRange           = "If-Range"
		ifMatch           = "If-Match"
		ifModifiedSince   = "If-Modified-Since"
		ifUnmodifiedSince = "If-Unmodified-Since"
	)
	parameters := []*huma.Param{
		{Name: "token", In: "path", Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Signed playback stream token; routed tuples must select this proxy and an allowed playback recipe. Download tokens are not authority."},
		{Name: "track", In: "path", Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Nonnegative stream index with optional format suffix. ASS preserves styling; SUP selects binary PGS; other suffixes use WebVTT."},
	}
	fonts := workerprotocol.JSONRead[[]playback.SubtitleFontBundleItem](schemas, listener, base+"/fonts", "(*internal/proxy.Server).handleSubtitleFonts", 400, 401, 409, 500, 503)
	// Inventory classifies the outer route as public: the owning handler enforces
	// the signed token, selected proxy and auxiliary playback recipe before I/O.
	fonts.AuthClass = public
	fonts.Parameters = parameters
	fonts.Description = "Signed playback auxiliary authority is required before extracting attached fonts. JSON items contain name and base64 data; empty results are an array. Response is no-store."
	text := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString}}
	binary := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}
	subtitle := workerprotocol.Operation{Listener: listener, Method: http.MethodGet, Path: base, Handler: "(*internal/proxy.Server).handleSubtitle", AuthClass: public, Parameters: parameters,
		Description: "Signed playback auxiliary authority is required. ASS/VTT are buffered; SUP may stream before extraction completes. A cold or windowed SUP request does not promise ranges; cached full SUP uses ServeContent conditions/ranges. A committed 200 can truncate on extraction failure.",
		Responses: map[string]*huma.Response{
			"200": {Description: "Subtitle bytes", Content: map[string]*huma.MediaType{"text/vtt": text, "text/x-ssa": text, octets: binary}},
			"206": {Description: "Cached full SUP byte ranges", Content: map[string]*huma.MediaType{octets: binary, "multipart/byteranges": binary}},
			"304": {Description: "Cached full SUP not modified"},
			"412": {Description: "Cached full SUP precondition failed"},
		},
	}
	for status, response := range fonts.Responses {
		if status != "200" {
			subtitle.Responses[status] = response
		}
	}
	subtitle.Responses["416"] = &huma.Response{Description: "Cached full SUP range not satisfiable", Content: map[string]*huma.MediaType{plain: text}}
	for _, name := range []string{"windowed", "position", "duration"} {
		subtitle.Parameters = append(subtitle.Parameters, &huma.Param{Name: name, In: "query", Schema: &huma.Schema{Type: huma.TypeString}, Description: "Existing PGS window option; interpreted only for SUP."})
	}
	for _, name := range []string{rangeHeader, ifRange, ifMatch, ifNoneMatch, ifModifiedSince, ifUnmodifiedSince} {
		subtitle.Parameters = append(subtitle.Parameters, &huma.Param{Name: name, In: "header", Schema: &huma.Schema{Type: huma.TypeString}, Description: "Conditional/range semantics apply only to cached full SUP."})
	}
	return []workerprotocol.Operation{subtitle, fonts}
}
