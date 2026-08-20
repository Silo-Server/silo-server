package plugins

import (
	"context"
	"encoding/json"
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
