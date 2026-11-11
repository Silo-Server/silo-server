package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

var ErrConnectionTestUnsupported = errors.New("plugin connection test unsupported")

// Capability types a connection check can probe.
const (
	metadataProviderCapability = "metadata_provider.v1"
	markerProviderCapability   = "marker_provider.v1"
)

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

func runPluginConnectionCheck(
	ctx context.Context,
	client pluginClient,
	manifest *pluginv1.PluginManifest,
) error {
	capability, err := connectionCheckCapability(manifest)
	if err != nil {
		return err
	}
	capabilityID := capability.GetId()
	if capability.GetType() == markerProviderCapability {
		return runMarkerProviderConnectionCheck(ctx, client, capabilityID)
	}
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
	return s.TestGlobalConfigWithClears(ctx, installationID, key, value, nil)
}

// TestGlobalConfigWithClears tests the exact prospective configuration,
// including explicit removals of saved secrets. This keeps a successful probe
// from describing credentials the operator has already staged for deletion.
func (s *Service) TestGlobalConfigWithClears(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
	clearSecrets []string,
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

	value, err = s.prepareStagedGlobalConfig(ctx, installationID, manifest, key, value, clearSecrets, func(err error) error {
		return &ConnectionTestError{Message: err.Error(), Cause: err}
	})
	if err != nil {
		return err
	}
	if _, err := connectionCheckCapability(manifest); err != nil {
		return err
	}

	configEntries, err := s.mergedGlobalConfigEntries(ctx, installationID, key, value)
	if err != nil {
		return err
	}

	binaryPath, err := s.localInstallPath(installation)
	if err != nil {
		return err
	}
	testInstallationID := -int(s.testConfigSeq.Add(1))
	client, err := s.host.Start(ctx, pluginhost.StartRequest{
		InstallationID: testInstallationID,
		BinaryPath:     binaryPath,
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

// prepareStagedGlobalConfig applies one staged global config entry the way
// a save would, without saving it: blank secret fields keep the stored
// secret, clearSecrets drops stored ones, emptied declared fields are
// cleared, and the result must validate.
//
// A stored secret is reused only while the rest of the entry matches what
// was saved with it and no secret is typed. Once the staged entry changes
// any setting (a server URL, issuer, CA or anything else), only the secrets
// the admin typed and the fields the schema declares reach the plugin, so a
// test cannot send a saved credential to a destination it was never saved
// for (issue #2796).
// invalid wraps a rejected entry in the caller's error type.
func (s *Service) prepareStagedGlobalConfig(
	ctx context.Context,
	installationID int,
	manifest *pluginv1.PluginManifest,
	key string,
	value map[string]any,
	clearSecrets []string,
	invalid func(error) error,
) (map[string]any, error) {
	if value == nil {
		value = map[string]any{}
	}
	clearSet, err := validatedSecretClearSet(key, GlobalConfigSecretFields(manifest, key), clearSecrets)
	if err != nil {
		return nil, invalid(err)
	}
	secretPaths := GlobalConfigSecretPaths(manifest, key)
	merged, stored, err := s.preserveStoredSecrets(ctx, installationID, key, value, secretPaths)
	if err != nil {
		return nil, err
	}
	removeGlobalConfigClears(manifest, key, merged, value, clearSet)
	if len(secretPaths) > 0 && stagedConfigChangesSettings(manifest, key, merged, stored, value, secretPaths) {
		keepOnlySubmittedSecrets(merged, value, secretPaths, clearSet)
		dropStoredUndeclaredFields(manifest, key, merged, value)
	}
	if err := validateMergedGlobalConfig(manifest, key, merged, value); err != nil {
		return nil, invalid(err)
	}
	return merged, nil
}

// stagedConfigChangesSettings reports whether a staged entry differs from the
// stored one in anything a stored secret could be sent with. Both sides are
// compared as JSON values without their secret values, and a blank or null
// top-level field counts as absent. A field the stored entry lacks matches a
// staged field holding its declared default, which the admin form fills in;
// removing a stored value is still a change. A secret the admin typed is a
// change too: it can be a destination itself (a proxy or webhook URL), and
// comparing it with the stored value would let a caller guess at secrets.
func stagedConfigChangesSettings(
	manifest *pluginv1.PluginManifest,
	key string,
	staged map[string]any,
	stored map[string]any,
	submitted map[string]any,
	secretPaths [][]string,
) bool {
	for _, path := range secretPaths {
		typed, present := configPathValue(submitted, path)
		if !present || isClearedConfigValue(typed) {
			continue
		}
		switch typed.(type) {
		case map[string]any, []any:
			// A secret object or list is compared whole below.
		default:
			return true
		}
	}
	stagedSettings, stagedOK := settingsForComparison(staged, secretPaths)
	storedSettings, storedOK := settingsForComparison(stored, secretPaths)
	if !stagedOK || !storedOK {
		// An entry that cannot be compared counts as changed.
		return true
	}
	for field, fallback := range globalConfigFieldDefaults(manifest, key) {
		if _, inStored := storedSettings[field]; !inStored && reflect.DeepEqual(stagedSettings[field], fallback) {
			delete(stagedSettings, field)
		}
	}
	return !reflect.DeepEqual(stagedSettings, storedSettings)
}

func settingsForComparison(value map[string]any, secretPaths [][]string) (map[string]any, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, false
	}
	if normalized == nil {
		normalized = map[string]any{}
	}
	for _, path := range secretPaths {
		// A secret path can name a whole object or list (a map of servers
		// with secrets inside, or a shape the path walker cannot address).
		// Its other values may be destinations, so it is compared whole.
		secret, _ := configPathValue(normalized, path)
		switch secret.(type) {
		case map[string]any, []any:
			continue
		}
		deleteConfigPath(normalized, path)
	}
	for field, fieldValue := range normalized {
		if isClearedConfigValue(fieldValue) {
			delete(normalized, field)
		}
	}
	return normalized, true
}

// dropStoredUndeclaredFields removes stored top-level fields the schema does
// not declare and the admin did not submit. Plugin-written state and fields
// left over from an older manifest can hold credentials the admin API never
// shows, so they do not go to changed settings either.
func dropStoredUndeclaredFields(
	manifest *pluginv1.PluginManifest,
	key string,
	merged map[string]any,
	submitted map[string]any,
) {
	public, secrets := GlobalConfigFieldSets(manifest, key)
	declared := secretFieldSet(append(append([]string(nil), public...), secrets...))
	for field := range merged {
		if _, isDeclared := declared[field]; isDeclared {
			continue
		}
		if _, sent := submitted[field]; sent {
			continue
		}
		delete(merged, field)
	}
}

// globalConfigFieldDefaults returns the scalar defaults the admin form shows
// for top-level fields: an admin form field's default_value, else the JSON
// schema property's default. Values are JSON-normalized.
func globalConfigFieldDefaults(manifest *pluginv1.PluginManifest, key string) map[string]any {
	defaults := map[string]any{}
	schema := globalConfigSchema(manifest, key)
	if schema == nil {
		return defaults
	}
	var document any
	if json.Unmarshal([]byte(schema.GetJsonSchema()), &document) == nil {
		properties := make(map[string][]any)
		collectTopLevelSchemaProperties(document, document, make(map[string]bool), properties)
		for field, candidates := range properties {
			for _, candidate := range candidates {
				property, _ := candidate.(map[string]any)
				switch fallback := property["default"].(type) {
				case string, float64, bool:
					defaults[field] = fallback
				}
			}
		}
	}
	for _, field := range schema.GetAdminForm().GetFields() {
		fieldKey := strings.TrimSpace(field.GetKey())
		if fieldKey == "" || field.GetDefaultValue() == nil {
			continue
		}
		switch fallback := field.GetDefaultValue().AsInterface().(type) {
		case string, float64, bool:
			defaults[fieldKey] = fallback
		}
	}
	for field, fallback := range defaults {
		if isClearedConfigValue(fallback) {
			delete(defaults, field)
		}
	}
	return defaults
}

// keepOnlySubmittedSecrets replaces every secret in merged with the value the
// admin submitted at the same path, removing it when the submission left it
// blank or out, or named its field in clearSet. Stored secrets merged in by a
// blank field are dropped.
func keepOnlySubmittedSecrets(
	merged map[string]any,
	submitted map[string]any,
	secretPaths [][]string,
	clearSet map[string]struct{},
) {
	for _, path := range secretPaths {
		_, cleared := clearSet[path[0]]
		typed, present := configPathValue(submitted, path)
		if cleared || !present || isClearedConfigValue(typed) {
			deleteConfigPath(merged, path)
			continue
		}
		setConfigPath(merged, path, cloneConfigValue(typed))
	}
}

func configPathValue(value map[string]any, path []string) (any, bool) {
	if len(path) == 0 {
		return nil, false
	}
	current := value
	for _, segment := range path[:len(path)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	found, ok := current[path[len(path)-1]]
	return found, ok
}

func deleteConfigPath(value map[string]any, path []string) {
	if len(path) == 0 {
		return
	}
	current := value
	for _, segment := range path[:len(path)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	delete(current, path[len(path)-1])
}

// setConfigPath sets a secret at path. The submitted value had objects along
// the path, and merged copies them, so the parents exist.
func setConfigPath(value map[string]any, path []string, secret any) {
	current := value
	for _, segment := range path[:len(path)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	current[path[len(path)-1]] = secret
}

// storedGlobalConfigs returns copies of an installation's saved global
// config entries by key.
func (s *Service) storedGlobalConfigs(ctx context.Context, installationID int) (map[string]map[string]any, error) {
	configsByKey := make(map[string]map[string]any)
	if s.configs == nil {
		return configsByKey, nil
	}
	configs, err := s.configs.ListGlobalConfigs(ctx, installationID)
	if err != nil {
		return nil, fmt.Errorf("list plugin runtime configs for installation %d: %w", installationID, err)
	}
	for _, config := range configs {
		if config != nil {
			configsByKey[config.Key] = cloneConfigMap(config.Value)
		}
	}
	return configsByKey, nil
}

func (s *Service) mergedGlobalConfigEntries(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
) ([]*pluginv1.ConfigEntry, error) {
	configsByKey, err := s.storedGlobalConfigs(ctx, installationID)
	if err != nil {
		return nil, err
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

// connectionCheckCapability picks the capability a connection check probes: the
// plugin's first metadata provider, or else its first marker provider.
func connectionCheckCapability(manifest *pluginv1.PluginManifest) (*pluginv1.CapabilityDescriptor, error) {
	for _, capabilityType := range []string{metadataProviderCapability, markerProviderCapability} {
		for _, capability := range manifest.GetCapabilities() {
			if capability.GetType() == capabilityType {
				return capability, nil
			}
		}
	}
	return nil, &ConnectionTestError{
		Message: connectionCheckUnsupportedMessage,
		Cause:   ErrConnectionTestUnsupported,
	}
}

const connectionCheckUnsupportedMessage = "Connection checks are not supported for this plugin yet."

// runMarkerProviderConnectionCheck asks a marker provider for its account
// statistics, which need the provider's credentials, so a success shows that
// the staged settings authenticate. A provider without statistics has no
// check to run.
func runMarkerProviderConnectionCheck(ctx context.Context, client pluginClient, capabilityID string) error {
	markerClient, err := client.MarkerProvider(capabilityID)
	if err != nil {
		return &ConnectionTestError{
			Message: fmt.Sprintf("Failed to initialize the marker provider: %v", err),
			Cause:   err,
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if _, err := markerClient.GetMarkerProviderStats(probeCtx, &pluginv1.GetMarkerProviderStatsRequest{}); err != nil {
		if status.Code(err) == codes.Unimplemented {
			return &ConnectionTestError{Message: connectionCheckUnsupportedMessage, Cause: ErrConnectionTestUnsupported}
		}
		return &ConnectionTestError{
			Message: "Connection check failed: " + status.Convert(err).Message(),
			Cause:   err,
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
