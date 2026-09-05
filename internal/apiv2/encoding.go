package apiv2

import (
	"net/http"
	"strings"
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
// reads carry no validator and compress as before.
func IdentityEncoded(r *http.Request, h http.Header) bool {
	return strings.HasPrefix(r.URL.Path, Prefix+"/") && h.Get(etagField) != ""
}
