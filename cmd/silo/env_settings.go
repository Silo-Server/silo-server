package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

// bootstrapPoolSize is the size of the first database pool: what
// SILO_DB_MAX_CONNECTIONS sets, or 20 until the stored setting is read.
func bootstrapPoolSize(env config.EnvSettings) int {
	if raw, ok := env.Values(config.EnvScopeProcess)["database.max_connections"]; ok {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	return 20
}

// applyEnvSettings checks the settings the environment sets the way an admin
// save checks them, stores the cluster-wide ones that differ from stored in
// one batch (through the encrypting repo, so secrets rest encrypted), and
// overlays every one on settings, the map startup builds its config from. It
// returns the checked values and the stored keys it changed.
func applyEnvSettings(ctx context.Context, repo *catalog.EncryptedSettingsRepo, settings map[string]string, env config.EnvSettings, redisBootstrapURL string) (config.EnvSettings, []string, error) {
	if len(env) == 0 {
		return env, nil, nil
	}
	normalizedRedis, redisErr := config.NormalizeRedisURL(redisBootstrapURL)
	checked, err := handlers.CheckEnvironmentSettings(settings, env, normalizedRedis != "" && redisErr == nil)
	if err != nil {
		return nil, nil, err
	}
	changed := map[string]string{}
	for key, value := range checked.Values(config.EnvScopeStored) {
		if settings[key] != value {
			changed[key] = value
		}
	}
	if len(changed) > 0 {
		if err := repo.SetMany(ctx, changed); err != nil {
			return nil, nil, fmt.Errorf("storing settings from the environment: %w", err)
		}
	}
	maps.Copy(settings, checked.All())
	return checked, slices.Sorted(maps.Keys(changed)), nil
}

// addEnvSettingLocks records the settings the environment sets in the maps
// the admin settings handlers read: the keys an admin cannot save here, the
// values in effect, and the variable that sets each.
func addEnvSettingLocks(env config.EnvSettings, configured map[string]bool, values, vars map[string]string) {
	for _, setting := range env {
		configured[setting.Key] = true
		values[setting.Key] = setting.Value
		vars[setting.Key] = setting.Var
	}
}
