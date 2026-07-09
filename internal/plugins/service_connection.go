package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

var ErrConnectionTestUnsupported = errors.New("plugin connection test unsupported")

type ConnectionTestError struct {
	Message string
	Cause   error
}

func (e *ConnectionTestError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *ConnectionTestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

var runPluginConnectionCheck = func(
	ctx context.Context,
	client pluginClient,
	manifest *pluginv1.PluginManifest,
) error {
	capabilityID, err := metadataProviderConnectionCheckCapabilityID(manifest)
	if err != nil {
		return err
	}
	capability := metadataProviderConnectionCheckCapability(manifest, capabilityID)
	if !metadataProviderSupportsConnectionProbe(capability, "movie") {
		slog.DebugContext(ctx,
			"skipping metadata provider connection check for unsupported probe type", "component", "plugins",
			"plugin_id", manifest.GetPluginId(),
			"capability_id", capabilityID,
			"item_type", "movie",
		)
		return nil
	}

	metadataClient, err := client.MetadataProvider(capabilityID)
	if err != nil {
		return &ConnectionTestError{
			Message: fmt.Sprintf("Failed to initialize the metadata provider: %v", err),
			Cause:   err,
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if _, err := metadataClient.Search(probeCtx, &pluginv1.SearchMetadataRequest{
		Query:    "The Matrix",
		ItemType: "movie",
		Year:     1999,
		Language: "en",
	}); err != nil {
		return &ConnectionTestError{
			Message: fmt.Sprintf("Connection check failed: %v", err),
			Cause:   err,
		}
	}

	return nil
}

func (s *Service) TestGlobalConfig(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
) error {
	if strings.TrimSpace(key) == "" {
		return &ConnectionTestError{Message: "Config key is required"}
	}
	if s.host == nil {
		return fmt.Errorf("plugin host not configured")
	}

	installation, manifest, err := s.ensureInstallationCache(ctx, installationID, false)
	if err != nil {
		return err
	}

	if value == nil {
		value = map[string]any{}
	}
	if err := ValidateGlobalConfigValue(manifest, key, value); err != nil {
		return &ConnectionTestError{
			Message: err.Error(),
			Cause:   err,
		}
	}
	if _, err := metadataProviderConnectionCheckCapabilityID(manifest); err != nil {
		return err
	}

	configEntries, err := s.mergedGlobalConfigEntries(ctx, installationID, key, value)
	if err != nil {
		return err
	}

	testInstallationID := -int(s.testConfigSeq.Add(1))
	client, err := s.host.Start(ctx, pluginhost.StartRequest{
		InstallationID: testInstallationID,
		BinaryPath:     installation.InstallPath,
		Manifest:       manifest,
		Config:         configEntries,
	})
	if err != nil {
		return &ConnectionTestError{
			Message: fmt.Sprintf("Failed to start the plugin with the test configuration: %v", err),
			Cause:   err,
		}
	}

	defer func() {
		if stopErr := s.host.Stop(testInstallationID); stopErr != nil && !errors.Is(stopErr, pluginhost.ErrClientNotFound) {
			slog.WarnContext(ctx,
				"stopping temporary plugin connection check instance failed", "component", "plugins",
				"installation_id", installationID,
				"test_installation_id", testInstallationID,
				"error", stopErr,
			)
		}
	}()

	return runPluginConnectionCheck(ctx, client, manifest)
}

func (s *Service) mergedGlobalConfigEntries(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
) ([]*pluginv1.ConfigEntry, error) {
	configsByKey := make(map[string]map[string]any)

	if s.configs != nil {
		configs, err := s.configs.ListGlobalConfigs(ctx, installationID)
		if err != nil {
			return nil, fmt.Errorf("list plugin runtime configs for installation %d: %w", installationID, err)
		}
		for _, config := range configs {
			if config == nil {
				continue
			}
			configsByKey[config.Key] = cloneConfigMap(config.Value)
		}
	}

	configsByKey[key] = cloneConfigMap(value)
	return configEntriesFromValues(configsByKey, installationID)
}

func configEntriesFromValues(
	configsByKey map[string]map[string]any,
	installationID int,
) ([]*pluginv1.ConfigEntry, error) {
	if len(configsByKey) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(configsByKey))
	for key := range configsByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	entries := make([]*pluginv1.ConfigEntry, 0, len(keys))
	for _, key := range keys {
		value := configsByKey[key]
		if value == nil {
			value = map[string]any{}
		}

		structValue, err := structpb.NewStruct(value)
		if err != nil {
			return nil, fmt.Errorf(
				"encode runtime config %q for installation %d: %w",
				key,
				installationID,
				err,
			)
		}

		entries = append(entries, &pluginv1.ConfigEntry{
			Key:   key,
			Value: structValue,
		})
	}

	return entries, nil
}

func cloneConfigMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(value))
	for key, entry := range value {
		cloned[key] = entry
	}
	return cloned
}

func metadataProviderConnectionCheckCapabilityID(manifest *pluginv1.PluginManifest) (string, error) {
	for _, capability := range manifest.GetCapabilities() {
		if capability.GetType() != "metadata_provider.v1" {
			continue
		}
		return capability.GetId(), nil
	}
	return "", &ConnectionTestError{
		Message: "Connection checks are not supported for this plugin yet.",
		Cause:   ErrConnectionTestUnsupported,
	}
}

func metadataProviderConnectionCheckCapability(
	manifest *pluginv1.PluginManifest,
	capabilityID string,
) *pluginv1.CapabilityDescriptor {
	for _, capability := range manifest.GetCapabilities() {
		if capability.GetType() == "metadata_provider.v1" && capability.GetId() == capabilityID {
			return capability
		}
	}
	return nil
}

func metadataProviderSupportsConnectionProbe(
	capability *pluginv1.CapabilityDescriptor,
	contentType string,
) bool {
	priorities, ok := metadataProviderDefaultPriorities(capability)
	if !ok {
		return true
	}
	return priorities[contentType] > 0
}

func metadataProviderDefaultPriorities(
	capability *pluginv1.CapabilityDescriptor,
) (map[string]float64, bool) {
	if capability == nil || capability.GetMetadata() == nil {
		return nil, false
	}

	metadataMap := capability.GetMetadata().AsMap()
	raw, ok := metadataMap["default_priority"]
	if !ok {
		nested, nestedOK := metadataMap["metadata"].(map[string]any)
		if !nestedOK {
			return nil, false
		}
		raw, ok = nested["default_priority"]
		if !ok {
			return nil, false
		}
	}

	rawMap, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	priorities := make(map[string]float64, len(rawMap))
	for key, value := range rawMap {
		switch v := value.(type) {
		case float64:
			priorities[key] = v
		case int:
			priorities[key] = float64(v)
		case int32:
			priorities[key] = float64(v)
		case int64:
			priorities[key] = float64(v)
		}
	}
	return priorities, len(priorities) > 0
}
