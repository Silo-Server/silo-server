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
			token := &huma.Param{Name: streamToken, In: headerParameter, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Stream token a relay forwards; the node passes it unread to the core's resolve, which verifies it. Not a native user access token."}
			op.Parameters = append(op.Parameters, token)
			binary := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: binaryFormat}}
			text := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString}}
			statuses := []int{401, 404, 410, 422, 503}
			switch mount.kind {
			case kindRemux:
				token.Required = true
				statuses = []int{400, 401, 404, 409, 410, 500, 503}
				op.Description = "Every GET and HEAD resolves the transport at the core (POST /api/internal/nodes/self/resolve, purpose remux), passing the stream token unread; a transport this process stopped is refused with 410 first, and a GET for a transport that already has a request running (409) or on a node that has reported itself drained (503) is refused before the core is asked. The core's refusals map to the node's answers: invalid_token 401, wrong_route 503, denied and stopped 410, source_changed 400, not_found 404, anything else (the core unavailable, or its per-node budget answering 429) 503. The token must bind the exact transport and this node's remux-execution/proxy-egress route; audio-downmix recipes require the exact stereo shape; a theme_aac_v1 recipe converts one detail-page theme to AAC. A GET opens the source the resolution grants (a refused grant 400, a mount that does not show the approved revision 409, the core unreachable 503) and reads only that path; a concurrent GET returns 409. GET starts request-scoped MP4 remux with cancellation/stop/reload tracking; a committed 200 can truncate on encoder/read failure, and a source the core withdraws ends it. HEAD validates authority but opens no source and starts no encoder. Process-local fences are not durable across node replacement."
				op.Parameters = append(op.Parameters, &huma.Param{Name: seekQuery, In: queryParameter, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Parsed float; parse errors and negative values return 400. No byte-range resume contract."})
				op.Responses["200"] = &huma.Response{Description: "Progressive remux bytes or HEAD preflight", Content: map[string]*huma.MediaType{mp4: binary, audioMP4: binary}}
			case kindManifest, kindSegment:
				op.Description = "Node bearer protects the worker listener. A live process-local session is served without asking the core; the relays check the deny marker before forwarding. A missing session is rebuilt from the recipe the core resolves (POST /api/internal/nodes/self/resolve, purpose hls, the stream token passed unread) and the source it grants, unless this process stopped the transport, which returns 410 before the core is asked. The core denying the session returns 410, any other refusal 404, a core the node cannot reach 503. Reads refresh liveness; no durable session availability, unscoped reconstruction or native route is promised. "
				if mount.kind == kindManifest {
					op.Parameters = append(op.Parameters, &huma.Param{Name: playback.SourceTimelineQueryParam, In: queryParameter, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Value 1 requests a source-aligned manifest; raw query is preserved in segment links."})
					op.Description += "Build the playback manifest; an unavailable manifest returns 503. No-store response contains relative segment links."
					op.Responses["200"] = &huma.Response{Description: "Playback manifest", Content: map[string]*huma.MediaType{"application/vnd.apple.mpegurl": text}}
				} else {
					op.Parameters = append(op.Parameters, &huma.Param{Name: segmentName, In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}}, &huma.Param{Name: transcodeproxy.RequestHeader, In: headerParameter, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Value 1 defers completion accounting to downstream acknowledgment and returns private generation metadata."})
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
			if mount.kind == kindRemux && method == http.MethodGet {
				op = withJobCapRefusal(op, "a GET for a playback session the node runs a session or progressive remux for, or served a progressive remux for within the last ten minutes (a seek or a reconnect)")
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
