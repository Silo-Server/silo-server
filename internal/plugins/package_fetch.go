package plugins

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// Plugin catalogs, checksum files and packages are fetched without a
// whole-request deadline, because a package can be up to 256 MiB and a slow
// but healthy download must still finish. Each phase that can hang is bounded
// instead: connecting, the TLS handshake, waiting for response headers, and
// any single wait for more of the body.
const (
	packageFetchDialTimeout           = 10 * time.Second
	packageFetchTLSHandshakeTimeout   = 10 * time.Second
	packageFetchResponseHeaderTimeout = 30 * time.Second
	packageFetchReadIdleTimeout       = 60 * time.Second
	// packageFetchMaxRead caps one Read of the body. An HTTP/1.1 chunked body
	// keeps reading from the connection until the caller's buffer is full, and
	// io.ReadAll's buffer grows to megabytes, so without a cap one Read could
	// wait on megabytes of a healthy download. With it, a body counts as
	// stalled only when it sends less than this within the idle timeout.
	packageFetchMaxRead = 32 << 10
)

// errPackageReadStalled is the cause recorded when one capped Read of a body
// waits longer than the read-idle timeout.
var errPackageReadStalled = errors.New("download stalled: too little data received within the idle timeout")

// packageFetchTimeouts bounds the transport phases of a package fetch.
type packageFetchTimeouts struct {
	dial           time.Duration
	tlsHandshake   time.Duration
	responseHeader time.Duration
}

var defaultPackageFetchTimeouts = packageFetchTimeouts{
	dial:           packageFetchDialTimeout,
	tlsHandshake:   packageFetchTLSHandshakeTimeout,
	responseHeader: packageFetchResponseHeaderTimeout,
}

// newPackageFetchClient returns the client used when the catalog service or
// installer is given none. It keeps the default transport's proxy and
// connection-pool settings and redirect handling.
func newPackageFetchClient(timeouts packageFetchTimeouts) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	transport := base.Clone()
	transport.DialContext = (&net.Dialer{Timeout: timeouts.dial, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = timeouts.tlsHandshake
	transport.ResponseHeaderTimeout = timeouts.responseHeader
	return &http.Client{Transport: transport}
}

// idleTimeoutReader cancels the fetch when one Read waits longer than idle.
// Only time blocked in Read counts, and each Read asks for at most
// packageFetchMaxRead bytes, so a body is cut off only when it sends less than
// that within idle (about 546 B/s at the default 60s), however steadily.
type idleTimeoutReader struct {
	r     io.Reader
	idle  time.Duration
	timer *time.Timer
}

func newIdleTimeoutReader(r io.Reader, idle time.Duration, cancel context.CancelCauseFunc) *idleTimeoutReader {
	timer := time.AfterFunc(idle, func() { cancel(errPackageReadStalled) })
	timer.Stop()
	return &idleTimeoutReader{r: r, idle: idle, timer: timer}
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	if len(p) > packageFetchMaxRead {
		p = p[:packageFetchMaxRead]
	}
	r.timer.Reset(r.idle)
	n, err := r.r.Read(p)
	r.timer.Stop()
	return n, err
}

func (r *idleTimeoutReader) stop() { r.timer.Stop() }
