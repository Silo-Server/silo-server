package plugins

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/netguard"
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
// installer is given none. Repository catalogs, checksum files and packages
// all come through it, and the catalog JSON, which a third party publishes,
// chooses the package addresses. Its netguard transport checks every address
// it dials, redirect hops included; fetchPackageResource trusts it with the
// server's local network so LAN-hosted repositories keep working, while
// blocked ranges such as link-local and cloud metadata stay refused. Like
// every netguard transport it ignores proxy environment variables. See
// docs/architecture/outbound-address-guard.md.
func newPackageFetchClient(timeouts packageFetchTimeouts) *http.Client {
	transport := netguard.NewTransport(netguard.DialTimeout(timeouts.dial), func(t *http.Transport) {
		t.TLSHandshakeTimeout = timeouts.tlsHandshake
		t.ResponseHeaderTimeout = timeouts.responseHeader
	})
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
