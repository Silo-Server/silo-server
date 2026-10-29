package handlers

import (
	"context"
	"net/http"
)

type refuseRelayRedirectsKey struct{}

// RefuseRelayRedirects marks the requests handler serves as ones whose node
// relay must never pass a transcode node's redirect on to the client. A
// relayed Location names the node, or wherever the node's own front end
// points, never this server's origin, and a client that follows it sends the
// credentials it attached for this server to that other origin. The native
// API v2 transcode routes use it; the frozen /api/v1 routes keep relaying the
// node's answer unchanged.
func RefuseRelayRedirects(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handler(w, r.WithContext(context.WithValue(r.Context(), refuseRelayRedirectsKey{}, true)))
	}
}

// refusesRelayRedirects reports whether RefuseRelayRedirects marked ctx.
func refusesRelayRedirects(ctx context.Context) bool {
	refuse, _ := ctx.Value(refuseRelayRedirectsKey{}).(bool)
	return refuse
}

// isRedirectStatus reports whether status is a redirection a client follows:
// any 3xx except 304 Not Modified, which answers a conditional request.
func isRedirectStatus(status int) bool {
	return status >= http.StatusMultipleChoices && status < http.StatusBadRequest && status != http.StatusNotModified
}
