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

// viewerNoContentMutation registers a bodiless 204 favorites or watchlist
// membership write for a verified profile. A repeated add or remove changes
// no row and the shared seam notifies nobody for it, so the write is
// naturally idempotent.
func viewerNoContentMutation(op huma.Operation) Operation {
	op.DefaultStatus = http.StatusNoContent
	return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
}
