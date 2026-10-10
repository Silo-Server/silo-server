package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// WatchSyncProviderConfig returns the installation's global configuration in
// the transient, field-classified shape used by watch-sync RPCs. Undeclared
// fields are treated as secret so manifest drift cannot expose credentials.
func (s *Service) WatchSyncProviderConfig(
	ctx context.Context,
	installationID int,
) (*pluginv1.WatchSyncProviderConfig, error) {
	manifest, err := s.manifestForInstallation(ctx, installationID, false)
	if err != nil {
		return nil, err
	}
	if s.configs == nil {
		return &pluginv1.WatchSyncProviderConfig{}, nil
	}
	configs, err := s.configs.ListGlobalConfigs(ctx, installationID)
	if err != nil {
		return nil, fmt.Errorf("list watch sync plugin config: %w", err)
	}
	return watchSyncProviderConfig(manifest, configs)
}

func watchSyncProviderConfig(
	manifest *pluginv1.PluginManifest,
	configs []*RuntimeConfig,
) (*pluginv1.WatchSyncProviderConfig, error) {
	result := &pluginv1.WatchSyncProviderConfig{
		Values:       make(map[string]string),
		SecretValues: make(map[string]string),
	}
	sort.Slice(configs, func(i, j int) bool {
		if configs[i] == nil {
			return false
		}
		if configs[j] == nil {
			return true
		}
		return configs[i].Key < configs[j].Key
	})
	for _, config := range configs {
		if config == nil {
			continue
		}
		configKey := strings.TrimSpace(config.Key)
		if configKey == "" {
			continue
		}
		publicFields, _ := GlobalConfigFieldSets(manifest, configKey)
		public := stringSet(publicFields)
		for field, raw := range config.Value {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			value, err := watchSyncConfigString(raw)
			if err != nil {
				return nil, fmt.Errorf("encode watch sync plugin config %q.%s: %w", configKey, field, err)
			}
			key := configKey + "." + field
			if _, isPublic := public[field]; isPublic {
				result.Values[key] = value
				continue
			}
			// Explicitly secret and undeclared fields both take the protected path.
			result.SecretValues[key] = value
		}
	}
	return result, nil
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func watchSyncConfigString(value any) (string, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// WatchSyncConfigReady reports whether every global config entry the
// installation's manifest marks required has a saved value, so a watch-sync
// plugin that needs provider app credentials can say whether a profile can
// connect yet.
func (s *Service) WatchSyncConfigReady(ctx context.Context, installationID int) (bool, error) {
	manifest, err := s.manifestForInstallation(ctx, installationID, false)
	if err != nil {
		return false, err
	}
	var required []string
	for _, schema := range manifest.GetGlobalConfigSchema() {
		if schema.GetRequired() {
			required = append(required, schema.GetKey())
		}
	}
	if len(required) == 0 {
		return true, nil
	}
	if s.configs == nil {
		return false, nil
	}
	configs, err := s.configs.ListGlobalConfigs(ctx, installationID)
	if err != nil {
		return false, fmt.Errorf("list watch sync plugin config: %w", err)
	}
	saved := savedGlobalConfigKeys(configs)
	for _, key := range required {
		if !saved[key] {
			return false, nil
		}
	}
	return true, nil
}

// globalConfigHasValue reports whether the installation has a saved, non-empty
// global config entry for key.
func (s *Service) globalConfigHasValue(ctx context.Context, installationID int, key string) (bool, error) {
	configs, err := s.configs.ListGlobalConfigs(ctx, installationID)
	if err != nil {
		return false, fmt.Errorf("list plugin config: %w", err)
	}
	return savedGlobalConfigKeys(configs)[key], nil
}

// savedGlobalConfigKeys lists the config keys with at least one non-blank value.
func savedGlobalConfigKeys(configs []*RuntimeConfig) map[string]bool {
	saved := make(map[string]bool, len(configs))
	for _, config := range configs {
		if config == nil {
			continue
		}
		for _, value := range config.Value {
			if text, isText := value.(string); !isText || strings.TrimSpace(text) != "" {
				saved[config.Key] = true
				break
			}
		}
	}
	return saved
}

// InstalledFromSiloRepository reports whether an installation came from a
// Silo-managed plugin repository, rather than from a repository an admin added
// or from an uploaded archive.
func (s *Service) InstalledFromSiloRepository(ctx context.Context, installation *Installation) (bool, error) {
	if installation == nil || installation.RepositoryID == nil || s.repositories == nil {
		return false, nil
	}
	repository, err := s.repositories.GetByID(ctx, *installation.RepositoryID)
	if errors.Is(err, ErrRepositoryNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read plugin repository %d: %w", *installation.RepositoryID, err)
	}
	return repository.SourceKind == RepositorySourceSilo, nil
}

// SeedGlobalConfig saves value under key only when the installation has no
// saved config for key yet, keeping just the fields the manifest declares for
// that key. It reports whether it saved the seed; false with no error means
// the key already holds a value. The write is create-only, so a seed that
// races an admin's save, or another node's seed, never overwrites what landed
// first. A seed that ends with no value saved, because the manifest declares
// none of its fields or the saved entry is empty, is an error.
func (s *Service) SeedGlobalConfig(ctx context.Context, installationID int, key string, value map[string]any) (bool, error) {
	if s.configs == nil {
		return false, nil
	}
	manifest, err := s.manifestForInstallation(ctx, installationID, false)
	if err != nil {
		return false, err
	}
	publicFields, secretFields := GlobalConfigFieldSets(manifest, key)
	declared := stringSet(append(publicFields, secretFields...))
	seed := make(map[string]any, len(value))
	for field, fieldValue := range value {
		if _, ok := declared[field]; !ok {
			continue
		}
		if text, isText := fieldValue.(string); isText && strings.TrimSpace(text) == "" {
			continue
		}
		seed[field] = fieldValue
	}
	if len(seed) == 0 {
		return false, fmt.Errorf("plugin config %q declares none of the fields to seed", key)
	}
	if err := ValidateGlobalConfigValue(manifest, key, seed); err != nil {
		return false, &ConfigValidationError{Message: err.Error(), Cause: err}
	}
	created, err := s.configs.CompareAndSwapGlobalConfig(ctx, installationID, key, seed, nil)
	if err != nil {
		return false, fmt.Errorf("persist plugin config: %w", err)
	}
	if !created {
		ready, err := s.globalConfigHasValue(ctx, installationID, key)
		if err != nil {
			return false, err
		}
		if !ready {
			return false, fmt.Errorf("plugin config %q was not saved", key)
		}
		return false, nil
	}
	return true, s.afterGlobalConfigSaved(ctx, installationID)
}
