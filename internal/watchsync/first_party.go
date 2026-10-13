package watchsync

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// The provider keys of Silo's former built-in providers.
const (
	traktProviderKey   = "trakt"
	simklProviderKey   = "simkl"
	mdblistProviderKey = "mdblist"
)

// firstPartyPlugin identifies a watch-sync capability of a first-party plugin.
type firstPartyPlugin struct {
	PluginID     string
	CapabilityID string
}

// firstPartyProviderKeys maps the first-party plugins that replaced Silo's
// former built-in providers to the provider keys those built-ins used.
// Connections, their encrypted tokens (whose AAD includes the key), history
// export records, and imported watch history are all stored under these keys,
// so registering the plugins under them carries every existing link over.
var firstPartyProviderKeys = map[firstPartyPlugin]string{
	{PluginID: "silo.watchprovider." + traktProviderKey, CapabilityID: traktProviderKey}:     traktProviderKey,
	{PluginID: "silo.watchprovider." + simklProviderKey, CapabilityID: simklProviderKey}:     simklProviderKey,
	{PluginID: "silo.watchprovider." + mdblistProviderKey, CapabilityID: mdblistProviderKey}: mdblistProviderKey,
}

// PluginProviderKey returns the registry key for a watch-sync plugin
// capability. A first-party plugin installed from a Silo-managed repository
// keeps its former built-in key. Every other plugin is keyed by installation,
// which a plugin can never claim for itself: a third-party plugin that reuses
// a first-party plugin ID must not receive the tokens of existing connections.
func PluginProviderKey(installationID int, pluginID, capabilityID string, siloManaged bool) string {
	if siloManaged {
		if key, ok := firstPartyProviderKeys[firstPartyPlugin{PluginID: pluginID, CapabilityID: capabilityID}]; ok {
			return key
		}
	}
	return fmt.Sprintf("%s:%d:%s", providerSourcePlugin, installationID, capabilityID)
}

// FirstPartyPluginID returns the first-party plugin that serves providerKey,
// for keys that a former built-in provider used.
func FirstPartyPluginID(providerKey string) (string, bool) {
	for plugin, key := range firstPartyProviderKeys {
		if key == providerKey {
			return plugin.PluginID, true
		}
	}
	return "", false
}

// The plugin global config entry, and its fields, where the first-party Trakt
// and Simkl plugins read their provider app credentials.
const (
	firstPartyAppConfigKey = "app"
	appClientIDField       = "client_id"
	appClientSecretField   = "client_secret"
)

// ServerSettingReader reads a decrypted server setting; a missing setting is
// empty.
type ServerSettingReader interface {
	GetServerSetting(ctx context.Context, key string) (string, error)
}

type pluginConfigSeeder interface {
	SeedGlobalConfig(ctx context.Context, installationID int, key string, value map[string]any) (bool, error)
}

// SeedFirstPartyAppConfig copies the app client ID and secret an admin saved
// for a former built-in provider (the watchsync.<key>.client_id and
// client_secret server settings) into the first-party plugin that now serves
// that key. Existing connections hold tokens issued to that app, so the plugin
// must use the same one. A plugin that already has app credentials keeps them.
// It runs until the plugin holds app credentials (or there were none to carry
// over), so app credentials an admin later clears from the plugin are not
// restored from the old settings.
func SeedFirstPartyAppConfig(
	ctx context.Context,
	providerKey string,
	installationID int,
	store FirstPartyMigrationStore,
	seeder pluginConfigSeeder,
) (bool, error) {
	if _, firstParty := FirstPartyPluginID(providerKey); !firstParty || store == nil || seeder == nil {
		return false, nil
	}
	doneKey := firstPartyAppSeededSettingKey(providerKey)
	done, err := store.GetServerSetting(ctx, doneKey)
	if err != nil || done != "" {
		return false, err
	}
	seeded := false
	clientID, err := store.GetServerSetting(ctx, "watchsync."+providerKey+"."+appClientIDField)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(clientID) != "" {
		clientSecret, err := store.GetServerSetting(ctx, "watchsync."+providerKey+"."+appClientSecretField)
		if err != nil {
			return false, err
		}
		seeded, err = seeder.SeedGlobalConfig(ctx, installationID, firstPartyAppConfigKey, map[string]any{
			appClientIDField:     clientID,
			appClientSecretField: clientSecret,
		})
		if err != nil {
			return false, err
		}
	}
	return seeded, store.SetServerSetting(ctx, doneKey, "done")
}

// firstPartyAppSeededSettingKey records that a provider's app credentials
// were carried over to its first-party plugin, or that there were none.
func firstPartyAppSeededSettingKey(providerKey string) string {
	return "watchsync.plugin_app_seeded." + providerKey
}

// FirstPartyMigrationStore holds what decides whether a former built-in
// provider's first-party plugin still needs installing.
type FirstPartyMigrationStore interface {
	ServerSettingReader
	SetServerSetting(ctx context.Context, key, value string) error
	HasConnections(ctx context.Context, providerKey string) (bool, error)
}

// firstPartyMigrationSettingKey records that a first-party plugin has served a
// former built-in provider's key, which completes that provider's migration.
func firstPartyMigrationSettingKey(providerKey string) string {
	return "watchsync.plugin_migration." + providerKey
}

// FirstPartyPluginsToInstall lists the first-party plugins to install so that
// connections made with a former built-in provider keep syncing. A plugin is
// listed only until it has served its key once (see MarkFirstPartyMigrated),
// so an admin who later uninstalls it does not get it back.
func FirstPartyPluginsToInstall(ctx context.Context, store FirstPartyMigrationStore) ([]string, error) {
	var pluginIDs []string
	for plugin, providerKey := range firstPartyProviderKeys {
		migrated, err := store.GetServerSetting(ctx, firstPartyMigrationSettingKey(providerKey))
		if err != nil {
			return nil, err
		}
		if migrated != "" {
			continue
		}
		connected, err := store.HasConnections(ctx, providerKey)
		if err != nil {
			return nil, err
		}
		if connected {
			pluginIDs = append(pluginIDs, plugin.PluginID)
		}
	}
	sort.Strings(pluginIDs)
	return pluginIDs, nil
}

// MarkFirstPartyMigrated records that a first-party plugin now serves a
// former built-in provider's key. Other keys are ignored.
func MarkFirstPartyMigrated(ctx context.Context, store FirstPartyMigrationStore, providerKey string) error {
	if _, firstParty := FirstPartyPluginID(providerKey); !firstParty || store == nil {
		return nil
	}
	key := firstPartyMigrationSettingKey(providerKey)
	marked, err := store.GetServerSetting(ctx, key)
	if err != nil || marked != "" {
		return err
	}
	return store.SetServerSetting(ctx, key, "plugin")
}

// AppClientID returns the provider app client ID that the plugin serving
// providerKey is configured with, read from its "app" global config. Other
// Silo features that call the same provider with a profile's token (Trakt
// collections) must send the client ID the token was issued to. It is empty
// when no plugin serves the key or the plugin has none configured.
func (s *Service) AppClientID(ctx context.Context, providerKey string) (string, error) {
	provider, ok := s.registry.Get(providerKey)
	if !ok {
		return "", nil
	}
	plugin, ok := provider.(*PluginProvider)
	if !ok {
		return "", nil
	}
	config, err := plugin.providerConfig(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(config.GetValues()[firstPartyAppConfigKey+"."+appClientIDField]), nil
}
