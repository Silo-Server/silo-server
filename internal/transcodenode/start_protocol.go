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
		Description: "Start a process-local HLS session after input, recipe, GPU and configuration guards. The input is the source source_grant names, opened through the core (POST /api/internal/nodes/self/sources/open) and renewed for the session's life; input_path is informational. A text subtitle burn-in from a core-access source has its track staged on the node, detached from the request and shared by later starts on the same source and track: the start returns once staging has begun, FFmpeg starts when it completes, and manifest and segment requests wait meanwhile as for a slow FFmpeg start (RequireReady waits for the manifest as usual, and a staging that outlasts that wait fails the start like any readiness failure while the staging goes on for a retry). A node runs two stagings at a time; a start needing another waits for one within its request, behind a bounded queue, and answers 503 when the queue or the staging cache's size bound is full. Ordinary JSON decoding ignores unknown keys and defaults omitted fields; there is no decoder body limit. A same-ID start can tear down and replace an earlier session before failing, and RequireReady may move an early hardware startup failure to a safer path: under hw_accel=auto CPU decode with GPU encode, then software; otherwise the VideoToolbox software retry. Monitoring runs asynchronously. No durable admission/replay receipt or automatic caller retry is implied.",
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{jsonMedia: {Schema: request}}},
		Responses: map[string]*huma.Response{
			"202": {Description: "Started session and recipe attestations, encoded as JSON text. The existing handler omits Content-Type; net/http serves text/plain.", Content: map[string]*huma.MediaType{textMedia: {Schema: &huma.Schema{Type: huma.TypeString, Extensions: map[string]any{"contentMediaType": jsonMedia, "contentSchema": result}}}}},
		},
	}
	for _, status := range []int{400, 401, 409, 422, 500, 503} {
		op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{textMedia: {Schema: &huma.Schema{Type: huma.TypeString}}}}
	}
	op.Responses["409"].Description = localSourceMismatchDescription
	for _, status := range []string{"422", "503"} {
		op.Responses[status].Headers = map[string]*huma.Param{ToneMapExecutionErrorHeader: {Description: "Existing tone-map refusal code when available.", Schema: &huma.Schema{Type: huma.TypeString}}}
	}
	return op
}
