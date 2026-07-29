package ratelimit

import (
	"context"
	"testing"
)

type mapSettingsStore map[string]string

func (s mapSettingsStore) Get(_ context.Context, key string) (string, error) {
	return s[key], nil
}

func (s mapSettingsStore) Set(_ context.Context, key, value string) error {
	s[key] = value
	return nil
}

func (s mapSettingsStore) GetAll(_ context.Context) (map[string]string, error) {
	return s, nil
}

func TestLoadConfigFallsBackFromRatesOutsideLimiterBounds(t *testing.T) {
	defaults := DefaultConfig()
	store := mapSettingsStore{
		"ratelimit.global.requests_per_second":        "1e308",
		"ratelimit.tier.standard.requests_per_second": "1e308",
		"ratelimit.ip.requests_per_minute":            "1e308",
		"ratelimit.ip.burst":                          "9223372036854775807",
		"ratelimit.auth.login.requests_per_minute":    "1e308",
	}

	cfg, err := LoadConfig(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.GlobalReqPerSecond != defaults.GlobalReqPerSecond {
		t.Errorf("global requests per second = %g, want default %g", cfg.GlobalReqPerSecond, defaults.GlobalReqPerSecond)
	}
	if cfg.Tiers["standard"].RequestsPerSecond != defaults.Tiers["standard"].RequestsPerSecond {
		t.Errorf("standard requests per second = %g, want default %g", cfg.Tiers["standard"].RequestsPerSecond, defaults.Tiers["standard"].RequestsPerSecond)
	}
	if cfg.IPReqPerMinute != defaults.IPReqPerMinute {
		t.Errorf("IP requests per minute = %g, want default %g", cfg.IPReqPerMinute, defaults.IPReqPerMinute)
	}
	if cfg.IPBurst != defaults.IPBurst {
		t.Errorf("IP burst = %d, want default %d", cfg.IPBurst, defaults.IPBurst)
	}
	if cfg.AuthEndpoints["login"].RequestsPerMinute != defaults.AuthEndpoints["login"].RequestsPerMinute {
		t.Errorf("login requests per minute = %g, want default %g", cfg.AuthEndpoints["login"].RequestsPerMinute, defaults.AuthEndpoints["login"].RequestsPerMinute)
	}
}
