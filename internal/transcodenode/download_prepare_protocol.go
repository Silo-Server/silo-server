package transcodenode

import (
	"net/http"
	"reflect"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolDownloadPreparation describes the retained synchronous worker command,
// not the native download lifecycle or a durable admission endpoint.
func ProtocolDownloadPreparation(schemas huma.Registry) workerprotocol.Operation {
	const (
		listener     = "transcode_node"
		bearerClass  = "node_bearer"
		nonRetryable = "non_retryable"
		jsonMedia    = "application/json"
	)
	request := schemas.Schema(reflect.TypeFor[downloadprepare.Request](), true, "")
	shape := schemas.Schema(reflect.TypeFor[downloadprepare.Request](), false, "")
	shape.Required = []string{"artifact_id", "input_path"}
	shape.AdditionalProperties = true
	shape.Properties["artifact_id"].Pattern = `^[A-Za-z0-9_-]{1,128}$`
	result := schemas.Schema(reflect.TypeFor[downloadprepare.Result](), true, "")
	op := workerprotocol.Operation{
		Listener: listener, Method: http.MethodPost, Path: "/downloads/prepare",
		Handler: "(*internal/transcodenode.Server).handleDownloadPrepare", AuthClass: bearerClass,
		RetrySafety: nonRetryable,
		Description: "Synchronously prepare or reuse a node-local MP4 under an artifact lifecycle lock. The JSON decoder reads at most 64 KiB, ignores unknown keys and uses zero values for omitted options. Input path authority and GPU admission apply. Ordinary nonempty artifacts can be reused by ID alone; requested execution attestations require a matching receipt. Missing/mismatched receipts may cause replacement. Request cancellation is not proof that no artifact was published; there is no durable job admission or automatic replay guarantee.",
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{jsonMedia: {Schema: request}}},
		Responses: map[string]*huma.Response{
			"200": {Description: "Completed or reused node-local artifact and optional execution attestation", Content: map[string]*huma.MediaType{jsonMedia: {Schema: result}}},
		},
	}
	for _, status := range []int{400, 401, 422, 500, 503} {
		op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{"text/plain": {Schema: &huma.Schema{Type: huma.TypeString}}}}
	}
	for _, status := range []string{"422", "503"} {
		op.Responses[status].Headers = map[string]*huma.Param{ToneMapExecutionErrorHeader: {Description: "Bounded tone-map refusal code when available; not present on all failures.", Schema: &huma.Schema{Type: huma.TypeString}}}
	}
	return op
}
