package transcodenode

import (
	"net/http"
	"reflect"

	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolChapterExtraction describes the existing JSON request/JPEG response
// boundary. JSON error bodies coexist with text errors from worker authority.
func ProtocolChapterExtraction(schemas huma.Registry) workerprotocol.Operation {
	const (
		listener  = "transcode_node"
		jsonMedia = "application/json"
		textMedia = "text/plain"
	)
	request := schemas.Schema(reflect.TypeFor[chapterthumbs.RemoteExtractRequest](), true, "")
	// The worker decodes ordinary JSON: only input_path is required; omitted
	// numeric/bool options retain their Go zero values, and unknown keys are ignored.
	shape := schemas.Schema(reflect.TypeFor[chapterthumbs.RemoteExtractRequest](), false, "")
	shape.Required = []string{"input_path"}
	shape.AdditionalProperties = true
	failure := schemas.Schema(reflect.TypeFor[chapterthumbs.RemoteExtractErrorResponse](), true, "")
	text := &huma.MediaType{Schema: &huma.Schema{Type: "string"}}
	problem := &huma.MediaType{Schema: failure}
	return workerprotocol.Operation{
		Listener: listener, Method: http.MethodPost, Path: "/chapter-thumbnails/extract",
		Handler: "(*internal/transcodenode.Server).handleChapterThumbnailExtract", AuthClass: "node_bearer",
		Description: "Extract a JPEG frame from an approved input path under the worker GPU admission gate. No durable replay receipt; decoder ignores unknown JSON keys and uses zero values for omitted options.",
		RetrySafety: "non_retryable",
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{jsonMedia: {Schema: request}}},
		Responses: map[string]*huma.Response{
			"200": {Description: "JPEG frame", Content: map[string]*huma.MediaType{"image/jpeg": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}},
			"400": {Description: "Invalid request or unapproved input path", Content: map[string]*huma.MediaType{jsonMedia: problem, textMedia: text}},
			"401": {Description: "Unauthorized", Content: map[string]*huma.MediaType{textMedia: text}},
			"422": {Description: "Extraction failed", Content: map[string]*huma.MediaType{jsonMedia: problem}},
			"503": {Description: "Node, input authority or GPU admission unavailable", Content: map[string]*huma.MediaType{jsonMedia: problem, textMedia: text}},
		},
	}
}
