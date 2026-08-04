package remotestream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const (
	maxRedirects          = 5
	responseHeaderTimeout = 30 * time.Second
)

type contextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// NewSafeTransport returns a transport that resolves and validates every new
// connection, then dials the validated IP directly. This closes the DNS
// rebinding window between validation and connection establishment.
func NewSafeTransport() *http.Transport {
	return newSafeTransport(net.DefaultResolver, &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second})
}

func newSafeTransport(resolver ipResolver, dialer contextDialer) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.Proxy = nil
	// Bound connection stalls without imposing a deadline on the response
	// body. Media bodies may legitimately stream for hours.
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse remote stream address: %w", err)
		}
		addresses, err := resolvePublicAddresses(ctx, resolver, host)
		if err != nil {
			return nil, err
		}
		var dialErr error
		for _, candidate := range addresses {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = errors.Join(dialErr, err)
		}
		return nil, fmt.Errorf("dial validated remote stream host: %w", dialErr)
	}
	transport.DialTLSContext = nil
	transport.ForceAttemptHTTP2 = true
	transport.MaxIdleConnsPerHost = 10
	transport.MaxConnsPerHost = 32
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec // secure minimum; ServerName is populated by net/http.
	return transport
}

// NewSafeClient returns an HTTP client that applies the safe transport and
// validates every redirect target. HTTPS-to-HTTP redirects are rejected.
func NewSafeClient() *http.Client {
	return &http.Client{
		Transport:     NewSafeTransport(),
		CheckRedirect: checkRedirect,
	}
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("too many remote stream redirects")
	}
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("remote stream redirect would downgrade HTTPS")
	}
	if _, err := ValidateURL(req.Context(), req.URL.String()); err != nil {
		return fmt.Errorf("unsafe remote stream redirect: %w", err)
	}
	return nil
}
