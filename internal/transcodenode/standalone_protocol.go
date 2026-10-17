package transcodenode

import (
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

// ProtocolStandalone describes the additional endpoint on enrolled HTTP-only
// workers and the existing health response's additive negotiation field.
func ProtocolStandalone(schemas huma.Registry) []workerprotocol.Operation {
	const (
		listener     = "transcode_node"
		bearerClass  = "node_bearer"
		binaryFormat = "binary"
		nonRetryable = "non_retryable"
		plain        = "text/plain"
	)
	request := schemas.Schema(reflect.TypeFor[workerwire.Assignment](), true, "")
	shape := schemas.Schema(reflect.TypeFor[workerwire.Assignment](), false, "")
	// RawMessage is JSON syntax, rather than the byte-string schema used for
	// ordinary []byte. The assigned handler validates its own request shape.
	shape.Properties["body"] = &huma.Schema{}
	shape.AdditionalProperties = false
	text := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString}}
	execution := workerprotocol.Operation{Listener: listener, Method: http.MethodPost, Path: "/worker/execute", Handler: "(*internal/transcodenode.Server).handleWorkerExecute", AuthClass: bearerClass, RetrySafety: nonRetryable,
		Description: "Available only on an enrolled HTTP-only worker. An Ed25519 core-command proof binds method, exact escaped path/query, origin, body and replay nonce. Version 1 assignment binds node/work/generation and opaque source references. The worker requires current signed core settings and a short active or pending execution lease, applies local compute admission, and forwards the assigned engine response. Body limit 1 MiB; unknown assignment fields are refused. Uncertain execution requires reconciliation before another candidate.",
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: request}}}, Responses: map[string]*huma.Response{"2XX": {Description: "Assigned engine success; shape and media depend on assignment path", Content: map[string]*huma.MediaType{"*/*": {Schema: &huma.Schema{Type: huma.TypeString, Format: binaryFormat}}}}, "400": {Description: "Invalid assignment or source", Content: map[string]*huma.MediaType{plain: text}}, "401": {Description: "Missing, invalid or replayed core proof", Content: map[string]*huma.MediaType{plain: text}}, "409": {Description: "Superseded generation", Content: map[string]*huma.MediaType{plain: text}}, "503": {Description: "Authority, worker or local admission unavailable", Content: map[string]*huma.MediaType{plain: text}}}}
	health := workerprotocol.JSONRead[HealthResponse](schemas, listener, "/api/v1/health", "(*internal/transcodenode.Server).handleHealth")
	health.AuthClass = "public"
	health.Description = "Path-free liveness, stored hardware capability digest and resource snapshots. Enrolled HTTP-only workers advertise worker_protocol=http_only_v1; legacy workers omit it. Health never probes hardware or renews execution authority."
	return []workerprotocol.Operation{execution, health}
}
