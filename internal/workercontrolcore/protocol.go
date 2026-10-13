package workercontrolcore

import (
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

const (
	protocolPathParameter   = "path"
	protocolHeaderParameter = "header"
	protocolSourceHandler   = "s.options.SourceHandler"
	protocolSnapshotHandler = "snapshot"
	protocolReportHandler   = "report"
	protocolRecipeHandler   = "recipe"
	protocolCloseHandler    = "closeWork"
	protocolRenewHandler    = "renew"
	protocolNodeParam       = "node"
	protocolWorkParam       = "work"
	protocolGrantParam      = "grant"
	protocolDelegatedAuth   = "delegated"
	protocolRecipePath      = "/api/internal/workers/{node}/recipes/{work}"
)

type snapshotRequest struct {
	BootID   string `json:"boot_id"`
	Nonce    string `json:"nonce"`
	Sequence uint64 `json:"sequence"`
}
type closeRequest struct {
	Generation uint64 `json:"generation"`
}
type renewRequest struct {
	WorkID     string `json:"work_id"`
	Generation uint64 `json:"generation"`
}
type outputProtocolRequest struct {
	Token   string `json:"token"`
	NodeURL string `json:"node_url"`
}

// ProtocolControls describes the sealed core listener. A node_bearer class here
// means its node-specific Ed25519 request proof, rather than a customer JWT.
func ProtocolControls(schemas huma.Registry) []workerprotocol.Operation {
	// Publish decoded grant bodies beside the actual signed envelope. They are
	// never separate HTTP fetches or credentials for database access.
	schemas.Schema(reflect.TypeFor[workerwire.Snapshot](), true, "")
	schemas.Schema(reflect.TypeFor[workerwire.Source](), true, "")
	schemas.Schema(reflect.TypeFor[RecipeReply](), true, "")
	schemas.Schema(reflect.TypeFor[streamtoken.Claims](), true, "")
	assignment := schemas.Schema(reflect.TypeFor[workerwire.Assignment](), false, "")
	assignment.Properties["body"] = &huma.Schema{}
	specs := []struct {
		method, path, handler, description string
		body                               reflect.Type
	}{
		{http.MethodPost, "/api/internal/workers/{node}/snapshot", protocolSnapshotHandler, "Signed two-second authority snapshot. The nonce, boot ID and increasing sequence bind the response; active/pending generations and allowlisted execution settings are validated locally. Freshness ages from request send, including network delay.", reflect.TypeFor[snapshotRequest]()},
		{http.MethodPost, "/api/internal/workers/{node}/report", protocolReportHandler, "Signed coalesced progress, sessions and bounded observations, generation-fenced on the core. Report JSON limit 512 KiB. The core persists operational state; workers have no Redis or database connection.", reflect.TypeFor[workerwire.Report]()},
		{http.MethodGet, protocolRecipePath, protocolRecipeHandler, "Fresh signed recipe and assignment for this node's active work; stopped or superseded generations cannot reconstruct. Source aliases contain opaque identities and scoped source descriptors, never catalog paths.", nil},
		{http.MethodHead, protocolRecipePath, protocolRecipeHandler, "The same proof and active-work admission as GET; no response body.", nil},
		{http.MethodDelete, protocolRecipePath, protocolCloseHandler, "Generation-fenced stop and source release. An uncertain response requires a fresh signed snapshot, rather than assuming the work stopped.", reflect.TypeFor[closeRequest]()},
		{http.MethodPost, "/api/internal/workers/{node}/revoke", "closeNode", "Revoke this node's active and pending work. Admission is checked before durable generation fencing and source release.", nil},
		{http.MethodPost, "/api/internal/workers/{node}/sources/{grant}/renew", protocolRenewHandler, "Renew the exact scoped immutable source for an admitted work generation. X-Silo-Source-Grant must match the route grant. The signed Source retains ID, revision, size, ETag, URL and scope. Source TTL is 120 seconds; workers renew after 45 seconds while execution authority remains current.", reflect.TypeFor[renewRequest]()},
	}
	var operations []workerprotocol.Operation
	for _, s := range specs {
		op := workerprotocol.JSONRead[GrantReply](schemas, "worker_control_core", s.path, "(*internal/workercontrolcore.Service)."+s.handler, 400, 401, 403, 404, 503)
		op.Method = s.method
		op.Description = s.description
		op.RetrySafety = "non_retryable"
		op.Parameters = []*huma.Param{{Name: protocolNodeParam, In: protocolPathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeInteger, Minimum: floatPointer(1)}}}
		if s.handler == protocolRecipeHandler || s.handler == protocolCloseHandler {
			op.Parameters = append(op.Parameters, &huma.Param{Name: protocolWorkParam, In: protocolPathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}})
		}
		if s.handler == protocolRenewHandler {
			op.Parameters = append(op.Parameters, &huma.Param{Name: protocolGrantParam, In: protocolPathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}}, &huma.Param{Name: "X-Silo-Source-Grant", In: protocolHeaderParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}})
		}
		if s.body != nil {
			op.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: schemas.Schema(s.body, true, "")}}}
		}
		if s.method == http.MethodHead {
			op.Responses["200"] = &huma.Response{Description: "Admitted recipe; no body"}
		}
		operations = append(operations, op)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		op := workerprotocol.Operation{Listener: "worker_control_core", Method: method, Path: "/api/internal/workers/{node}/sources/{grant}", Handler: protocolSourceHandler, AuthClass: protocolDelegatedAuth, Description: "Delegated core source authority verifies node proof, exact X-Silo-Source-Grant and active work on every request and bounded streaming chunks. GET requires exact If-Match. Single, open and suffix byte ranges are supported; malformed or multiple ranges return 400; unsatisfiable ranges return 416. Pinned regular-file identity and revision are rechecked before, throughout and after reads; mutation revokes the source. Identity encoding, no redirects and Cache-Control no-store. Core-only paths never appear on the wire.", Parameters: []*huma.Param{{Name: protocolNodeParam, In: protocolPathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeInteger}}, {Name: protocolGrantParam, In: protocolPathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}}, {Name: "X-Silo-Source-Grant", In: protocolHeaderParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}}, {Name: "If-Match", In: protocolHeaderParameter, Required: method == http.MethodGet, Schema: &huma.Schema{Type: huma.TypeString}}, {Name: "Range", In: protocolHeaderParameter, Schema: &huma.Schema{Type: huma.TypeString}}}, Responses: map[string]*huma.Response{}}
		for _, status := range []string{"200", "206", "400", "401", "403", "404", "408", "410", "412", "416", "429", "503"} {
			op.Responses[status] = &huma.Response{Description: "Scoped source response"}
		}
		if method == http.MethodGet {
			for _, status := range []string{"200", "206"} {
				op.Responses[status].Content = map[string]*huma.MediaType{"application/octet-stream": {Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}}
			}
		}
		operations = append(operations, op)
	}
	return operations
}
func floatPointer(value float64) *float64 { return &value }

// ProtocolGateway describes the API's exact forwarding surface separately from
// the owning sealed listener. Forwarding preserves proof bytes and delegates
// authentication; none of these operations becomes a native /api/v2 alias.
func ProtocolGateway(schemas huma.Registry) []workerprotocol.Operation {
	operations := ProtocolControls(schemas)
	names := map[string]string{protocolSnapshotHandler: protocolSnapshotHandler, protocolReportHandler: protocolReportHandler, protocolRecipeHandler: protocolRecipeHandler, protocolCloseHandler: protocolCloseHandler, "closeNode": "revoke", protocolRenewHandler: protocolRenewHandler, protocolSourceHandler: "source"}
	for i := range operations {
		op := &operations[i]
		name := op.Handler
		if name != protocolSourceHandler {
			name = name[len("(*internal/workercontrolcore.Service)."):]
		}
		op.Listener = "api"
		op.Handler = "(internal/api.workerGateway)." + names[name]
		op.AuthClass = protocolDelegatedAuth
	}
	for _, spec := range []struct {
		path, handler, description string
		body                       reflect.Type
	}{
		{"/api/internal/workers/output-grant", "outputGrant", "Trusted core/proxy request proof admits delegation of an existing customer token to its exact active standalone node/work/generation/output scope. This gateway uses the existing trusted proxy proof; standalone workers use node-specific asymmetric grants and do not receive that proxy signing secret. Body limit128 KiB and token limit64 KiB.", reflect.TypeFor[outputProtocolRequest]()},
	} {
		op := workerprotocol.JSONRead[GrantReply](schemas, "api", spec.path, "(internal/api.workerGateway)."+spec.handler, 400, 401, 403, 409, 500)
		op.Method = http.MethodPost
		op.AuthClass = protocolDelegatedAuth
		op.Description = spec.description
		op.RetrySafety = "non_retryable"
		op.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: schemas.Schema(spec.body, true, "")}}}
		operations = append(operations, op)
	}
	return operations
}
