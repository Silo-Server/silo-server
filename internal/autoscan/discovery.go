package autoscan

import (
	"context"
	"fmt"
)

// Built-in source identities are host-discovered scan-source entries that need
// no plugin installation. The ARR webhook identity backs webhook-mode sources:
// Sonarr/Radarr POST directly to Silo, the host parses the payload, and the
// plugin provider is never invoked for it.
const (
	BuiltinArrWebhookPluginID     = "silo.autoscan.arr-webhook"
	BuiltinArrWebhookCapabilityID = "arr-webhook"
	builtinArrWebhookDisplayName  = "Sonarr/Radarr Webhook"
)

// BuiltinArrWebhookSource returns the host-built-in ARR webhook source
// identity offered by the Add-source picker alongside installed plugin
// capabilities.
func BuiltinArrWebhookSource() DiscoveredSource {
	return DiscoveredSource{
		PluginID:     BuiltinArrWebhookPluginID,
		CapabilityID: BuiltinArrWebhookCapabilityID,
		DisplayName:  builtinArrWebhookDisplayName,
	}
}

// IsBuiltinArrWebhookIdentity reports whether (pluginID, capabilityID) is the
// built-in ARR webhook source identity.
func IsBuiltinArrWebhookIdentity(pluginID, capabilityID string) bool {
	return pluginID == BuiltinArrWebhookPluginID && capabilityID == BuiltinArrWebhookCapabilityID
}

// DiscoveredSource identifies one installed scan_source.v1 capability instance,
// enriched with the metadata the Add-source picker needs (plugin id + a
// human-friendly display name).
type DiscoveredSource struct {
	// PluginID is the installation's plugin id (e.g. "sonarr"); empty when the
	// lister cannot supply it.
	PluginID     string
	CapabilityID string
	// DisplayName is a human-friendly label for the capability (from the
	// capability's manifest display_name, falling back to plugin/capability ids).
	DisplayName string
}

// ScanSourceLister enumerates every installed scan_source.v1 capability so the
// engine can offer them in the Add-source picker.
type ScanSourceLister interface {
	// ListScanSources returns one entry per installed scan_source.v1 capability,
	// enriched with plugin id + display name.
	ListScanSources(ctx context.Context) ([]DiscoveredSource, error)
}

// WithBuiltinSources wraps a ScanSourceLister so host-built-in source
// identities appear beside installed plugin capabilities. inner may be nil
// (only the builtins are returned), matching the Service's nil-lister
// tolerance.
func WithBuiltinSources(inner ScanSourceLister, builtins ...DiscoveredSource) ScanSourceLister {
	return builtinSourceLister{inner: inner, builtins: builtins}
}

type builtinSourceLister struct {
	inner    ScanSourceLister
	builtins []DiscoveredSource
}

func (l builtinSourceLister) ListScanSources(ctx context.Context) ([]DiscoveredSource, error) {
	var out []DiscoveredSource
	if l.inner != nil {
		discovered, err := l.inner.ListScanSources(ctx)
		if err != nil {
			return nil, err
		}
		out = discovered
	}
	return append(out, l.builtins...), nil
}

// AvailableScanSource is one installed scan_source capability an operator can
// create a source against (the Add-source picker list).
type AvailableScanSource struct {
	PluginID     string `json:"plugin_id"`
	CapabilityID string `json:"capability_id"`
	DisplayName  string `json:"display_name"`
}

// ListAvailableScanSources enumerates every installed scan_source capability so
// an operator can pick one when creating a source. When no lister is configured
// it returns an empty list. The handler also uses this to validate that a
// create request targets a currently-installed capability.
func (s *Service) ListAvailableScanSources(ctx context.Context) ([]AvailableScanSource, error) {
	if s.lister == nil {
		return []AvailableScanSource{}, nil
	}
	discovered, err := s.lister.ListScanSources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list scan sources: %w", err)
	}
	out := make([]AvailableScanSource, 0, len(discovered))
	for _, d := range discovered {
		out = append(out, AvailableScanSource{
			PluginID:     d.PluginID,
			CapabilityID: d.CapabilityID,
			DisplayName:  d.DisplayName,
		})
	}
	return out, nil
}
