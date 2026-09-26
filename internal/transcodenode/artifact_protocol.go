package transcodenode

import (
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolArtifacts describes node-local prepared bytes, never a native download grant.
func ProtocolArtifacts() []workerprotocol.Operation {
	const path = "/downloads/artifacts/{artifact_id}"
	const listener = "transcode_node"
	const plain = "text/plain"
	const mp4 = "video/mp4"
	binary := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}
	text := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString}}
	var operations []workerprotocol.Operation
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete} {
		op := workerprotocol.Operation{Listener: listener, Method: method, Path: path, AuthClass: "node_bearer", Handler: "(*internal/transcodenode.Server).handleDownloadArtifact",
			Description: "Read node-local prepared MP4 bytes under the preparation/deletion lifecycle lock. No native delivery grant or cross-node artifact availability is implied.",
			Parameters:  []*huma.Param{{Name: "artifact_id", In: "path", Required: true, Schema: &huma.Schema{Type: huma.TypeString, Pattern: `^[A-Za-z0-9_-]{1,128}$`}}},
			Responses:   map[string]*huma.Response{},
		}
		for _, status := range []int{401, 404, 500, 503} {
			op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{plain: text}}
		}
		if method == http.MethodDelete {
			op.Handler = "(*internal/transcodenode.Server).handleDeleteDownloadArtifact"
			op.RetrySafety = "natural_idempotent"
			op.Description = "Remove the exact node-local artifact, partial file and receipt under its lifecycle lock. Missing valid identifiers succeed; failures may follow partial removal. This does not cancel preparation or delete another node's bytes."
			op.Responses["204"] = &huma.Response{Description: "No Content"}
		} else {
			for _, name := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
				op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: "header", Schema: &huma.Schema{Type: huma.TypeString}})
			}
			headers := map[string]*huma.Param{}
			for _, name := range []string{"ETag", "Last-Modified", "Accept-Ranges", "Content-Range", "Content-Length", "Content-Disposition"} {
				headers[name] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}, Description: "Standard ServeContent response metadata, when applicable."}
			}
			op.Responses["200"] = &huma.Response{Description: "Prepared MP4", Headers: headers, Content: map[string]*huma.MediaType{mp4: binary}}
			op.Responses["206"] = &huma.Response{Description: "Requested byte ranges", Headers: headers, Content: map[string]*huma.MediaType{mp4: binary, "multipart/byteranges": binary}}
			op.Responses["304"] = &huma.Response{Description: "Not Modified", Headers: headers}
			op.Responses["412"] = &huma.Response{Description: "Precondition Failed"}
			op.Responses["416"] = &huma.Response{Description: "Range Not Satisfiable", Headers: headers, Content: map[string]*huma.MediaType{plain: text}}
			if method == http.MethodHead {
				op.Description += " HEAD has no response body; conditional handling follows http.ServeContent."
				for _, response := range op.Responses {
					response.Content = nil
				}
			}
		}
		operations = append(operations, op)
	}
	return operations
}
