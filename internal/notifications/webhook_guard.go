package notifications

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

// webhookIPAllowed applies the shared outbound policy. Private access never
// permits the Blocked class (including mapped and translated blocked addresses).
func webhookIPAllowed(addr netip.Addr, allowPrivate bool) bool {
	return netguard.CheckAddr(addr, allowPrivate) == nil
}

// ValidateWebhookURL enforces the destination guardrails the profile cannot
// opt out of: HTTPS only, no URL credentials, and resolution to allowed addresses.
// The admin's private-destination switch permits Private, never Blocked addresses.
// Returns the host for the denormalized url_host column.
func ValidateWebhookURL(rawURL string, allowPrivate bool) (host string, err error) {
	return validateWebhookURL(rawURL, allowPrivate, net.DefaultResolver.LookupNetIP)
}

const webhookLookupTimeout = 2 * time.Second

func validateWebhookURL(rawURL string, allowPrivate bool, lookup func(context.Context, string, string) ([]netip.Addr, error)) (host string, err error) {
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
	if addr, err := netip.ParseAddr(host); err == nil {
		if !webhookIPAllowed(addr, allowPrivate) {
			return "", fmt.Errorf("webhook destinations on private or special-use networks are not allowed")
		}
		return host, nil
	}

	// Registration-time resolution check. Delivery-time re-validation happens
	// in the HTTP client's dialer (DNS rebinding mitigation), so a host that
	// later resolves to a disallowed address is still refused.
	ctx, cancel := context.WithTimeout(context.Background(), webhookLookupTimeout)
	defer cancel()
	addrs, err := lookup(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return "", fmt.Errorf("webhook host could not be resolved")
	}
	for _, addr := range addrs {
		if !webhookIPAllowed(addr, allowPrivate) {
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
