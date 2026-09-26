package transcodeproxy

import "net/http"

// nodeClient is shared by every hop that relays transcode output from a node:
// the API's native and Jellyfin-compatible relays and the dedicated proxy.
//
// It sets no overall timeout, because segment bodies stream at the viewer's
// download speed, and no response-header timeout, because no fixed limit
// covers how long a node may legitimately take to answer. A node that lost a
// session to a restart rebuilds it on the first manifest or segment request
// before it sends headers: it may queue for a rebuild slot, walk the
// hw_accel=auto fallback paths with up to playback.ManifestStartupTimeout per
// FFmpeg attempt, and then wait for the requested segment. Each of those waits
// is bounded on the node. The relay is bounded by the downstream request
// instead: every relay sends its node request with that request's context, so
// a viewer that gives up ends the node call, and completion acknowledgements
// carry their own deadline (Acknowledge).
var nodeClient = &http.Client{
	Transport: newStreamTransport(),
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// NodeClient returns a client for relaying manifests, segments and their
// completion acknowledgements to a transcode node. Every returned client
// shares one process-wide transport and connection pool; each call returns a
// fresh copy of the client so a caller that changes its settings cannot change
// them for every other relay. Pass it to telemetry.DoTrustedNode so the calls
// land in the node dependency metrics, and give every request a context that
// ends when its caller stops waiting.
func NodeClient() *http.Client {
	c := *nodeClient
	return &c
}

// newStreamTransport tunes the relay→transcode-node connection pool. Many
// concurrent viewers fan their segment fetches through one relay→node pair,
// and Go's default of 2 idle connections per host causes constant connection
// churn (and TLS re-handshakes) under load.
func newStreamTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	t := base.Clone()
	t.MaxIdleConns = 128
	t.MaxIdleConnsPerHost = 32
	// See nodeClient: the request context, not a header deadline, bounds a
	// relay. Set explicitly so a process-wide change to the default transport
	// cannot reintroduce one.
	t.ResponseHeaderTimeout = 0
	return t
}
