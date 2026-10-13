package workerregistration

import (
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
)

// ProtocolEnrollment describes the API gateway's operator-authorized one-shot
// enrollment operation; the signed response contains only a public binding.
func ProtocolEnrollment(schemas huma.Registry) workerprotocol.Operation {
	schemas.Schema(reflect.TypeFor[PublicBinding](), true, "")
	op := workerprotocol.JSONRead[workercontrolcore.GrantReply](schemas, "api", "/api/internal/workers/enroll", "(internal/api.workerGateway).enroll", 400, 401, 403, 409, 500)
	op.Method = http.MethodPost
	op.AuthClass = "delegated"
	op.RetrySafety = "non_retryable"
	op.Description = "Operator-issued one-shot enrollment ticket plus proof of possession of a newly generated node Ed25519 key. Ticket digest binds the node, core public key, origins and explicit local HTTP policy. Successful response contains a signed public binding, never customer infrastructure credentials. Lost or consumed ticket requires a fresh operator ticket."
	op.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: schemas.Schema(reflect.TypeFor[enrollmentRequest](), true, "")}}}
	return op
}
