package transcodenode

import (
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolStreaming describes retained node-side progressive and HLS delivery.
func ProtocolStreaming() []workerprotocol.Operation {
	const (
		kindRemux         = "remux"
		kindManifest      = "manifest"
		kindSegment       = "segment"
		mp4               = "video/mp4"
		audioMP4          = "audio/mp4"
		segmentName       = "name"
		seekQuery         = "seek"
		queryParameter    = "query"
		rangeHeader       = "Range"
		ifRange           = "If-Range"
		ifMatch           = "If-Match"
		ifNoneMatch       = "If-None-Match"
		ifModifiedSince   = "If-Modified-Since"
		ifUnmodifiedSince = "If-Unmodified-Since"
		anyMedia          = "*/*"
		listener          = "transcode_node"
		bearerClass       = "node_bearer"
		pathParameter     = "path"
		headerParameter   = "header"
		sessionParameter  = "session_id"
		textMedia         = "text/plain"
		streamToken       = "X-Silo-Stream-Token"
		binaryFormat      = "binary"
	)
	mounts := []struct {
		path, handler, kind string
		methods             []string
	}{
		{"/remux/{session_id}", "handleRemux", kindRemux, []string{http.MethodGet, http.MethodHead}},
		{"/transcode/{session_id}/master.m3u8", "handleManifest", kindManifest, []string{http.MethodGet}},
		{"/transcode/{session_id}/segment/{name}", "handleSegment", kindSegment, []string{http.MethodGet}},
	}
	var operations []workerprotocol.Operation
	for _, mount := range mounts {
		for _, method := range mount.methods {
			op := workerprotocol.Operation{Listener: listener, Method: method, Path: mount.path, Handler: "(*internal/transcodenode.Server)." + mount.handler, AuthClass: bearerClass, Responses: map[string]*huma.Response{}, Parameters: []*huma.Param{{Name: sessionParameter, In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}}}}
			token := &huma.Param{Name: streamToken, In: headerParameter, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Signed recipe reference for node-side validation and deny-marker lookup; not a native user access token."}
			op.Parameters = append(op.Parameters, token)
			binary := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: binaryFormat}}
			text := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString}}
			statuses := []int{401, 404, 410, 422, 503}
			switch mount.kind {
			case kindRemux:
				token.Required = true
				statuses = []int{400, 401, 404, 409, 410, 500, 503}
				op.Description = "Node bearer plus independently verified playback recipe token must bind exact transport and this node's remux-execution/proxy-egress route. Audio-downmix recipes require the exact stereo shape; input authority and current configuration are checked. A session carrying the deny marker, or revoked/stopped authority, returns410 and a concurrent GET returns409. GET starts request-scoped MP4 remux with cancellation/stop/reload tracking; committed200 can truncate on encoder/read failure. HEAD validates authority but does not check actual file existence or start an encoder. Process-local fences are not durable across node replacement."
				op.Parameters = append(op.Parameters, &huma.Param{Name: seekQuery, In: queryParameter, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Parsed float; parse errors and negative values return400. No byte-range resume contract."})
				op.Responses["200"] = &huma.Response{Description: "Progressive remux bytes or HEAD preflight", Content: map[string]*huma.MediaType{mp4: binary, audioMP4: binary}}
			case kindManifest, kindSegment:
				op.Description = "Node bearer protects the worker listener. A session carrying the deny marker returns410 and is neither served nor reconstructed. Missing process-local sessions may reconstruct through existing signed-token/stored-recipe, input and execution authority guards; a missing/refused reconstruction can return404. Reads refresh liveness; no durable session availability, unscoped reconstruction or native route is promised. "
				if mount.kind == kindManifest {
					op.Parameters = append(op.Parameters, &huma.Param{Name: playback.SourceTimelineQueryParam, In: queryParameter, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Value1 requests source-aligned manifest; raw query is preserved in segment links."})
					op.Description += "Build the playback manifest; unavailable manifest returns503. No-store response contains relative segment links."
					op.Responses["200"] = &huma.Response{Description: "Playback manifest", Content: map[string]*huma.MediaType{"application/vnd.apple.mpegurl": text}}
				} else {
					op.Parameters = append(op.Parameters, &huma.Param{Name: segmentName, In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}}, &huma.Param{Name: transcodeproxy.RequestHeader, In: headerParameter, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Value1 defers completion accounting to downstream acknowledgment and returns private generation metadata."})
					for _, name := range []string{rangeHeader, ifRange, ifMatch, ifNoneMatch, ifModifiedSince, ifUnmodifiedSince} {
						op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: headerParameter, Schema: &huma.Schema{Type: huma.TypeString}})
					}
					op.Description += "Open an exact segment lease; missing media can wait or trigger guarded seek/restart recovery. Non-numbered init data can also wait. ServeContent supplies extension/sniffed media, conditions and ranges. Private generation is emitted only on a marked proxy hop. Direct complete GET reports generation-bound completion; proxied reads await separate acknowledgment. Partial/failed responses do not advance completion and may truncate after headers."
					for _, status := range []string{"200", "206"} {
						op.Responses[status] = &huma.Response{Description: "Leased segment bytes with standard conditions/ranges", Content: map[string]*huma.MediaType{anyMedia: binary}, Headers: map[string]*huma.Param{transcodeproxy.GenerationHeader: {Schema: &huma.Schema{Type: huma.TypeString}, Description: "Private generation only for marked proxy hops; never client-facing."}}}
					}
					op.Responses["304"] = &huma.Response{Description: "Not Modified"}
					op.Responses["412"] = &huma.Response{Description: "Precondition Failed"}
					statuses = append(statuses, 416, 500)
				}
			}
			for _, status := range statuses {
				op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{textMedia: text}}
			}
			if mount.kind != kindRemux {
				for _, status := range []string{"422", "503"} {
					op.Responses[status].Headers = map[string]*huma.Param{ToneMapExecutionErrorHeader: {Schema: &huma.Schema{Type: huma.TypeString}, Description: "Existing tone-map refusal code when available."}}
				}
			}
			if method == http.MethodHead {
				for _, response := range op.Responses {
					response.Content = nil
				}
			}
			operations = append(operations, op)
		}
	}
	return operations
}
