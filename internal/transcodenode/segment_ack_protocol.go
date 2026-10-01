package transcodenode

import (
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolSegmentAcknowledgement retains the private downstream-completion hop.
func ProtocolSegmentAcknowledgement() workerprotocol.Operation {
	const (
		listener         = "transcode_node"
		pathParameter    = "path"
		headerParameter  = "header"
		segmentName      = "name"
		bearerClass      = "node_bearer"
		sessionParameter = "session_id"
		textMedia        = "text/plain"
		idempotent       = "natural_idempotent"
	)
	parameters := []*huma.Param{
		{Name: sessionParameter, In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Existing transport session."},
		{Name: segmentName, In: pathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Media segment filename parsed by playback.ParseSegmentNumber."},
		{Name: transcodeproxy.GenerationHeader, In: headerParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Opaque incarnation/timeline from the completed response; private to Silo hops."},
	}
	responses := map[string]*huma.Response{"204": {Description: "Acknowledged or ignored stale timeline; no body."}}
	for _, status := range []int{400, 401, 404, 503} {
		responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{textMedia: {Schema: &huma.Schema{Type: huma.TypeString}}}}
	}
	return workerprotocol.Operation{Listener: listener, Method: http.MethodPost, Path: "/transcode/{session_id}/segment/{name}/downloaded", Handler: "(*internal/transcodenode.Server).handleSegmentDownloaded", AuthClass: bearerClass, Parameters: parameters, Responses: responses, RetrySafety: idempotent,
		Description: "Record downstream completion only for the exact session incarnation and timeline. A stale nonempty generation is ignored with 204; no reconstruction, durable receipt or client-visible generation is implied."}
}
