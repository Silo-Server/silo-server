package clientip

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
)

// SettingsStore is satisfied by *catalog.ServerSettingsRepo.
type SettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	GetAll(ctx context.Context) (map[string]string, error)
}

const (
	// SettingTrustedProxies is the server_settings key holding the
	// comma-separated CIDR list of trusted reverse proxies.
	SettingTrustedProxies = "clientip.trusted_proxies"
	// EnvTrustedProxies overrides SettingTrustedProxies at startup when set.
	EnvTrustedProxies = "SILO_TRUSTED_PROXIES"
	// DefaultTrustedProxies: RFC 1918 private ranges + loopback.
	DefaultTrustedProxies = "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.0/8,::1/128"
)

// SeedDefaults ensures the trusted proxy setting exists. When the
// SILO_TRUSTED_PROXIES environment variable is set it wins: the value is
// validated and persisted so the admin UI shows the effective config (and is
// re-applied on every startup while the variable remains set). Otherwise the
// default list is written only if the setting is unset.
func SeedDefaults(ctx context.Context, store SettingsStore) error {
	if env := strings.TrimSpace(os.Getenv(EnvTrustedProxies)); env != "" {
		normalized, err := NormalizeCIDRList(env)
		if err != nil {
			return fmt.Errorf("invalid %s: %w", EnvTrustedProxies, err)
		}
		existing, err := store.Get(ctx, SettingTrustedProxies)
		if err != nil {
			return fmt.Errorf("seed clientip defaults: %w", err)
		}
		if existing == normalized {
			return nil
		}
		return store.Set(ctx, SettingTrustedProxies, normalized)
	}
	existing, err := store.Get(ctx, SettingTrustedProxies)
	if err != nil {
		return fmt.Errorf("seed clientip defaults: %w", err)
	}
	if existing != "" {
		return nil
	}
	return store.Set(ctx, SettingTrustedProxies, DefaultTrustedProxies)
}

// LoadTrustedCIDRs reads the trusted proxy CIDRs from settings.
func LoadTrustedCIDRs(ctx context.Context, store SettingsStore) ([]*net.IPNet, error) {
	raw, err := store.Get(ctx, SettingTrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("load trusted proxies: %w", err)
	}
	if raw == "" {
		raw = DefaultTrustedProxies
	}
	return ParseCIDRs(raw)
}

// NormalizeCIDRList validates a comma-separated CIDR list and returns it in
// canonical form (trimmed entries joined by ", "). An empty list is valid and
// means "use the built-in defaults".
func NormalizeCIDRList(raw string) (string, error) {
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(p); err != nil {
			return "", fmt.Errorf("invalid CIDR %q (expected e.g. 203.0.113.7/32)", p)
		}
		out = append(out, p)
	}
	return strings.Join(out, ", "), nil
}

// ParseCIDRs parses a comma-separated list of CIDR strings.
func ParseCIDRs(raw string) ([]*net.IPNet, error) {
	parts := strings.Split(raw, ",")
	var cidrs []*net.IPNet
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, cidr, err := net.ParseCIDR(p)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", p, err)
		}
		cidrs = append(cidrs, cidr)
	}
	return cidrs, nil
}
