package main

import (
	"maps"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestApplyBootstrapOverridesTakesRedisFromTheEnvironment(t *testing.T) {
	saved := config.RedisConfig{URL: "redis://saved.example.invalid:6379/3", DB: "5"}

	cfg := &config.Config{Redis: saved}
	applyBootstrapOverrides(cfg, &config.BootstrapConfig{
		Listen:      ":8080",
		JFListen:    ":8096",
		Mode:        "integrated",
		DatabaseURL: "postgres://db.example.invalid/silo",
	})
	if cfg.Redis != saved {
		t.Errorf("without REDIS_URL Redis = %+v, want the saved %+v", cfg.Redis, saved)
	}
	if cfg.Server.Listen != ":8080" || cfg.JellyfinCompat.Listen != ":8096" ||
		cfg.Server.Mode != "integrated" || cfg.Database.URL != "postgres://db.example.invalid/silo" {
		t.Errorf("listen = %q, Jellyfin listen = %q, mode = %q, database URL = %q; want the environment's values",
			cfg.Server.Listen, cfg.JellyfinCompat.Listen, cfg.Server.Mode, cfg.Database.URL)
	}

	// REDIS_URL names the whole connection, so the saved redis.db goes too.
	cfg = &config.Config{Redis: saved}
	applyBootstrapOverrides(cfg, &config.BootstrapConfig{RedisURL: "redis://env.example.invalid:6379/1"})
	if want := (config.RedisConfig{URL: "redis://env.example.invalid:6379/1"}); cfg.Redis != want {
		t.Errorf("with REDIS_URL Redis = %+v, want %+v", cfg.Redis, want)
	}
	if db, ok := cfg.Redis.Database(); !ok || db != 1 {
		t.Errorf("with REDIS_URL the database number = (%d, %v), want the 1 in REDIS_URL", db, ok)
	}
}

func TestAddRedisBootstrapSettings(t *testing.T) {
	configured, values := map[string]bool{}, map[string]string{}
	addRedisBootstrapSettings("", configured, values)
	if len(configured) != 0 || len(values) != 0 {
		t.Errorf("without REDIS_URL: configured = %v, values = %v; want nothing managed by the environment", configured, values)
	}

	for redisURL, wantDB := range map[string]string{
		"redis://env.example.invalid:6379/1":                        "1",
		"redis://env.example.invalid:6379":                          "0",
		"redis://sentinel.example.invalid:26379/2?master_name=silo": "2",
		// A bare address reaches the event bus only, which stays on database 0.
		"env.example.invalid:6379": "0",
	} {
		configured, values := map[string]bool{}, map[string]string{}
		addRedisBootstrapSettings(redisURL, configured, values)
		wantConfigured := map[string]bool{"redis.url": true, config.RedisDBSettingKey: true}
		wantValues := map[string]string{"redis.url": redisURL, config.RedisDBSettingKey: wantDB}
		if !maps.Equal(configured, wantConfigured) || !maps.Equal(values, wantValues) {
			t.Errorf("REDIS_URL=%q: configured = %v, values = %v; want %v and %v",
				redisURL, configured, values, wantConfigured, wantValues)
		}
	}
}
