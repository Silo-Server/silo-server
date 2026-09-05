package apiv2

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
)

// IdentityEncoded is the API listener's response-time compression exclusion
// for the v2 subtree: a v2 response that carries an ETag is served
// identity-encoded, whatever Accept-Encoding asked for. RFC 9110 8.8.1 makes
// a strong validator name the representation data, content coding included,
// so a gzip body and an identity body may not share the strong tag RenderETag
// produces from (scope, id, version) alone. Leaving the validator-bearing
// response uncompressed keeps one tag per version, so a client echoing the
// tag it received in If-Match or If-None-Match names the same version
// whichever coding it accepted. The bodies are small JSON documents; bulk
// reads carry no validator and compress as before. A request that forbids
// identity outright never reaches this point: encodingGuard answers it 406.
func IdentityEncoded(r *http.Request, h http.Header) bool {
	return strings.HasPrefix(r.URL.Path, Prefix+"/") && h.Get(etagField) != ""
}

// encodingGuard is the request-time half of the identity rule. An operation
// whose output declares an ETag (guarded, conditional, create-only, the
// OpenAPI document) is identity-only, and RFC 9110 12.5.3 forbids sending a
// coding the client refused: an Accept-Encoding that excludes identity
// (`identity;q=0`, or `*;q=0` without identity listed positively) is the 406
// problem rather than an identity body the client said it cannot take.
func encodingGuard(ctx huma.Context, next func(huma.Context)) {
	if identityOnly, _ := ctx.Operation().Metadata[metaIdentityOnly].(bool); identityOnly {
		r, w := humachi.Unwrap(ctx)
		if values, present := r.Header["Accept-Encoding"]; present && identityForbidden(strings.Join(values, ",")) {
			writeProblem(w, r, NewProblem(TypeNotAcceptable,
				"This response carries a validator and is served identity-encoded; Accept-Encoding must not exclude identity."))
			return
		}
	}
	next(ctx)
}

// codingIdentity is the content coding name RFC 9110 12.5.3 reserves for
// "no transformation".
const codingIdentity = "identity"

// identityForbidden reports whether an Accept-Encoding field value excludes
// the identity coding under RFC 9110 12.5.3: identity is acceptable unless
// it is listed with q=0, or `*` is listed with q=0 and identity is not listed
// itself. An explicit identity entry wins over the wildcard; a missing or
// empty field, or one naming only other codings, leaves identity acceptable.
func identityForbidden(acceptEncoding string) bool {
	identityQ, starQ := -1.0, -1.0
	for _, part := range strings.Split(acceptEncoding, ",") {
		fields := strings.Split(part, ";")
		coding := strings.ToLower(strings.TrimSpace(fields[0]))
		if coding != codingIdentity && coding != "*" {
			continue
		}
		q := 1.0
		for _, param := range fields[1:] {
			param = strings.TrimSpace(param)
			if len(param) > 2 && strings.EqualFold(param[:2], "q=") {
				if v, err := strconv.ParseFloat(param[2:], 64); err == nil {
					q = v
				}
			}
		}
		if coding == codingIdentity {
			identityQ = q
		} else {
			starQ = q
		}
	}
	if identityQ >= 0 {
		return identityQ == 0
	}
	return starQ == 0
}
