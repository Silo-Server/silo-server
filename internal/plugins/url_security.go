package plugins

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// validateProviderStreamURL accepts only externally reachable HTTP(S) URLs.
// Plugin output is fetched by ffprobe/FFmpeg from the server, so accepting a
// loopback, link-local, private, or otherwise non-global destination would
// turn a compromised plugin into an SSRF primitive.
func validateProviderStreamURL(ctx context.Context, raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || !parsed.IsAbs() {
		return "", errors.New("stream URL must be absolute")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("stream URL must use HTTP or HTTPS")
	}
	if parsed.User != nil || parsed.Hostname() == "" {
		return "", errors.New("stream URL has an invalid host")
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if forbiddenProviderIP(ip) {
			return "", fmt.Errorf("stream URL targets a non-public address")
		}
		return parsed.String(), nil
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return "", errors.New("stream URL targets localhost")
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return "", fmt.Errorf("resolve stream URL host: %w", err)
	}
	if len(ips) == 0 {
		return "", errors.New("stream URL host has no address")
	}
	for _, ip := range ips {
		if forbiddenProviderIP(ip) {
			return "", fmt.Errorf("stream URL host resolves to a non-public address")
		}
	}
	return parsed.String(), nil
}

func forbiddenProviderIP(ip net.IP) bool {
	return ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}
