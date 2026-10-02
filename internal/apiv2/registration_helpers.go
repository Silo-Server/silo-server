package apiv2

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// viewerOperation registers a service-backed operation for a verified profile.
// Domain-specific policy and retry semantics remain explicit at the call site.
func viewerOperation(op huma.Operation) Operation {
	return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}
}

func viewerNoContentMutation(op huma.Operation) Operation {
	op.DefaultStatus = http.StatusNoContent
	return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
}
