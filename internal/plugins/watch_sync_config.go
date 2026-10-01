package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5"
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

// InstalledFromSiloRepository reports whether an installation came from a
// Silo-managed plugin repository, rather than from a repository an admin added
// or from an uploaded archive.
func (s *Service) InstalledFromSiloRepository(ctx context.Context, installation *Installation) (bool, error) {
	if installation == nil || installation.RepositoryID == nil || s.repositories == nil {
		return false, nil
	}
	repository, err := s.repositories.GetByID(ctx, *installation.RepositoryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read plugin repository %d: %w", *installation.RepositoryID, err)
	}
	return repository.SourceKind == RepositorySourceSilo, nil
}

// SeedGlobalConfig saves value under key only when the installation has no
// saved value for key yet, keeping just the fields the manifest declares for
// that key. It reports whether it saved anything. Use it to carry settings
// over from elsewhere without overwriting what an admin entered.
func (s *Service) SeedGlobalConfig(ctx context.Context, installationID int, key string, value map[string]any) (bool, error) {
	if s.configs == nil {
		return false, nil
	}
	configs, err := s.configs.ListGlobalConfigs(ctx, installationID)
	if err != nil {
		return false, fmt.Errorf("list plugin config: %w", err)
	}
	for _, config := range configs {
		if config != nil && config.Key == key && len(config.Value) > 0 {
			return false, nil
		}
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
		return false, nil
	}
	if err := s.SetGlobalConfig(ctx, installationID, key, seed); err != nil {
		return false, err
	}
	return true, nil
}
