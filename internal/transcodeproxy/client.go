package transcodeproxy

import (
	"net/http"
	"time"
)

// nodeClient is shared by every hop that relays transcode output from a node:
// the API's native and Jellyfin-compatible relays and the dedicated proxy.
// No overall timeout — stream bodies are long-lived. Hung nodes are bounded by
// the transport's response-header timeout instead.
var nodeClient = &http.Client{
	Transport: newStreamTransport(),
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// NodeClient returns the process-wide client for relaying manifests, segments
// and their completion acknowledgements to a transcode node. Pass it to
// telemetry.DoTrustedNode so the calls land in the node dependency metrics.
// Calls whose synchronous work on the node can exceed the response-header
// timeout, such as a tone-mapped transcode start, must not use it.
func NodeClient() *http.Client {
	return nodeClient
}

// newStreamTransport tunes the relay→transcode-node connection pool. Many
// concurrent viewers fan their segment fetches through one relay→node pair,
// and Go's default of 2 idle connections per host causes constant connection
// churn (and TLS re-handshakes) under load. The response-header timeout
// bounds requests to a hung node; the longest legitimate server-side wait is
// the 30s manifest-readiness poll on the transcode node.
func newStreamTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	t := base.Clone()
	t.MaxIdleConns = 128
	t.MaxIdleConnsPerHost = 32
	t.ResponseHeaderTimeout = 60 * time.Second
	return t
}
