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
func (r *Resolver) ClientIP(req *http.Request) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	remoteIP := parseRemoteAddr(req.RemoteAddr)

	// If the connecting IP is not a trusted proxy, ignore forwarding headers.
	if !r.isTrusted(remoteIP) {
		return normalize(remoteIP)
	}

	// Check X-Forwarded-For: walk right-to-left, return first untrusted IP.
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		for i := len(ips) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(ips[i])
			if candidate == "" {
				continue
			}
			parsed := net.ParseIP(candidate)
			if parsed == nil {
				continue
			}
			if !r.isTrusted(parsed) {
				return normalize(parsed)
			}
		}
		// All IPs in chain are trusted — return leftmost
		for _, raw := range ips {
			candidate := strings.TrimSpace(raw)
			if parsed := net.ParseIP(candidate); parsed != nil {
				return normalize(parsed)
			}
		}
	}

	// Fallback: X-Real-IP
	if realIP := req.Header.Get("X-Real-IP"); realIP != "" {
		if parsed := net.ParseIP(realIP); parsed != nil {
			return normalize(parsed)
		}
	}

	return normalize(remoteIP)
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

// requestScheme must run before Middleware replaces the transport peer address.
// Proxies must preserve Host and overwrite X-Forwarded-Proto, never append it.
// On a WebSocket upgrade, Traefik sends "wss" or "ws" instead of "https" or
// "http"; those name the same transport security and are accepted there only.
func (r *Resolver) requestScheme(req *http.Request) string {
	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || r == nil {
		return scheme
	}
	r.mu.RLock()
	trusted := r.isTrusted(peer)
	r.mu.RUnlock()
	if !trusted {
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
	case "ws", "wss":
		if isWebSocketUpgrade(req) {
			return map[string]string{"ws": "http", "wss": "https"}[values[0]]
		}
	}
	return ""
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
