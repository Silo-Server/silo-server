package nodeenrollment

import (
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/workerprotocol"
)

// ProtocolEnrollment describes the separate pre-database listener. Its bearer
// authenticates the provider token; bound runtime requests use the customer key.
func ProtocolEnrollment(schemas huma.Registry) workerprotocol.Operation {
	op := workerprotocol.JSONRead[Receipt](schemas, "node_enrollment", EnrollmentPath, "(*internal/nodeenrollment.Server).enroll", 401, 400, 409, 422, 500)
	op.Method = http.MethodPost
	op.Description = "One-time provider-token configuration handoff. Validates customer database settings and effective Redis access before saving an immutable private binding. Response confirms a saved binding; use the bound runtime's authenticated enrollment receipt to recover a lost reply and verify readiness. The provider encoding ceiling cannot be supplied here. HTTP and HTTPS are supported."
	op.RetrySafety = "non_retryable"
	op.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: schemas.Schema(reflect.TypeFor[Binding](), true, "")}}}
	return op
}
