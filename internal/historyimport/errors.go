package historyimport

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// UpstreamHTTPError is the error a source server answering with status
// produces. The client types that carry an upstream status are unexported;
// tests of a mapping outside this package build one here without a client.
func UpstreamHTTPError(status int) error { return &embyHTTPError{StatusCode: status} }

// IsReachabilityError reports whether err indicates the upstream server could
// not be reached due to URL, DNS, connection, timeout, or TLS issues.
func IsReachabilityError(err error) bool {
	if err == nil || UpstreamHTTPStatus(err) > 0 {
		return false
	}
	// The guard refuses before connecting: the address is not allowed, and
	// the user needs that message rather than "unreachable".
	if _, refused := ServerAddressMessage(err); refused {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() || urlErr.Op == "parse" {
			return true
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	var addrErr *net.AddrError
	if errors.As(err, &addrErr) {
		return true
	}

	var invalidCertErr x509.CertificateInvalidError
	if errors.As(err, &invalidCertErr) {
		return true
	}

	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return true
	}

	var unknownAuthorityErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityErr) {
		return true
	}

	var systemRootsErr x509.SystemRootsError
	if errors.As(err, &systemRootsErr) {
		return true
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection refused") ||
		strings.Contains(message, "no such host") ||
		strings.Contains(message, "dial tcp") ||
		strings.Contains(message, "lookup ") ||
		strings.Contains(message, "server misbehaving") ||
		strings.Contains(message, "missing protocol scheme") ||
		strings.Contains(message, "unsupported protocol scheme") ||
		strings.Contains(message, "certificate") ||
		strings.Contains(message, "tls:") ||
		strings.Contains(message, "x509:") ||
		strings.Contains(message, "timeout") ||
		strings.Contains(message, "no reachable server url")
}

// ErrSourceUnreachable marks a source server (or Emby Connect or plex.tv)
// that Silo could not reach at all. Only upstream call sites tag it, so a
// failure of Silo's own database never reads as a source address problem.
var ErrSourceUnreachable = errors.New("history import source unreachable")

func tagUnreachable(err error) error {
	if IsReachabilityError(err) {
		return fmt.Errorf("%w: %w", ErrSourceUnreachable, err)
	}
	return err
}
