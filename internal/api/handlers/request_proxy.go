package handlers

import (
	"net/http"
)

// RequestBaseURL is the client-facing origin (scheme and host) of a request,
// reading forwarded headers only from a trusted proxy. The v2 listener uses
// it to build the same absolute URLs the v1 handlers build.
func RequestBaseURL(r *http.Request) string { return requestBaseURL(r) }
