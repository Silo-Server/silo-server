package notifications

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

// webhookIPAllowed reports whether a resolved destination IP is public
// (docs/architecture/notifications.md, "Trust model and SSRF guard"). The
// address classes are shared with every other user-supplied destination in
// netguard; IPv4-mapped IPv6 addresses are classified as their IPv4 form, so
// ::ffff:127.0.0.1 cannot bypass the IPv4 ranges.
func webhookIPAllowed(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	return ok && netguard.Classify(addr) == netguard.Public
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
