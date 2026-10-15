package transcodenode

import (
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolLegacyStop describes only the existing ID-addressed worker command.
func ProtocolLegacyStop() workerprotocol.Operation {
	const (
		listener         = "transcode_node"
		bearerClass      = "node_bearer"
		nonRetryable     = "non_retryable"
		pathParameter    = "path"
		sessionParameter = "session_id"
		textMedia        = "text/plain"
	)
	op := workerprotocol.Operation{
		Listener: listener, Method: http.MethodDelete, Path: "/transcode/{session_id}", Handler: "(*internal/transcodenode.Server).handleStop", AuthClass: bearerClass,
		RetrySafety: nonRetryable,
		Description: "Stop a transport under its lifecycle lock. The caller deletes the transport's stored recipe before it sends this. A progressive transport, and any transport not running as an HLS session here, gets a process-local token-lifetime fence. Local teardown may precede a 503 shutdown failure. A missing session returns 404 after cleanup attempts. This ID-addressed command has no durable replay identity and must not be automatically repeated against a possible successor.",
		Parameters:  []*huma.Param{{Name: sessionParameter, In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Transport identifier; not a durable stop receipt."}},
		Responses:   map[string]*huma.Response{"204": {Description: "Local teardown completed; no body. Local file/session close errors are logged rather than changing this status."}},
	}
	for _, status := range []int{401, 404, 503} {
		op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{textMedia: {Schema: &huma.Schema{Type: huma.TypeString}}}}
	}
	return op
}
