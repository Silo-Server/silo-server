package clientip

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
)

// Resolver resolves the real client IP from an HTTP request, accounting for
// trusted reverse proxies that set forwarding headers.
type Resolver struct {
	reloadMu sync.Mutex
	mu       sync.RWMutex
	trusted  []*net.IPNet
}

// NewResolver creates a Resolver with the given trusted proxy CIDRs.
// If trusted is nil or empty, forwarding headers are never consulted.
func NewResolver(trusted []*net.IPNet) *Resolver {
	return &Resolver{trusted: trusted}
}

// ClientIP returns the resolved client IP address string.
// The returned value is always normalized via net.ParseIP().String().
//
// Forwarding headers count only when the transport peer is a trusted proxy.
// X-Forwarded-For is read across all of its header lines in order, because
// some proxies (HAProxy's option forwardfor) add their own line after the
// client's instead of appending to it. Each entry was written by the hop to
// its right, so walking right to left the first untrusted entry is the client.
// An entry that is not an address stops the walk at the trusted hop that
// wrote it, since nothing to its left can be attributed.
//
// X-Real-IP, as a single value, is read only when the peer named no address
// in X-Forwarded-For: a proxy that appends to X-Forwarded-For may pass a
// client's X-Real-IP through, so it never overrides an entry. It keeps nginx's
// `X-Real-IP $remote_addr` setup working. That setup forwards a client's
// X-Forwarded-For as sent, so ignoring X-Real-IP would not make it safe; it
// would only resolve a client that sends junk X-Forwarded-For to the proxy's
// own, local-looking address.
func (r *Resolver) ClientIP(req *http.Request) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	remoteIP := parseRemoteAddr(req.RemoteAddr)

	// If the connecting IP is not a trusted proxy, ignore forwarding headers.
	if !r.isTrusted(remoteIP) {
		return normalize(remoteIP)
	}

	if ip := r.forwardedFor(req.Header.Values("X-Forwarded-For")); ip != nil {
		return normalize(ip)
	}

	if realIP := req.Header.Values("X-Real-IP"); len(realIP) == 1 {
		if parsed := parseForwardedAddr(strings.TrimSpace(realIP[0])); parsed != nil {
			return normalize(parsed)
		}
	}

	return normalize(remoteIP)
}

// forwardedFor walks the X-Forwarded-For lines right to left and returns the
// first untrusted entry, the leftmost entry when all are trusted, or the
// trusted hop before an entry that is not an address. It returns nil when the
// peer named no address.
func (r *Resolver) forwardedFor(lines []string) net.IP {
	var last net.IP
	for i := len(lines) - 1; i >= 0; i-- {
		entries := strings.Split(lines[i], ",")
		for j := len(entries) - 1; j >= 0; j-- {
			entry := strings.TrimSpace(entries[j])
			if entry == "" {
				continue
			}
			ip := parseForwardedAddr(entry)
			if ip == nil {
				return last
			}
			if !r.isTrusted(ip) {
				return ip
			}
			last = ip
		}
	}
	return last
}

// parseForwardedAddr parses a forwarded address: a bare IP, or an IP with a
// port or in brackets, as some proxies write it. It returns nil otherwise.
func parseForwardedAddr(addr string) net.IP {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return net.ParseIP(host)
	}
	if len(addr) > 1 && addr[0] == '[' && addr[len(addr)-1] == ']' {
		addr = addr[1 : len(addr)-1]
	}
	return net.ParseIP(addr)
}

// parseRemoteAddr extracts the IP from r.RemoteAddr using net.SplitHostPort.
// Handles IPv4, IPv6 (bracket notation), and bare IPs without port.
func parseRemoteAddr(addr string) net.IP {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// No port — try parsing as bare IP
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return net.IPv4zero
	}
	return ip
}

func (r *Resolver) isTrusted(ip net.IP) bool {
	for _, cidr := range r.trusted {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// normalize returns the canonical string form of an IP.
func normalize(ip net.IP) string {
	if ip == nil {
		return "0.0.0.0"
	}
	return ip.String()
}

// UpdateTrustedCIDRs updates the Resolver's trusted proxy list in place.
// Safe to call concurrently with ClientIP — protected by RWMutex.
func (r *Resolver) UpdateTrustedCIDRs(cidrs []*net.IPNet) {
	r.mu.Lock()
	r.trusted = cidrs
	r.mu.Unlock()
}

// ReloadTrustedCIDRs serializes the authoritative store read and publication.
// Holding only the publication lock would let an older delayed read overwrite
// a newer configuration. A failed read retains the last valid trust boundary.
func (r *Resolver) ReloadTrustedCIDRs(ctx context.Context, store SettingsStore) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	cidrs, err := LoadTrustedCIDRs(ctx, store)
	if err != nil {
		return err
	}
	r.UpdateTrustedCIDRs(cidrs)
	return nil
}

// Forwarded protocols Traefik sends on a WebSocket upgrade in place of
// http and https.
const (
	forwardedProtoWS  = "ws"
	forwardedProtoWSS = "wss"
)

// requestScheme must run before Middleware replaces the transport peer address.
// Proxies must preserve Host (or name the client's host in X-Forwarded-Host)
// and overwrite X-Forwarded-Proto, never append it.
// On a WebSocket upgrade, Traefik sends "wss" or "ws" instead of "https" or
// "http"; those name the same transport security and are accepted there only.
func (r *Resolver) requestScheme(req *http.Request) string {
	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	if !r.peerTrusted(req) {
		return scheme
	}
	values := req.Header.Values("X-Forwarded-Proto")
	if len(values) == 0 {
		return scheme
	}
	if len(values) != 1 {
		return ""
	}
	switch values[0] {
	case "http", "https":
		return values[0]
	case forwardedProtoWS:
		if isWebSocketUpgrade(req) {
			return "http"
		}
	case forwardedProtoWSS:
		if isWebSocketUpgrade(req) {
			return "https"
		}
	}
	return ""
}

// requestHost is the single X-Forwarded-Host value a trusted proxy sent, or
// the Host header. A list, several headers or a value that is not a bare
// host[:port] is ambiguous and falls back to Host. Like requestScheme it must
// run before Middleware replaces the transport peer address.
func (r *Resolver) requestHost(req *http.Request) string {
	if !r.peerTrusted(req) {
		return req.Host
	}
	values := req.Header.Values("X-Forwarded-Host")
	if len(values) != 1 {
		return req.Host
	}
	host := strings.TrimSpace(values[0])
	if host == "" || strings.ContainsAny(host, ",/?#@ \t") {
		return req.Host
	}
	return host
}

// peerTrusted reports whether the transport peer of req is a trusted proxy.
func (r *Resolver) peerTrusted(req *http.Request) bool {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.isTrusted(peer)
}

// isWebSocketUpgrade reports whether req asks to upgrade to WebSocket: a
// Connection header carrying the "upgrade" token and Upgrade: websocket.
func isWebSocketUpgrade(req *http.Request) bool {
	if !strings.EqualFold(strings.TrimSpace(req.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, value := range req.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}
