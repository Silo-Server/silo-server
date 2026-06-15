package notifications

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// webhookDeniedNetworks are the private/special ranges webhook destinations
// must never resolve to (docs/superpowers/plans/notifications/04, "Trust
// Model"). IPv4-mapped IPv6 addresses are unwrapped before checking, so the
// IPv4 entries also cover ::ffff:0:0/96 bypass attempts.
var webhookDeniedNetworks = func() []*net.IPNet {
	cidrs := []string{
		// IPv4 private/special.
		"0.0.0.0/8",
		"10.0.0.0/8",
		"100.64.0.0/10",  // CGNAT (RFC 6598)
		"127.0.0.0/8",    // loopback
		"169.254.0.0/16", // link-local
		"172.16.0.0/12",
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"192.88.99.0/24",  // deprecated 6to4 anycast
		"192.168.0.0/16",
		"198.18.0.0/15", // benchmarking (RFC 2544)
		"224.0.0.0/4",   // multicast
		"240.0.0.0/4",   // reserved future use (incl. broadcast)
		// IPv6 private/special.
		"::/128",        // unspecified
		"::1/128",       // loopback
		"fc00::/7",      // ULA
		"fe80::/10",     // link-local
		"2001:db8::/32", // documentation
		"64:ff9b::/96",  // NAT64
	}
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("invalid webhook deny CIDR %q: %v", cidr, err))
		}
		networks = append(networks, network)
	}
	return networks
}()

// webhookIPAllowed reports whether a resolved destination IP is outside every
// denied range. v4-mapped IPv6 addresses are unwrapped and re-checked against
// the IPv4 deny set — a literal ::ffff:127.0.0.1 reaches loopback while
// bypassing naive IPv4-only checks.
func webhookIPAllowed(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, network := range webhookDeniedNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

// ValidateWebhookURL enforces the destination guardrails the profile cannot
// opt out of: HTTPS only, a well-formed host, and (unless the admin enabled
// private destinations for development) resolution to public addresses only.
// Returns the host for the denormalized url_host column.
func ValidateWebhookURL(rawURL string, allowPrivate bool) (host string, err error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != schemeHTTPS {
		return "", fmt.Errorf("webhook URLs must use https")
	}
	host = parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("webhook URL has no host")
	}
	if len(host) > 253 {
		return "", fmt.Errorf("webhook URL host is too long")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("webhook URLs must not embed credentials")
	}
	if allowPrivate {
		return host, nil
	}

	if ip := net.ParseIP(host); ip != nil {
		if !webhookIPAllowed(ip) {
			return "", fmt.Errorf("webhook destinations on private or special-use networks are not allowed")
		}
		return host, nil
	}

	// Registration-time resolution check. Delivery-time re-validation happens
	// in the HTTP client's dialer (DNS rebinding mitigation), so a host that
	// later starts resolving privately is still refused.
	addrs, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("webhook host could not be resolved")
	}
	for _, addr := range addrs {
		if !webhookIPAllowed(addr) {
			return "", fmt.Errorf("webhook destinations on private or special-use networks are not allowed")
		}
	}
	return host, nil
}

// discordWebhookURL matches Discord channel webhook endpoints for type
// auto-detection.
func discordWebhookURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "discord.com", "discordapp.com", "ptb.discord.com", "canary.discord.com":
	default:
		return false
	}
	return strings.HasPrefix(parsed.Path, "/api/webhooks/")
}
