// Package remotestream contains the security boundary for remote media URLs.
// Callers must validate URLs immediately before use; validation performed when
// a plugin is configured is not sufficient because DNS and redirects can
// change between configuration and playback.
package remotestream

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

const maxURLLength = 8 << 10

var forbiddenPrefixes = mustPrefixes(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"::/128",
	"::1/128",
	"64:ff9b:1::/48",
	"100::/64",
	"2001::/23",
	"2001:db8::/32",
	"2002::/16",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
)

type ipResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// ValidatedURL is an HTTP(S) URL whose host resolved only to public addresses
// at validation time. Addresses returns a copy so the validation result cannot
// be mutated by callers.
type ValidatedURL struct {
	parsed    *url.URL
	addresses []netip.Addr
}

func (u *ValidatedURL) String() string {
	if u == nil || u.parsed == nil {
		return ""
	}
	return u.parsed.String()
}

func (u *ValidatedURL) URL() *url.URL {
	if u == nil || u.parsed == nil {
		return nil
	}
	clone := *u.parsed
	return &clone
}

func (u *ValidatedURL) Addresses() []netip.Addr {
	if u == nil {
		return nil
	}
	return append([]netip.Addr(nil), u.addresses...)
}

// ValidateURL accepts only absolute, externally reachable HTTP(S) URLs.
func ValidateURL(ctx context.Context, raw string) (*ValidatedURL, error) {
	return validateURL(ctx, raw, net.DefaultResolver)
}

// ValidateURLSyntax validates the non-network parts of a remote media URL.
// It is suitable for cheaply screening a provider's candidate list. Callers
// must still call ValidateURL immediately before fetching a selected URL.
func ValidateURLSyntax(raw string) (*url.URL, error) {
	return validateURLSyntax(raw)
}

func validateURL(ctx context.Context, raw string, resolver ipResolver) (*ValidatedURL, error) {
	parsed, err := validateURLSyntax(raw)
	if err != nil {
		return nil, err
	}
	addresses, err := resolvePublicAddresses(ctx, resolver, parsed.Hostname())
	if err != nil {
		return nil, err
	}
	return &ValidatedURL{parsed: parsed, addresses: addresses}, nil
}

func validateURLSyntax(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURLLength {
		return nil, errors.New("remote stream URL is empty or too long")
	}
	if strings.ContainsAny(raw, "\x00\r\n\t") {
		return nil, errors.New("remote stream URL contains control characters")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || !parsed.IsAbs() || parsed.Opaque != "" {
		return nil, errors.New("remote stream URL must be absolute")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("remote stream URL must use HTTP or HTTPS")
	}
	if parsed.User != nil || parsed.Hostname() == "" {
		return nil, errors.New("remote stream URL has an invalid host")
	}
	if strings.EqualFold(parsed.Hostname(), "localhost") ||
		strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".localhost") {
		return nil, errors.New("remote stream URL targets localhost")
	}
	if address, err := netip.ParseAddr(parsed.Hostname()); err == nil && forbiddenAddress(address.Unmap()) {
		return nil, errors.New("remote stream URL targets a non-public address")
	}
	return parsed, nil
}

func resolvePublicAddresses(ctx context.Context, resolver ipResolver, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if forbiddenAddress(addr) {
			return nil, errors.New("remote stream URL targets a non-public address")
		}
		return []netip.Addr{addr}, nil
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		// Resolver errors frequently echo the complete host. Provider tokens
		// can be embedded in hostnames, so keep the error credential-free.
		return nil, errors.New("resolve remote stream host: lookup failed")
	}
	if len(addresses) == 0 {
		return nil, errors.New("remote stream host has no address")
	}
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if forbiddenAddress(address) {
			return nil, errors.New("remote stream host resolves to a non-public address")
		}
		result = append(result, address)
	}
	return result, nil
}

func forbiddenAddress(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() ||
		address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return true
	}
	for _, prefix := range forbiddenPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func mustPrefixes(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}

// RedactURL returns a URL safe for logs. Provider credentials commonly appear
// in both query strings and path segments, so neither is retained.
func RedactURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<redacted-url>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if parsed.Path != "" && parsed.Path != "/" {
		parsed.Path = "/<redacted>"
		parsed.RawPath = ""
	}
	return parsed.String()
}
