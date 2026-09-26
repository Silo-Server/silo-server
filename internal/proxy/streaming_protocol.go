package proxy

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolStreaming describes the remaining proxy remux/HLS token and grant
// routes. Only these existing relays retain open-ended origin responses.
func ProtocolStreaming() []workerprotocol.Operation {
	const (
		kindRemux           = "remux"
		kindAudio           = "audio"
		kindManifest        = "manifest"
		kindSegment         = "segment"
		kindIdentity        = "identity"
		segmentName         = "name"
		seekQuery           = "seek"
		queryParameter      = "query"
		authorizationHeader = "Authorization"
		listener            = "proxy"
		publicClass         = "public"
		pathParameter       = "path"
		headerParameter     = "header"
		tokenParameter      = "token"
		sessionParameter    = "session_id"
		anyMedia            = "*/*"
		binaryFormat        = "binary"
	)
	mounts := []struct {
		path, handler, kind string
		methods             []string
	}{
		{"/stream/remux/{token}", "handleRemux", kindRemux, []string{http.MethodGet, http.MethodHead}},
		{"/stream/remux/audio-v2/{token}", "handleAudioV2Remux", kindAudio, []string{http.MethodGet, http.MethodHead}},
		{"/stream/transcode/{token}/master.m3u8", "handleTranscodeManifest", kindManifest, []string{http.MethodGet, http.MethodHead}},
		{"/stream/transcode/{token}/segment/{name}", "handleTranscodeSegment", kindSegment, []string{http.MethodGet}},
		{"/stream/v3/{session_id}", "handleGrantIdentity", kindIdentity, []string{http.MethodGet, http.MethodHead}},
		{"/stream/v3/{session_id}/master.m3u8", "handleGrantTranscodeManifest", kindManifest, []string{http.MethodGet, http.MethodHead}},
		{"/stream/v3/{session_id}/segment/{name}", "handleGrantTranscodeSegment", kindSegment, []string{http.MethodGet}},
	}
	var operations []workerprotocol.Operation
	for _, mount := range mounts {
		grant := strings.Contains(mount.path, "/v3/")
		for _, method := range mount.methods {
			op := workerprotocol.Operation{Listener: listener, Method: method, Path: mount.path, Handler: "(*internal/proxy.Server)." + mount.handler, AuthClass: publicClass, Responses: map[string]*huma.Response{}}
			op.Description = "Outer public middleware does not authorize content. Signed expiring playback token must select the committed proxy egress and the endpoint recipe family; partial routes fail409 and wrong egress/family fails503. Empty legacy routing retains only original method authority. "
			parameter := tokenParameter
			if grant {
				parameter = sessionParameter
				op.Description = "Header-only bearer access JWT, current login-session validity and a stored grant owned by the same account are required on every request. No API key, cookie or query-token fallback. Missing grant404; wrong account403; unavailable dependencies503. Committed proxy egress and recipe family remain enforced. Local authorization errors are application/json objects with error and message; downstream/local media failures retain their existing media. This does not add a profile/PIN check or native v2 alias. "
				op.Parameters = append(op.Parameters, &huma.Param{Name: authorizationHeader, In: headerParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Bearer viewer access token; relay mints a separate node-facing playback token from the grant."})
			}
			op.Parameters = append(op.Parameters, &huma.Param{Name: parameter, In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}})
			switch mount.kind {
			case kindRemux, kindAudio, kindIdentity:
				op.Parameters = append(op.Parameters, &huma.Param{Name: seekQuery, In: queryParameter, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Existing progressive seek interpretation; proxy-local and remote-node validation differ."})
				op.Description += "Progressive remux streams MP4/audio MP4 locally or relays to the selected transcode transport. Local seek parse failure defaults to zero; local Range/conditional headers are not a resumable-file contract. Local proxy HEAD still starts and drains remux work; remote-node HEAD validates authority without starting an encoder. Failures after200 can truncate output. "
				if mount.kind == kindRemux {
					op.Description += "Audio-downmix-v2 recipes are refused404 here and require the versioned audio route. "
				}
				if mount.kind == kindAudio {
					op.Description += "Only the exact versioned stereo AAC downmix shape is accepted; ordinary/partial recipes return404. "
				}
				if mount.kind == kindIdentity {
					op.Description += "Identity dispatch selects remux only for the literal remux play method; after the endpoint family gate other accepted non-transcode methods fall through to direct-file serving. Boosted-remux grant compatibility is not established by this description. Direct files retain strongETag/ranges. "
				}
			case kindManifest:
				op.Description += "HLS manifest uses the bound transport ID (legacy fallback session ID), forwarding raw query to the node. No manifest Range/conditional request headers are forwarded. Session tracking is refreshed by access. "
			case kindSegment:
				op.Parameters = append(op.Parameters, &huma.Param{Name: segmentName, In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}})
				for _, name := range directProtocolConditions() {
					op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: headerParameter, Schema: &huma.Schema{Type: huma.TypeString}})
				}
				op.Description += "Segment relay forwards representation conditions and marks the private proxy hop. Only a complete downstream media-segment response attempts exact-generation acknowledgement; failure is logged and is not a durable completion receipt. Partial/failed reads do not acknowledge. "
			}
			op.Description += "The final origin response status and headers are forwarded except the private generation header; this relay is not restricted to a finite status/media list. Request construction failure500, missing origin400 and transport failure502 remain local. Read interruption can truncate committed bytes. No durable transfer, generic retry promise or websocket tunnel is advertised."
			body := map[string]*huma.MediaType{anyMedia: {Schema: &huma.Schema{Type: huma.TypeString, Format: binaryFormat}}}
			op.Responses["default"] = &huma.Response{Description: "Final origin response status, headers and bytes; generation metadata stays private.", Content: body}
			for _, status := range []int{200, 400, 401, 403, 404, 409, 500, 502, 503} {
				if status == 403 && !grant {
					continue
				}
				op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status) + " from local handling or the final origin; media remains response-specific.", Content: body}
			}
			if method == http.MethodHead {
				op.Description += " The HTTP server suppresses all HEAD response bytes."
				for _, response := range op.Responses {
					response.Content = nil
				}
			}
			operations = append(operations, op)
		}
	}
	return operations
}
