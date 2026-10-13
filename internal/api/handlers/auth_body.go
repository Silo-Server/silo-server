package handlers

import "net/http"

// maxAuthRequestBodyBytes bounds JSON bodies on the public v1 auth and
// onboarding endpoints (login, setup, signup, refresh, device start/poll,
// invitation accept). These run before any credential check, so without a
// cap an anonymous caller could make the server buffer an arbitrarily large
// JSON string. The value matches the v2 default structured-body cap
// (apiv2.MaxJSONBodyBytes) so both surfaces accept the same requests.
const maxAuthRequestBodyBytes = 1 << 20

// decodeAuthRequestBody decodes a public auth request body with the
// maxAuthRequestBodyBytes cap; see decodeLimitedJSONBody.
func decodeAuthRequestBody(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) bool {
	return decodeLimitedJSONBody(w, r, maxAuthRequestBodyBytes, dst, allowEmpty)
}
