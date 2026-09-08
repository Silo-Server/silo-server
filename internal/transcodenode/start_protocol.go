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
		inputParameter   = "input_path"
	)
	request := schemas.Schema(reflect.TypeFor[TranscodeStartRequest](), true, "")
	shape := schemas.Schema(reflect.TypeFor[TranscodeStartRequest](), false, "")
	shape.Required = []string{sessionParameter, inputParameter}
	shape.AdditionalProperties = true
	result := schemas.Schema(reflect.TypeFor[TranscodeStartResponse](), true, "")
	op := workerprotocol.Operation{
		Listener: listener, Method: http.MethodPost, Path: "/transcode/start", Handler: "(*internal/transcodenode.Server).handleStart", AuthClass: bearerClass, RetrySafety: nonRetryable,
		Description: "Start a process-local HLS session after input, recipe, GPU and configuration guards. Ordinary JSON decoding ignores unknown keys and defaults omitted fields; there is no decoder body limit. A same-ID start can tear down and replace an earlier session before failing, and RequireReady may retry early hardware startup failure in software. Monitoring runs asynchronously. No durable admission/replay receipt or automatic caller retry is implied.",
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{jsonMedia: {Schema: request}}},
		Responses: map[string]*huma.Response{
			"202": {Description: "Started session and recipe attestations, encoded as JSON text. The existing handler omits Content-Type; net/http serves text/plain.", Content: map[string]*huma.MediaType{textMedia: {Schema: &huma.Schema{Type: huma.TypeString, Extensions: map[string]any{"contentMediaType": jsonMedia, "contentSchema": result}}}}},
		},
	}
	for _, status := range []int{400, 401, 422, 500, 503} {
		op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{textMedia: {Schema: &huma.Schema{Type: huma.TypeString}}}}
	}
	for _, status := range []string{"422", "503"} {
		op.Responses[status].Headers = map[string]*huma.Param{ToneMapExecutionErrorHeader: {Description: "Existing tone-map refusal code when available.", Schema: &huma.Schema{Type: huma.TypeString}}}
	}
	return op
}
