package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	sdkcapability "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// PluginProviderRegistryConfig wires the registry to the plugin stores.
type PluginProviderRegistryConfig struct {
	Bindings interface {
		ListAuthBindings(ctx context.Context) ([]*plugins.AuthBinding, error)
	}
	Installations interface {
		GetByID(ctx context.Context, id int) (*plugins.Installation, error)
		ListCapabilities(ctx context.Context, installationID int) ([]*plugins.Capability, error)
	}
	GlobalConfigs interface {
		ListGlobalConfigs(ctx context.Context, installationID int) ([]*plugins.RuntimeConfig, error)
	}
	// Manifests supplies the plugin's global config schema, whose
	// icon_url_path default is the button icon until the operator saves
	// that setting. Optional.
	Manifests interface {
		ManifestForInstallation(ctx context.Context, installationID int) (*pluginv1.PluginManifest, error)
	}
	Clients  PluginAuthClientResolver
	Sessions *SessionRepository
	Resolver *AccountResolver
}

// PluginProviderRegistry holds the sign-in providers built from enabled
// auth_provider.v1 bindings. Rebuild replaces the whole list at once; every
// node rebuilds when bindings, plugin configuration or installations change
// (announced on the admin event channel), so no restart is needed. A node
// that misses an announcement or fails a rebuild catches up through
// RunRebuilds.
type PluginProviderRegistry struct {
	cfg     PluginProviderRegistryConfig
	current atomic.Pointer[[]RegisteredProvider]
	mu      sync.Mutex
	// wake asks RunRebuilds for a rebuild; one pending request is enough.
	wake chan struct{}
}

// Provider registry resync timing for RunRebuilds.
const (
	// ProviderRegistryResyncInterval bounds how long a node that missed a
	// change announcement keeps a stale provider set, for example one that
	// still signs people in through a binding an administrator turned off.
	ProviderRegistryResyncInterval = time.Minute
	// ProviderRegistryRetryWait is the wait before retrying a failed rebuild.
	ProviderRegistryRetryWait = 10 * time.Second
)

// NewPluginProviderRegistry returns an empty registry; call Rebuild.
func NewPluginProviderRegistry(cfg PluginProviderRegistryConfig) *PluginProviderRegistry {
	r := &PluginProviderRegistry{cfg: cfg, wake: make(chan struct{}, 1)}
	empty := []RegisteredProvider{}
	r.current.Store(&empty)
	return r
}

// RequestRebuild asks RunRebuilds for a rebuild without waiting for it.
// Requests made while one is pending or running collapse into one more
// rebuild.
func (r *PluginProviderRegistry) RequestRebuild() {
	if r == nil {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// RunRebuilds rebuilds the provider set on every RequestRebuild and every
// interval until ctx ends, so a node that missed a change announcement, or
// whose rebuild failed, does not keep a stale set until a restart. A failed
// rebuild keeps the previous set (Rebuild) and is retried after retryWait.
func (r *PluginProviderRegistry) RunRebuilds(ctx context.Context, interval, retryWait time.Duration) {
	if r == nil {
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-timer.C:
		}
		next := interval
		if err := r.Rebuild(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.WarnContext(ctx, "rebuild sign-in providers failed; keeping the previous set and retrying", "component", "auth",
				"retry_in", retryWait, "error", err)
			next = retryWait
		}
		timer.Reset(next)
	}
}

// Providers returns the current list. Callers must not modify it.
func (r *PluginProviderRegistry) Providers() []RegisteredProvider {
	if r == nil {
		return nil
	}
	return *r.current.Load()
}

// Rebuild reads the bindings again and swaps the provider list. On error the
// previous list stays in place: a read that fails for any reason but a
// missing installation must not drop a provider or change its mode.
func (r *PluginProviderRegistry) Rebuild(ctx context.Context) error {
	if r == nil || r.cfg.Bindings == nil || r.cfg.Installations == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	bindings, err := r.cfg.Bindings.ListAuthBindings(ctx)
	if err != nil {
		return fmt.Errorf("list plugin auth bindings: %w", err)
	}
	providers := make([]RegisteredProvider, 0, len(bindings))
	for _, binding := range bindings {
		if binding == nil || !binding.Enabled {
			continue
		}
		installation, err := r.cfg.Installations.GetByID(ctx, binding.InstallationID)
		if errors.Is(err, plugins.ErrInstallationNotFound) {
			// A binding can outlive a half-removed installation for a moment;
			// leave it out rather than failing every other provider.
			slog.WarnContext(ctx, "skipping auth binding without installation", "component", "auth",
				"installation_id", binding.InstallationID, "error", err)
			continue
		}
		if err != nil {
			return fmt.Errorf("read plugin installation %d: %w", binding.InstallationID, err)
		}
		if !installation.Enabled {
			continue
		}
		provider, err := r.providerFor(ctx, binding)
		if err != nil {
			return err
		}
		providers = append(providers, provider)
	}
	// A network identity provider may run next to the one primary provider
	// (plugins.RuntimeConfigStore.UpsertAuthBinding).
	primary := 0
	for _, provider := range providers {
		if provider.Info.Mode != ProviderModeNetwork {
			primary++
		}
	}
	if primary > 1 {
		slog.WarnContext(ctx, "more than one external sign-in provider is enabled; turn all but one off",
			"component", "auth", "count", primary)
	}
	r.current.Store(&providers)
	return nil
}

// providerFor builds the provider of binding. It fails when the plugin's
// capabilities cannot be read: without them the mode is unknown, and
// guessing credentials would route passwords to an OAuth plugin.
func (r *PluginProviderRegistry) providerFor(ctx context.Context, binding *plugins.AuthBinding) (RegisteredProvider, error) {
	displayName := binding.CapabilityID
	mode := ProviderModeCredentials
	iconURL := ""
	matched := false
	capabilities, err := r.cfg.Installations.ListCapabilities(ctx, binding.InstallationID)
	if err != nil {
		return RegisteredProvider{}, fmt.Errorf("list capabilities of plugin installation %d: %w", binding.InstallationID, err)
	}
	for _, capability := range capabilities {
		if capability == nil || capability.Type != sdkcapability.AuthProvider || capability.ID != binding.CapabilityID {
			continue
		}
		matched = true
		if name, ok := capability.Metadata[configKeyDisplayName].(string); ok && strings.TrimSpace(name) != "" {
			displayName = name
		}
		mode = CapabilityProviderMode(capability.Metadata)
		if url, ok := capability.Metadata["icon_url"].(string); ok {
			iconURL = url
		}
		break
	}
	if !matched {
		return RegisteredProvider{}, fmt.Errorf("auth capability %q not found in plugin installation %d", binding.CapabilityID, binding.InstallationID)
	}
	// The operator names the button and its icon in global config
	// (display_name, icon_url_path); manifest values are the fallback. While
	// icon_url_path is unset or empty, its schema default is the icon, so the
	// plugin's own button icon shows on a fresh install and after the
	// operator clears the setting.
	iconPath := ""
	if r.cfg.GlobalConfigs != nil {
		if runtimeConfigs, err := r.cfg.GlobalConfigs.ListGlobalConfigs(ctx, binding.InstallationID); err == nil {
			for _, rc := range runtimeConfigs {
				switch rc.Key {
				case configKeyDisplayName:
					if v, ok := rc.Value[configValueField].(string); ok && strings.TrimSpace(v) != "" {
						displayName = v
					}
				case configKeyIconURLPath:
					if v, ok := rc.Value[configValueField].(string); ok && strings.TrimSpace(v) != "" {
						iconPath = v
					}
				}
			}
		}
	}
	if iconPath == "" {
		iconPath = r.defaultIconPath(ctx, binding.InstallationID)
	}
	if iconPath != "" {
		iconURL = pluginIconURL(binding.InstallationID, iconPath)
	}
	return RegisteredProvider{
		Info: LoginProviderInfo{
			ID:          PluginProviderID(binding.InstallationID, binding.CapabilityID),
			DisplayName: displayName,
			Mode:        mode,
			// The default is where a password login that names no provider
			// goes; a network provider takes no password.
			Default:        binding.DefaultLogin && mode != ProviderModeNetwork,
			IconURL:        iconURL,
			InstallationID: binding.InstallationID,
		},
		Provider: NewPluginProvider(
			PluginProviderConfig{
				InstallationID: binding.InstallationID,
				CapabilityID:   binding.CapabilityID,
				DisplayName:    displayName,
				AutoProvision:  binding.AutoProvision,
			},
			r.cfg.Sessions,
			r.cfg.Resolver,
			r.cfg.Clients,
		),
	}, nil
}

// Provider modes: a username and password form, an OAuth button, or a
// network identity button that signs in the overlay peer of the request
// (network_sign_in.go).
const (
	ProviderModeCredentials = "credentials"
	ProviderModeOAuth       = "oauth"
	ProviderModeNetwork     = "network"
)

// CapabilityProviderMode is the sign-in mode an auth_provider.v1
// capability's stored metadata declares (its auth_modes; see
// ProviderModeForAuthModes).
func CapabilityProviderMode(metadata map[string]any) string {
	var modes []string
	switch raw := metadata["auth_modes"].(type) {
	case []any:
		for _, m := range raw {
			if mode, ok := m.(string); ok {
				modes = append(modes, mode)
			}
		}
	case []string:
		modes = raw
	}
	return ProviderModeForAuthModes(modes)
}

// ProviderModeForAuthModes is the sign-in mode of a capability's auth_modes:
// "network" makes the provider a network identity button whatever else is
// listed, so a password never reaches it; ["oauth2"] alone makes it an OAuth
// button; password alone or alongside, or none, is a credentials form.
func ProviderModeForAuthModes(modes []string) string {
	hasPassword, hasOAuth := false, false
	for _, m := range modes {
		switch m {
		case manifest.AuthModeNetwork:
			return ProviderModeNetwork
		case manifest.AuthModePassword:
			hasPassword = true
		case manifest.AuthModeOAuth2:
			hasOAuth = true
		}
	}
	if hasOAuth && !hasPassword {
		return ProviderModeOAuth
	}
	return ProviderModeCredentials
}

// configKeyDisplayName names the login button, in capability metadata and
// in the plugin's global config.
const configKeyDisplayName = "display_name"

// configValueField is the member of a global config entry, and the field of
// its admin form, that holds the setting's value.
const configValueField = "value"

// configKeyIconURLPath is the global config holding the login button icon:
// a file under the plugin's assets route.
const configKeyIconURLPath = "icon_url_path"

// pluginIconURL is the login button icon at path under the plugin's assets.
// It is minted under the versioned plugin-content mount so the icon keeps
// resolving after the /api/v1 tombstone; the v2 auth-providers projection
// validates this shape.
func pluginIconURL(installationID int, path string) string {
	return fmt.Sprintf("%s/plugins/%d/assets/%s", plugins.ContentPrefix, installationID, strings.TrimLeft(strings.TrimSpace(path), "/"))
}

// defaultIconPath is the default of the plugin's icon_url_path setting, as
// its global config schema declares it: the JSON schema's value default,
// else the admin form's value field default. Empty when there is none or
// the manifest cannot be read.
func (r *PluginProviderRegistry) defaultIconPath(ctx context.Context, installationID int) string {
	if r.cfg.Manifests == nil {
		return ""
	}
	manifest, err := r.cfg.Manifests.ManifestForInstallation(ctx, installationID)
	if err != nil || manifest == nil {
		return ""
	}
	for _, schema := range manifest.GetGlobalConfigSchema() {
		if schema.GetKey() != configKeyIconURLPath {
			continue
		}
		var parsed struct {
			Properties struct {
				Value struct {
					Default any `json:"default"`
				} `json:"value"`
			} `json:"properties"`
		}
		if json.Unmarshal([]byte(schema.GetJsonSchema()), &parsed) == nil {
			if v, ok := parsed.Properties.Value.Default.(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		for _, field := range schema.GetAdminForm().GetFields() {
			if field.GetKey() == configValueField {
				if v := strings.TrimSpace(field.GetDefaultValue().GetStringValue()); v != "" {
					return v
				}
			}
		}
		return ""
	}
	return ""
}

// PluginProviderID is the sign-in provider id of one auth binding.
func PluginProviderID(installationID int, capabilityID string) string {
	return fmt.Sprintf("plugin:%d:%s", installationID, capabilityID)
}
