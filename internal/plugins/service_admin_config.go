package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// ConfigValidationError indicates that the caller supplied an invalid key or
// a value that did not match the plugin's manifest global_config_schema.
// Callers (the admin HTTP handler) should map this to a 400-equivalent
// response.
type ConfigValidationError struct {
	Message string
	Cause   error
}

func (e *ConfigValidationError) Error() string { return e.Message }
func (e *ConfigValidationError) Unwrap() error { return e.Cause }

// SetGlobalConfig validates a single global-config entry against the plugin's
// manifest schema, persists it, and stops the running plugin so the next
// invocation rebinds with the new configuration. Used by the admin HTTP
// handler at PUT /api/admin/plugins/installations/{id}/config.
func (s *Service) SetGlobalConfig(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
) error {
	return s.SetGlobalConfigWithClears(ctx, installationID, key, value, nil)
}

// SetGlobalConfigWithClears saves one config entry while preserving redacted
// secret fields the browser left blank. A secret is removed only when its key
// appears in clearSecrets, making credential deletion an explicit action.
func (s *Service) SetGlobalConfigWithClears(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
	clearSecrets []string,
) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return &ConfigValidationError{Message: "config key is required"}
	}
	if s.configs == nil {
		return fmt.Errorf("plugin config store not configured")
	}

	_, manifest, err := s.ensureInstallationCache(ctx, installationID, false)
	if err != nil {
		return err
	}

	if value == nil {
		value = map[string]any{}
	}
	secretFields := GlobalConfigSecretFields(manifest, key)
	secretPaths := GlobalConfigSecretPaths(manifest, key)
	clearSet, err := validatedSecretClearSet(key, secretFields, clearSecrets)
	if err != nil {
		return &ConfigValidationError{Message: err.Error(), Cause: err}
	}
	const maxConfigSaveAttempts = 5
	saved := false
	for attempt := 0; attempt < maxConfigSaveAttempts; attempt++ {
		merged, expectedUpdatedAt, err := s.mergeStoredConfig(
			ctx,
			installationID,
			key,
			value,
			secretPaths,
		)
		if err != nil {
			return err
		}
		for field := range clearSet {
			delete(merged, field)
		}
		projection := globalConfigValidationProjection(manifest, key, merged, value)
		if err := ValidateGlobalConfigValue(manifest, key, projection); err != nil {
			return &ConfigValidationError{Message: err.Error(), Cause: err}
		}

		swapped, err := s.configs.CompareAndSwapGlobalConfig(
			ctx,
			installationID,
			key,
			merged,
			expectedUpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("persist plugin config: %w", err)
		}
		if swapped {
			saved = true
			break
		}
	}
	if !saved {
		return fmt.Errorf("persist plugin config: concurrent updates did not settle")
	}

	var stopErr error
	if s.host != nil {
		if err := s.host.Stop(installationID); err != nil && !errors.Is(err, pluginhost.ErrClientNotFound) {
			stopErr = fmt.Errorf("reload plugin after config update: %w", err)
		}
	}
	// Configuration is part of metadata match input identity. Notify hooks
	// after the old process has been stopped so resolver reloads observe the new
	// runtime. The durable config changed even when stopping failed, so hooks
	// still run before returning that error and parked rows are not left asleep.
	s.OnLifecycleChange(ctx)
	return stopErr
}

func validatedSecretClearSet(
	configKey string,
	secretFields []string,
	clearSecrets []string,
) (map[string]struct{}, error) {
	clearSet := secretFieldSet(clearSecrets)
	allowedSecrets := secretFieldSet(secretFields)
	for field := range clearSet {
		if _, ok := allowedSecrets[field]; !ok {
			return nil, fmt.Errorf("%s is not a secret field in config %s", field, configKey)
		}
	}
	return clearSet, nil
}

func (s *Service) preserveStoredSecrets(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
	secretPaths [][]string,
) (map[string]any, error) {
	merged, _, err := s.mergeStoredConfig(ctx, installationID, key, value, secretPaths)
	return merged, err
}

func (s *Service) mergeStoredConfig(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
	secretPaths [][]string,
) (map[string]any, *time.Time, error) {
	if s.configs == nil {
		if len(secretPaths) > 0 {
			return nil, nil, fmt.Errorf("plugin config store not configured")
		}
		merged := make(map[string]any, len(value))
		for field, incoming := range value {
			merged[field] = cloneConfigValue(incoming)
		}
		return merged, nil, nil
	}
	configs, err := s.configs.ListGlobalConfigs(ctx, installationID)
	if err != nil {
		return nil, nil, fmt.Errorf("load existing plugin config: %w", err)
	}
	var existing map[string]any
	var expectedUpdatedAt *time.Time
	for _, config := range configs {
		if config != nil && config.Key == key {
			existing = config.Value
			updatedAt := config.UpdatedAt
			expectedUpdatedAt = &updatedAt
			break
		}
	}
	merged := make(map[string]any, len(existing)+len(value))
	for field, saved := range existing {
		merged[field] = cloneConfigValue(saved)
	}
	for field, incoming := range value {
		merged[field] = cloneConfigValue(incoming)
	}
	if len(secretPaths) == 0 {
		return merged, expectedUpdatedAt, nil
	}
	pathsByField := make(map[string][][]string)
	for _, path := range secretPaths {
		if len(path) == 0 {
			continue
		}
		pathsByField[path[0]] = append(pathsByField[path[0]], path[1:])
	}
	for field, nestedSecretPaths := range pathsByField {
		incoming, present := value[field]
		saved, savedPresent := existing[field]
		if !present {
			continue
		}
		incomingObject, incomingIsObject := incoming.(map[string]any)
		savedObject, savedIsObject := saved.(map[string]any)
		if savedPresent && incomingIsObject && savedIsObject {
			merged[field] = mergeConfigObjects(savedObject, incomingObject, nestedSecretPaths)
			continue
		}
		incomingString, isString := incoming.(string)
		if !isString || strings.TrimSpace(incomingString) != "" {
			continue
		}
		if savedPresent {
			merged[field] = cloneConfigValue(saved)
		}
	}
	return merged, expectedUpdatedAt, nil
}

func globalConfigValidationProjection(
	manifest *pluginv1.PluginManifest,
	key string,
	merged map[string]any,
	submitted map[string]any,
) map[string]any {
	projection := make(map[string]any)
	schema := globalConfigSchema(manifest, key)
	if schema != nil {
		var document any
		if json.Unmarshal([]byte(schema.GetJsonSchema()), &document) == nil {
			projection = projectDeclaredConfigObject(
				merged,
				[]any{document},
				document,
			)
		}
	}
	overlaySubmittedConfig(projection, submitted, merged)
	return projection
}

func projectDeclaredConfigObject(
	value map[string]any,
	schemaNodes []any,
	root any,
) map[string]any {
	properties := make(map[string][]any)
	for _, node := range schemaNodes {
		collectTopLevelSchemaProperties(node, root, make(map[string]bool), properties)
	}
	projected := make(map[string]any, len(properties))
	for field, fieldSchemas := range properties {
		saved, present := value[field]
		if !present {
			continue
		}
		savedObject, isObject := saved.(map[string]any)
		if isObject {
			projected[field] = projectDeclaredConfigObject(savedObject, fieldSchemas, root)
			continue
		}
		projected[field] = cloneConfigValue(saved)
	}
	return projected
}

func overlaySubmittedConfig(
	projection map[string]any,
	submitted map[string]any,
	merged map[string]any,
) {
	for field, submittedValue := range submitted {
		mergedValue, present := merged[field]
		if !present {
			delete(projection, field)
			continue
		}
		submittedObject, submittedIsObject := submittedValue.(map[string]any)
		mergedObject, mergedIsObject := mergedValue.(map[string]any)
		if submittedIsObject && mergedIsObject {
			projectedObject, _ := projection[field].(map[string]any)
			if projectedObject == nil {
				projectedObject = make(map[string]any)
			}
			overlaySubmittedConfig(projectedObject, submittedObject, mergedObject)
			projection[field] = projectedObject
			continue
		}
		projection[field] = cloneConfigValue(mergedValue)
	}
}

func mergeConfigObjects(
	saved map[string]any,
	incoming map[string]any,
	secretPaths [][]string,
) map[string]any {
	merged := make(map[string]any, len(saved)+len(incoming))
	for key, value := range saved {
		merged[key] = cloneConfigValue(value)
	}
	for key, value := range incoming {
		nestedSecretPaths, wholeValueSecret := childSecretPaths(secretPaths, key)
		if text, isString := value.(string); wholeValueSecret &&
			isString && strings.TrimSpace(text) == "" {
			if savedValue, present := saved[key]; present {
				merged[key] = cloneConfigValue(savedValue)
				continue
			}
		}
		incomingObject, incomingIsObject := value.(map[string]any)
		savedObject, savedIsObject := saved[key].(map[string]any)
		if incomingIsObject && savedIsObject {
			if wholeValueSecret {
				nestedSecretPaths = append(nestedSecretPaths, nil)
			}
			merged[key] = mergeConfigObjects(savedObject, incomingObject, nestedSecretPaths)
			continue
		}
		merged[key] = cloneConfigValue(value)
	}
	return merged
}

func childSecretPaths(paths [][]string, key string) (nested [][]string, wholeValue bool) {
	for _, path := range paths {
		if len(path) == 0 {
			wholeValue = true
			continue
		}
		if path[0] != key {
			continue
		}
		if len(path) == 1 {
			wholeValue = true
			continue
		}
		nested = append(nested, path[1:])
	}
	return nested, wholeValue
}

func cloneConfigValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, entry := range typed {
			cloned[key] = cloneConfigValue(entry)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, entry := range typed {
			cloned[index] = cloneConfigValue(entry)
		}
		return cloned
	default:
		return value
	}
}
