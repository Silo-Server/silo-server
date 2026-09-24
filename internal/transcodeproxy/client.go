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

// NodeClient returns a client for relaying manifests, segments and their
// completion acknowledgements to a transcode node. Every returned client
// shares one process-wide transport and connection pool; each call returns a
// fresh copy of the client so a caller that changes its settings cannot change
// them for every other relay. Pass it to telemetry.DoTrustedNode so the calls
// land in the node dependency metrics. Calls whose synchronous work on the
// node can exceed the response-header timeout, such as a tone-mapped
// transcode start, must not use it.
func NodeClient() *http.Client {
	c := *nodeClient
	return &c
}

// nodeResponseHeaderTimeout bounds how long a relay waits for a node to start
// answering. It must stay above the node's slowest legitimate wait before it
// sends headers. Today that is a segment request that triggers a seek restart
// of a tone-mapped session: up to 20s re-validating the tone-map source
// (playback.restartToneMapValidationTimeout), the FFmpeg stop, then up to 30s
// waiting for the segment. That is about 50s. The manifest-readiness poll
// (playback.ManifestStartupTimeout, 30s) is shorter. Raising either node
// budget means revisiting this one.
const nodeResponseHeaderTimeout = 60 * time.Second

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
	t.ResponseHeaderTimeout = nodeResponseHeaderTimeout
	return t
}
