package ratelimit

import (
	"context"
	"fmt"
	"sort"
	"strconv"
)

// SettingsStore is the interface for reading/writing server_settings.
// Satisfied by *catalog.ServerSettingsRepo.
type SettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	GetAll(ctx context.Context) (map[string]string, error)
}

type settingsBatchWriter interface {
	SetMany(ctx context.Context, values map[string]string) error
}

// LoadConfig reads rate limit settings from the settings store.
// Missing keys fall back to DefaultConfig() values.
func LoadConfig(ctx context.Context, store SettingsStore) (Config, error) {
	all, err := store.GetAll(ctx)
	if err != nil {
		return Config{}, fmt.Errorf("load rate limit config: %w", err)
	}
	return ConfigFromSettings(all), nil
}

// ConfigFromSettings resolves rate-limit configuration from an already-read
// server_settings snapshot. Admin mutations use it while holding the shared
// settings transaction lock so validation and writes cannot race.
func ConfigFromSettings(all map[string]string) Config {
	defaults := DefaultConfig()
	cfg := Config{
		Enabled:            parseBool(all, "ratelimit.enabled", defaults.Enabled),
		GlobalReqPerSecond: parseRate(all, "ratelimit.global.requests_per_second", defaults.GlobalReqPerSecond, MaxGlobalRequestsPerSecond),
		Tiers:              make(map[string]TierConfig),
	}

	for name, tier := range defaults.Tiers {
		prefix := "ratelimit.tier." + name + "."
		cfg.Tiers[name] = TierConfig{
			RequestsPerSecond: parseRate(all, prefix+"requests_per_second", tier.RequestsPerSecond, MaxRequestsPerWindow),
			RequestsPerMinute: parseRate(all, prefix+"requests_per_minute", tier.RequestsPerMinute, MaxRequestsPerWindow),
			Burst:             parseInt(all, prefix+"burst", tier.Burst),
		}
	}

	cfg.IPReqPerSecond = parseRate(all, "ratelimit.ip.requests_per_second", defaults.IPReqPerSecond, MaxRequestsPerWindow)
	cfg.IPReqPerMinute = parseRate(all, "ratelimit.ip.requests_per_minute", defaults.IPReqPerMinute, MaxRequestsPerWindow)
	cfg.IPBurst = parseInt(all, "ratelimit.ip.burst", defaults.IPBurst)

	cfg.AuthEndpoints = make(map[string]AuthEndpointConfig)
	for name, ep := range defaults.AuthEndpoints {
		prefix := "ratelimit.auth." + name + "."
		cfg.AuthEndpoints[name] = AuthEndpointConfig{
			RequestsPerMinute: parseRate(all, prefix+"requests_per_minute", ep.RequestsPerMinute, MaxRequestsPerWindow),
			Burst:             parseInt(all, prefix+"burst", ep.Burst),
		}
	}

	return cfg
}

// SeedDefaults writes default rate limit settings if they don't exist yet.
func SeedDefaults(ctx context.Context, store SettingsStore) error {
	all, err := store.GetAll(ctx)
	if err != nil {
		return fmt.Errorf("seed rate limit defaults: %w", err)
	}

	defaults := map[string]string{
		"ratelimit.enabled":                                "true",
		"ratelimit.global.requests_per_second":             "1000",
		"ratelimit.tier.standard.requests_per_second":      "20",
		"ratelimit.tier.standard.requests_per_minute":      "1200",
		"ratelimit.tier.standard.burst":                    "20",
		"ratelimit.tier.elevated.requests_per_second":      "100",
		"ratelimit.tier.elevated.requests_per_minute":      "6000",
		"ratelimit.tier.elevated.burst":                    "100",
		"ratelimit.ip.requests_per_second":                 "120",
		"ratelimit.ip.requests_per_minute":                 "6000",
		"ratelimit.ip.burst":                               "120",
		"ratelimit.auth.login.requests_per_minute":         "20",
		"ratelimit.auth.login.burst":                       "10",
		"ratelimit.auth.signup.requests_per_minute":        "10",
		"ratelimit.auth.signup.burst":                      "6",
		"ratelimit.auth.setup.requests_per_minute":         "10",
		"ratelimit.auth.setup.burst":                       "6",
		"ratelimit.auth.device_start.requests_per_minute":  "20",
		"ratelimit.auth.device_start.burst":                "10",
		"ratelimit.auth.device_lookup.requests_per_minute": "60",
		"ratelimit.auth.device_lookup.burst":               "20",
		"ratelimit.auth.device_poll.requests_per_minute":   "120",
		"ratelimit.auth.device_poll.burst":                 "30",
	}

	// Sort keys for deterministic seeding order
	keys := make([]string, 0, len(defaults))
	for k := range defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if _, exists := all[key]; !exists {
			if err := store.Set(ctx, key, defaults[key]); err != nil {
				return fmt.Errorf("seed default %s: %w", key, err)
			}
		}
	}
	return nil
}

// ConfigSettings serializes a runtime rate-limit config into server_settings
// values. Admin callers use the same mapping when they include the backend in
// one atomic transaction, avoiding drift between the dedicated endpoint and
// the runtime loader.
func ConfigSettings(cfg Config) map[string]string {
	pairs := map[string]string{
		"ratelimit.enabled":                    strconv.FormatBool(cfg.Enabled),
		"ratelimit.global.requests_per_second": strconv.FormatFloat(cfg.GlobalReqPerSecond, 'f', -1, 64),
	}
	for name, tier := range cfg.Tiers {
		prefix := "ratelimit.tier." + name + "."
		pairs[prefix+"requests_per_second"] = strconv.FormatFloat(tier.RequestsPerSecond, 'f', -1, 64)
		pairs[prefix+"requests_per_minute"] = strconv.FormatFloat(tier.RequestsPerMinute, 'f', -1, 64)
		pairs[prefix+"burst"] = strconv.Itoa(tier.Burst)
	}
	pairs["ratelimit.ip.requests_per_second"] = strconv.FormatFloat(cfg.IPReqPerSecond, 'f', -1, 64)
	pairs["ratelimit.ip.requests_per_minute"] = strconv.FormatFloat(cfg.IPReqPerMinute, 'f', -1, 64)
	pairs["ratelimit.ip.burst"] = strconv.Itoa(cfg.IPBurst)
	for name, ep := range cfg.AuthEndpoints {
		prefix := "ratelimit.auth." + name + "."
		pairs[prefix+"requests_per_minute"] = strconv.FormatFloat(ep.RequestsPerMinute, 'f', -1, 64)
		pairs[prefix+"burst"] = strconv.Itoa(ep.Burst)
	}
	return pairs
}

// SaveConfig persists rate limit settings to the store. Production stores
// support SetMany, so the full config changes atomically; the per-key fallback
// keeps small test and embedding stores backwards-compatible.
func SaveConfig(ctx context.Context, store SettingsStore, cfg Config) error {
	pairs := ConfigSettings(cfg)
	if writer, ok := store.(settingsBatchWriter); ok {
		if err := writer.SetMany(ctx, pairs); err != nil {
			return fmt.Errorf("save rate limit config: %w", err)
		}
		return nil
	}
	for key, value := range pairs {
		if err := store.Set(ctx, key, value); err != nil {
			return fmt.Errorf("save rate limit config %s: %w", key, err)
		}
	}
	return nil
}

// parseBool/parseRate/parseInt are lenient helpers (silent fallback on parse error).
// This is intentional: runtime config should not crash on a bad value in the DB.

func parseBool(m map[string]string, key string, def bool) bool {
	if v, ok := m[key]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// parseRate returns the stored value for key, falling back to def if the key
// is missing, unparseable, outside the limiter's safe integer range, or <= 0.
func parseRate(m map[string]string, key string, def, maxValue float64) float64 {
	if v, ok := m[key]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= maxValue {
			return f
		}
	}
	return def
}

// parseInt returns the stored value for key, falling back to def if the key is
// missing, unparseable, non-positive, or outside the limiter's portable range.
func parseInt(m map[string]string, key string, def int) int {
	if v, ok := m[key]; ok {
		if i, err := strconv.Atoi(v); err == nil && i > 0 && i <= MaxBurst {
			return i
		}
	}
	return def
}
