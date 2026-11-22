package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/serversettings"
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

// deploymentAIProviders are the AI providers the deployment document
// declares. Without an ai_providers section (declared false) the stored
// registry is left as it is; an empty section releases every provider.
type deploymentAIProviders struct {
	declared  bool
	providers []config.AIProvider
	variable  string
}

// applyEnvSettings checks the settings the environment sets the way an admin
// save checks them, stores the cluster-wide ones that differ from stored in
// one batch (through the encrypting repo, so secrets rest encrypted), and
// overlays every one on settings, the map startup builds its config from. It
// returns the checked values and the stored keys it changed.
//
// The AI providers the deployment document declares go into the registry in
// the same batch, checked with the environment's values, so a
// recommendations variable can name one of them and the recommendation
// engine starts with them on this start.
//
// The check and the write share one settings transaction, as an admin save's
// do, so the check sees the rows the write replaces: a storage transition
// another server commits while this one starts cannot slip between them.
func applyEnvSettings(ctx context.Context, repo *serversettings.EncryptedSettingsRepo, settings map[string]string, env config.EnvSettings, ai deploymentAIProviders, redisBootstrapURL string) (config.EnvSettings, []string, error) {
	if len(env) == 0 && !ai.declared {
		return env, nil, nil
	}
	normalizedRedis, redisErr := config.NormalizeRedisURL(redisBootstrapURL)
	redisBootstrapAvailable := normalizedRedis != "" && redisErr == nil
	var checked config.EnvSettings
	var changed, snapshot, declared map[string]string
	var checkErr error
	err := repo.UpdateAtomic(ctx, func(current map[string]string) (map[string]string, error) {
		declared = nil
		if ai.declared {
			registry, err := config.ApplyDeclaredAIProviders(current[config.AIProvidersSettingKey], ai.providers, ai.variable)
			if err != nil {
				checkErr = fmt.Errorf("%s: ai_providers: %w", ai.variable, err)
				return nil, checkErr
			}
			declared = map[string]string{config.AIProvidersSettingKey: registry}
		}
		if checked, checkErr = handlers.CheckDeploymentSettings(current, env, declared, ai.variable, redisBootstrapAvailable); checkErr != nil {
			return nil, checkErr
		}
		snapshot = maps.Clone(current)
		changed = map[string]string{}
		for key, value := range declared {
			if current[key] != value {
				changed[key] = value
			}
		}
		for key, value := range checked.Values(config.EnvScopeStored) {
			if current[key] != value {
				changed[key] = value
			}
		}
		return changed, nil
	})
	if checkErr != nil {
		// Already names the variable.
		return nil, nil, checkErr
	}
	if err != nil {
		return nil, nil, fmt.Errorf("storing settings from the environment: %w", err)
	}
	// Startup builds its config from the rows the check ran against, not from
	// the earlier read, which another server may have changed since.
	clear(settings)
	maps.Copy(settings, snapshot)
	maps.Copy(settings, declared)
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
