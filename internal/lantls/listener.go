package lantls

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// recordTypeHandshake is the first byte of every TLS connection (a handshake
// record carrying the ClientHello). No HTTP/1.x request starts with it: a
// request line starts with an ASCII method name.
const recordTypeHandshake = 0x16

// DefaultSniffTimeout is how long a new connection may stay silent before it
// is handed on as plain HTTP. TLS clients send their ClientHello as soon as
// they connect; a silent connection is a plain-HTTP client that connected
// ahead of its first request, which the HTTP server's own timeouts then cover.
const DefaultSniffTimeout = 5 * time.Second

// Listener serves TLS and plain HTTP on one port. It accepts every connection
// from the wrapped listener at once and reads the first byte in a goroutine of
// its own, so a slow or silent client never holds up the next one. A TLS
// connection is returned as a *tls.Conn (the HTTP server keys request.TLS,
// and with it the request scheme, on that type); anything else is returned as
// the plain connection with the byte it read put back.
//
// Close closes the wrapped listener and every connection still being
// classified, so a server shutdown leaves nothing behind that the HTTP server
// never saw.
type Listener struct {
	inner        net.Listener
	config       *tls.Config
	sniffTimeout time.Duration

	ready chan net.Conn
	// errs carries Accept errors from the wrapped listener. The acceptor stops
	// after the first error that is not temporary and records it in stopErr.
	errs      chan error
	stopped   chan struct{}
	stopErr   error
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error

	mu      sync.Mutex
	pending map[net.Conn]struct{}
}

// NewListener wraps inner. config is the TLS configuration for connections
// that open with a TLS handshake; sniffTimeout <= 0 uses DefaultSniffTimeout.
func NewListener(inner net.Listener, config *tls.Config, sniffTimeout time.Duration) *Listener {
	if sniffTimeout <= 0 {
		sniffTimeout = DefaultSniffTimeout
	}
	l := &Listener{
		inner:        inner,
		config:       config,
		sniffTimeout: sniffTimeout,
		ready:        make(chan net.Conn),
		errs:         make(chan error),
		stopped:      make(chan struct{}),
		done:         make(chan struct{}),
		pending:      make(map[net.Conn]struct{}),
	}
	go l.acceptLoop()
	return l
}

// Accept returns the next classified connection. Once Close has run it
// returns net.ErrClosed, even when a classified connection is waiting.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case conn := <-l.ready:
		return conn, nil
	case err := <-l.errs:
		return nil, err
	case <-l.done:
		return nil, net.ErrClosed
	case <-l.stopped:
		return nil, l.stopErr
	}
}

// Close closes the wrapped listener and every connection still being
// classified. Safe to call more than once.
func (l *Listener) Close() error {
	l.closeOnce.Do(func() {
		close(l.done)
		l.closeErr = l.inner.Close()
		l.mu.Lock()
		for conn := range l.pending {
			_ = conn.Close()
		}
		l.pending = nil
		l.mu.Unlock()
	})
	return l.closeErr
}

// Addr is the wrapped listener's address.
func (l *Listener) Addr() net.Addr { return l.inner.Addr() }

func (l *Listener) acceptLoop() {
	for {
		conn, err := l.inner.Accept()
		if err != nil {
			if isTemporary(err) {
				select {
				case l.errs <- err:
					continue
				case <-l.done:
					return
				}
			}
			l.stopErr = err
			close(l.stopped)
			return
		}
		if !l.track(conn) {
			_ = conn.Close()
			return
		}
		go l.classify(conn)
	}
}

// isTemporary reports an Accept error the HTTP server retries after a pause
// (too many open files, for example) rather than one that ends serving.
func isTemporary(err error) bool {
	var ne interface{ Temporary() bool }
	return errors.As(err, &ne) && ne.Temporary()
}

func (l *Listener) track(conn net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending == nil {
		return false
	}
	l.pending[conn] = struct{}{}
	return true
}

// untrack reports whether the connection was still pending; false means
// Close already closed it.
func (l *Listener) untrack(conn net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending == nil {
		return false
	}
	delete(l.pending, conn)
	return true
}

func (l *Listener) classify(conn net.Conn) {
	classified, ok := l.sniff(conn)
	if !l.untrack(conn) || !ok {
		_ = conn.Close()
		return
	}
	select {
	case l.ready <- classified:
	case <-l.done:
		_ = classified.Close()
	}
}

func (l *Listener) sniff(conn net.Conn) (net.Conn, bool) {
	if err := conn.SetReadDeadline(time.Now().Add(l.sniffTimeout)); err != nil {
		return nil, false
	}
	var first [1]byte
	n, err := conn.Read(first[:])
	if n == 0 {
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			// Closed before saying anything; nothing to serve.
			return nil, false
		}
		// Silent so far: a plain-HTTP client that connected early. Its
		// request, when it comes, is read by the HTTP server.
		if conn.SetReadDeadline(time.Time{}) != nil {
			return nil, false
		}
		return conn, true
	}
	if conn.SetReadDeadline(time.Time{}) != nil {
		return nil, false
	}
	replayed := &replayConn{Conn: conn, first: first[0], pending: true}
	if first[0] == recordTypeHandshake {
		return tls.Server(replayed, l.config), true
	}
	return replayed, true
}

// replayConn returns the byte the listener read to classify the connection
// before reading on from the connection itself.
type replayConn struct {
	net.Conn
	first   byte
	pending bool
}

func (c *replayConn) Read(p []byte) (int, error) {
	if !c.pending || len(p) == 0 {
		return c.Conn.Read(p)
	}
	p[0] = c.first
	c.pending = false
	return 1, nil
}

// CloseWrite lets the HTTP server half-close a connection it answers without
// reading the whole request (an early 413, say), as it does for a bare
// *net.TCPConn: without the half-close the client can lose that response to a
// reset.
func (c *replayConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errors.ErrUnsupported
}

// ReadFrom keeps the HTTP server's sendfile path for responses: it writes to
// the wrapped connection directly when that supports it (a *net.TCPConn does).
// Writes never involve the replayed byte.
func (c *replayConn) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := c.Conn.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(c.Conn, r)
}
