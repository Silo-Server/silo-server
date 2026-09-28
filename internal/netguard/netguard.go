// Package netguard keeps server-originated HTTP requests to user-supplied
// addresses away from the server's own network. Silo runs inside a household
// LAN or a cluster; an address a user types can otherwise point the server at
// its database, its cache, a transcode node, or a cloud metadata endpoint.
// See docs/architecture/outbound-address-guard.md.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Class is the trust class of a destination address.
type Class int

const (
	// Public addresses are reachable by anyone on the internet.
	Public Class = iota
	// Private addresses belong to the server's own network: loopback, RFC 1918,
	// CGNAT (which includes Tailscale), IPv6 ULA, and other special-use unicast
	// ranges. Only trusted callers may reach them.
	Private
	// Blocked addresses are never dialed: unspecified, link-local (where cloud
	// metadata services live), known metadata addresses, multicast, and
	// reserved ranges. No media server lives there.
	Blocked
)

var (
	// ErrPrivateDestination reports a destination on the server's own network
	// that the caller was not trusted to reach.
	ErrPrivateDestination = errors.New("destination is on the server's local network")
	// ErrBlockedDestination reports a destination no caller may reach.
	ErrBlockedDestination = errors.New("destination address is not allowed")
)

var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8",      // "this network"; 0.0.0.0 dials loopback on Linux
	"169.254.0.0/16", // link-local, including 169.254.169.254 metadata
	"224.0.0.0/4",    // multicast
	"240.0.0.0/4",    // reserved, including broadcast
	"100.100.100.200/32",
	"::/128",
	"fe80::/10",
	"ff00::/8",
	"fd00:ec2::254/128", // AWS IPv6 instance metadata, inside the ULA range
	// Local-use NAT64 (RFC 8215) translates to an IPv4 destination whose
	// position in the address varies by network, so it cannot be checked.
	"64:ff9b:1::/48",
)

var privatePrefixes = mustPrefixes(
	"10.0.0.0/8",
	"100.64.0.0/10", // CGNAT (RFC 6598), including Tailscale
	"127.0.0.0/8",
	"172.16.0.0/12",
	"192.0.0.0/24", // IETF protocol assignments
	"192.0.2.0/24", // TEST-NET-1
	"192.88.99.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",   // benchmarking (RFC 2544)
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"::1/128",
	"fc00::/7",      // ULA
	"fec0::/10",     // site-local; deprecated (RFC 3879) but still routed in some networks
	"2001:db8::/32", // documentation
)

// nat64 is the well-known NAT64 prefix (RFC 6052). A NAT64 gateway translates
// an address in it to the IPv4 destination in its last 32 bits.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// ipv4Compatible holds the deprecated IPv4-compatible IPv6 form (::a.b.c.d,
// RFC 4291). Nothing legitimate uses it, so it is blocked outright rather than
// trusted to stay unroutable. It contains :: and ::1, which Classify handles
// first.
var ipv4Compatible = netip.MustParsePrefix("::/96")

func mustPrefixes(cidrs ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefixes = append(prefixes, netip.MustParsePrefix(cidr))
	}
	return prefixes
}

// Classify reports the trust class of addr. IPv4-mapped IPv6 addresses are
// classified as their IPv4 form, so ::ffff:127.0.0.1 cannot pass as public.
func Classify(addr netip.Addr) Class {
	if !addr.IsValid() {
		return Blocked
	}
	// Prefix.Contains never matches a zoned address or an IPv4-mapped one
	// against an IPv4 prefix.
	addr = addr.WithZone("").Unmap()
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return Blocked
		}
	}
	// A NAT64 address is never public: it reaches whatever IPv4 destination it
	// embeds, and is blocked when that destination is.
	if nat64.Contains(addr) {
		b := addr.As16()
		if Classify(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})) == Blocked {
			return Blocked
		}
		return Private
	}
	for _, prefix := range privatePrefixes {
		if prefix.Contains(addr) {
			return Private
		}
	}
	if ipv4Compatible.Contains(addr) {
		return Blocked
	}
	return Public
}

// CheckAddr reports whether a caller may dial addr. allowPrivate trusts the
// caller with the server's own network; blocked addresses stay refused.
// Untrusted callers also may not dial the server's own interface addresses,
// which reach services a cloud firewall hides from the internet.
func CheckAddr(addr netip.Addr, allowPrivate bool) error {
	switch Classify(addr) {
	case Blocked:
		return ErrBlockedDestination
	case Private:
		if !allowPrivate {
			return ErrPrivateDestination
		}
	case Public:
		if !allowPrivate && hostAddress(addr.WithZone("").Unmap()) {
			return ErrPrivateDestination
		}
	}
	return nil
}

// hostAddress reports whether addr is assigned to one of this machine's
// interfaces. An enumeration failure counts as a match, so the check fails
// closed.
func hostAddress(addr netip.Addr) bool {
	assigned, err := net.InterfaceAddrs()
	if err != nil {
		return true
	}
	for _, a := range assigned {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip, ok := netip.AddrFromSlice(ipNet.IP); ok && ip.Unmap() == addr {
			return true
		}
	}
	return false
}

// checkLookupTimeout bounds CheckURL's DNS lookup. The check only gives an
// early answer; a slow resolver must not stall the request that asked.
const checkLookupTimeout = 2 * time.Second

// CheckURL resolves rawURL's host and reports ErrPrivateDestination or
// ErrBlockedDestination when any address it resolves to may not be dialed.
// It gives callers a clear refusal before a request is queued or credentials
// are stored. A URL that does not parse, has no host, or does not resolve in
// time passes: the request itself reports those failures, and the dialer
// enforces the policy on every connection regardless.
func CheckURL(ctx context.Context, rawURL string, allowPrivate bool) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil
	}
	host := parsed.Hostname()
	if host == "" {
		return nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return CheckAddr(addr, allowPrivate)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, checkLookupTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", host)
	if err != nil {
		return nil
	}
	for _, addr := range addrs {
		if err := CheckAddr(addr, allowPrivate); err != nil {
			return err
		}
	}
	return nil
}

type privateAccessKey struct{}

// WithPrivateAccess marks requests made with ctx as trusted to reach the
// server's own network. Blocked addresses stay refused.
func WithPrivateAccess(ctx context.Context) context.Context {
	return context.WithValue(ctx, privateAccessKey{}, true)
}

// PrivateAccess reports whether ctx was marked by WithPrivateAccess.
func PrivateAccess(ctx context.Context) bool {
	allowed, _ := ctx.Value(privateAccessKey{}).(bool)
	return allowed
}

// Transport sends each request through one of two connection pools, chosen
// by whether the request context carries WithPrivateAccess. The pools never
// share connections: an idle connection a trusted request opened to a LAN
// server must not be reused by an untrusted request to the same address.
type Transport struct {
	public  *http.Transport
	private *http.Transport
}

// NewTransport returns a Transport whose dialers check every address they
// connect to, which covers redirects and DNS answers that change after a
// URL was checked.
func NewTransport() *Transport {
	return &Transport{public: newGuardedTransport(false), private: newGuardedTransport(true)}
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if PrivateAccess(req.Context()) {
		return t.private.RoundTrip(req)
	}
	return t.public.RoundTrip(req)
}

// CloseIdleConnections closes idle connections in both pools.
func (t *Transport) CloseIdleConnections() {
	t.public.CloseIdleConnections()
	t.private.CloseIdleConnections()
}

// NewClient returns an HTTP client over a new Transport.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: NewTransport()}
}

func newGuardedTransport(allowPrivate bool) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("checking dial address: %w", err)
			}
			addr, err := netip.ParseAddr(host)
			if err != nil {
				return fmt.Errorf("checking dial address: %w", err)
			}
			return CheckAddr(addr, allowPrivate)
		},
	}
	return &http.Transport{
		// No proxy: the dialer checks the address it connects to, and a proxy
		// would hide the real destination behind the proxy's address.
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}
