package handlers

import (
	"net/http"
	"strings"
)

// forwardedHost returns the public host a reverse proxy or CDN forwarded the
// request on behalf of, taken from the first X-Forwarded-Host value, or "" when
// the header is absent.
//
// Behind a TLS-terminating CDN/proxy, r.Host is the internal origin host the
// proxy dialed, while the public host the browser actually used arrives here.
// Callers that need to reason about the client-facing host (origin checks,
// absolute URL construction) must prefer this over r.Host.
func forwardedHost(r *http.Request) string {
	forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if forwarded == "" {
		return ""
	}

	// Multiple proxy hops produce a comma-separated list; the first entry is
	// the original client-facing host.
	if comma := strings.IndexByte(forwarded, ','); comma >= 0 {
		forwarded = forwarded[:comma]
	}

	return strings.TrimSpace(forwarded)
}
