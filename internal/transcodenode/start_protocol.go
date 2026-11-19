package transcodenode

import (
	"net/http"
	"reflect"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolTranscodeStart describes the existing private start/replace command.
func ProtocolTranscodeStart(schemas huma.Registry) workerprotocol.Operation {
	const (
		listener         = "transcode_node"
		bearerClass      = "node_bearer"
		nonRetryable     = "non_retryable"
		textMedia        = "text/plain"
		jsonMedia        = "application/json"
		sessionParameter = "session_id"
		grantParameter   = "source_grant"
	)
	request := schemas.Schema(reflect.TypeFor[TranscodeStartRequest](), true, "")
	shape := schemas.Schema(reflect.TypeFor[TranscodeStartRequest](), false, "")
	shape.Required = []string{sessionParameter, grantParameter}
	shape.AdditionalProperties = true
	result := schemas.Schema(reflect.TypeFor[TranscodeStartResponse](), true, "")
	op := workerprotocol.Operation{
		Listener: listener, Method: http.MethodPost, Path: "/transcode/start", Handler: "(*internal/transcodenode.Server).handleStart", AuthClass: bearerClass, RetrySafety: nonRetryable,
		Description: "Start a process-local HLS session after input, recipe, GPU and configuration guards. Ordinary JSON decoding ignores unknown keys and defaults omitted fields; there is no decoder body limit. A same-ID start can tear down and replace an earlier session before failing, and RequireReady may move an early hardware startup failure to a safer path: under hw_accel=auto CPU decode with GPU encode, then software; otherwise the VideoToolbox software retry. No durable admission/replay receipt or automatic caller retry is implied. source_grant is required: the node exchanges it at the core (POST /api/internal/nodes/self/sources/open), runs FFmpeg on the path the core approves, never on input_path, and keeps the source open for the session's life; a source the core withdraws ends the session. With core source access the input is a loopback relay URL. A missing or refused grant returns 400, a node mount that does not show the approved revision 409, a text subtitle burn-in that cannot be staged on a node reading its source through the core 409 (libass would read the whole file through the core), a core the node cannot reach 503. playback_session_id, user_id, profile_id and media_file_id name the playback session the node files its session record and stream telemetry under.",
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{jsonMedia: {Schema: request}}},
		Responses: map[string]*huma.Response{
			"202": {Description: "Started session and recipe attestations, encoded as JSON text. The existing handler omits Content-Type; net/http serves text/plain.", Content: map[string]*huma.MediaType{textMedia: {Schema: &huma.Schema{Type: huma.TypeString, Extensions: map[string]any{"contentMediaType": jsonMedia, "contentSchema": result}}}}},
		},
	}
	for _, status := range []int{400, 401, 409, 422, 500, 503} {
		op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{textMedia: {Schema: &huma.Schema{Type: huma.TypeString}}}}
	}
	for _, status := range []string{"422", "503"} {
		op.Responses[status].Headers = map[string]*huma.Param{ToneMapExecutionErrorHeader: {Description: "Existing tone-map refusal code when available.", Schema: &huma.Schema{Type: huma.TypeString}}}
	}
	return withJobCapRefusal(op, "a same-id replacement, or a start for a playback_session_id the node already runs a session or progressive remux for")
}
