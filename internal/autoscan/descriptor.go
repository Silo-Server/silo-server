package autoscan

import "strings"

// ConnectionRequirement says whether a scan source needs upstream credentials
// to reach its provider. It drives whether the Add-source flow shows the
// connection step at all, replacing the UI's per-plugin-id special cases.
type ConnectionRequirement string

const (
	// ConnectionNone means the source reaches its provider without host-held
	// credentials — a local filesystem watcher, or a provider that pushes to a
	// Silo webhook endpoint.
	ConnectionNone ConnectionRequirement = "none"
	// ConnectionOptional means the source can bind a connection but works
	// without one. This is the default for a capability that declares nothing,
	// because it is what every source behaved as before descriptors existed.
	ConnectionOptional ConnectionRequirement = "optional"
	// ConnectionRequired means the source cannot poll until a connection is
	// bound.
	ConnectionRequired ConnectionRequirement = "required"
)

// normalizeConnectionRequirement maps a manifest-supplied string onto a known
// requirement, falling back to ConnectionOptional for absent or unrecognized
// values so an unknown future value never hides the connection step outright.
func normalizeConnectionRequirement(value string) ConnectionRequirement {
	switch ConnectionRequirement(strings.ToLower(strings.TrimSpace(value))) {
	case ConnectionNone:
		return ConnectionNone
	case ConnectionRequired:
		return ConnectionRequired
	default:
		return ConnectionOptional
	}
}

// ScanSourceDescriptor is the host-facing setup contract for one scan_source
// capability: everything the Add-source flow needs to build its steps without
// launching the plugin. It mirrors the precedent set by the SDK's typed
// WatchSyncProviderDescriptor, and is read from the capability's manifest
// metadata (see DescriptorFromMetadata).
//
// Every field is optional in a manifest. A capability that declares nothing
// resolves to DefaultScanSourceDescriptor, which reproduces the pre-descriptor
// behavior exactly, so existing installs are unaffected.
type ScanSourceDescriptor struct {
	// DeliveryModes lists how changes reach the host (DeliveryModePoll and/or
	// DeliveryModeWebhook). A single entry lets the flow skip its delivery step.
	DeliveryModes []string `json:"delivery_modes"`
	// Connection says whether upstream credentials are needed.
	Connection ConnectionRequirement `json:"connection"`
	// ConnectionKinds restricts which stored connections may be bound (e.g.
	// "sonarr", "radarr"). Empty means any connection is offerable.
	ConnectionKinds []string `json:"connection_kinds"`
	// EmitsNativePaths reports that the plugin already returns Silo-native
	// paths, so the host can skip prompting for path rewrites.
	EmitsNativePaths bool `json:"emits_native_paths"`
	// Summary is a short operator-facing sentence for the picker card.
	Summary string `json:"summary"`
	// IconURL optionally points at a plugin-served icon, same convention as an
	// auth provider's icon_url.
	IconURL string `json:"icon_url"`
	// ConfigForm describes the per-source configuration fields the operator
	// fills in when creating the source. It carries the capability's own
	// admin_form so plugin-specific knobs render generically instead of being
	// hardcoded in the admin UI.
	ConfigForm *AdminForm `json:"config_form,omitempty"`
}

// SupportsDeliveryMode reports whether the descriptor allows the given mode.
func (d ScanSourceDescriptor) SupportsDeliveryMode(mode string) bool {
	for _, m := range d.DeliveryModes {
		if m == mode {
			return true
		}
	}
	return false
}

// DefaultDeliveryMode returns the mode a new source should use when the
// operator is not asked to choose. Poll wins when available because it is the
// historical default; a webhook-only source resolves to webhook.
func (d ScanSourceDescriptor) DefaultDeliveryMode() string {
	if d.SupportsDeliveryMode(DeliveryModePoll) {
		return DeliveryModePoll
	}
	if len(d.DeliveryModes) > 0 {
		return d.DeliveryModes[0]
	}
	return DeliveryModePoll
}

// DefaultScanSourceDescriptor is what a capability that declares no scan-source
// metadata resolves to: host-polled, connection allowed but not demanded, and
// path rewrites still offered. This is exactly how every source behaved before
// descriptors existed, which is what keeps already-installed plugins working.
func DefaultScanSourceDescriptor() ScanSourceDescriptor {
	return ScanSourceDescriptor{
		DeliveryModes: []string{DeliveryModePoll},
		Connection:    ConnectionOptional,
	}
}

// DescriptorFromMetadata builds a descriptor from a capability's manifest
// metadata map, filling anything absent from DefaultScanSourceDescriptor.
//
// The metadata arrives as decoded JSON (map[string]any), so every read is
// tolerant: a wrong type is treated as absent rather than failing the whole
// capability, because a malformed descriptor must not make an otherwise working
// plugin undiscoverable.
func DescriptorFromMetadata(metadata map[string]any) ScanSourceDescriptor {
	out := DefaultScanSourceDescriptor()
	if metadata == nil {
		return out
	}

	// The config form is read first and independently of the typed block: a
	// capability may describe its per-source fields through config_schema
	// without declaring any scan-source behavior at all.
	out.ConfigForm = configFormFromMetadata(metadata)

	raw, ok := metadata["scan_source"].(map[string]any)
	if !ok {
		return out
	}

	if modes := stringSlice(raw["delivery_modes"]); len(modes) > 0 {
		valid := make([]string, 0, len(modes))
		for _, m := range modes {
			switch m := strings.ToLower(strings.TrimSpace(m)); m {
			case DeliveryModePoll:
				valid = append(valid, m)
			case DeliveryModeWebhook:
				// Deliberately dropped for plugin capabilities. Webhook delivery
				// is only accepted for the host's built-in ARR identity (see
				// resolveDeliveryMode in the autoscan handler), which supplies
				// its descriptor directly rather than through this parser.
				// Honouring a plugin's claim here would surface a setup option
				// whose every submission ends in HTTP 400.
			}
		}
		// Only adopt the declared list when at least one mode is understood;
		// otherwise keep the default so the source stays creatable.
		if len(valid) > 0 {
			out.DeliveryModes = valid
		}
	}

	if connection, ok := raw["connection"].(string); ok {
		out.Connection = normalizeConnectionRequirement(connection)
	}
	if kinds := stringSlice(raw["connection_kinds"]); len(kinds) > 0 {
		out.ConnectionKinds = kinds
	}
	if native, ok := raw["emits_native_paths"].(bool); ok {
		out.EmitsNativePaths = native
	}
	if summary, ok := raw["summary"].(string); ok {
		out.Summary = strings.TrimSpace(summary)
	}
	if icon, ok := raw["icon_url"].(string); ok {
		out.IconURL = strings.TrimSpace(icon)
	}

	return out
}

// scanSourceConfigKey is the config_schema key a scan_source capability uses to
// describe its per-source fields. A capability's config_schema may hold several
// entries; only this one describes the Add-source form, so the rest (plugin-wide
// settings) keep rendering on the Plugins page as they do today.
const scanSourceConfigKey = "scan_source"

// configFormFromMetadata finds the per-source admin form in a capability's
// metadata. It prefers an explicit form nested under the typed scan_source
// block, then falls back to the capability's config_schema entry keyed
// "scan_source".
func configFormFromMetadata(metadata map[string]any) *AdminForm {
	if raw, ok := metadata["scan_source"].(map[string]any); ok {
		if form := adminFormFromMetadata(raw["config_form"]); form != nil {
			return form
		}
	}

	entries, ok := metadata["config_schema"].([]any)
	if !ok {
		return nil
	}
	for _, entry := range entries {
		schema, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if key, _ := schema["key"].(string); key != scanSourceConfigKey {
			continue
		}
		return adminFormFromMetadata(schema["admin_form"])
	}
	return nil
}

// stringSlice coerces a decoded-JSON value into a trimmed, non-empty string
// slice. Both []any (fresh JSON) and []string (in-process construction) are
// accepted; anything else yields nil.
func stringSlice(value any) []string {
	var raw []string
	switch typed := value.(type) {
	case []string:
		raw = typed
	case []any:
		for _, entry := range typed {
			text, ok := entry.(string)
			if !ok {
				return nil
			}
			raw = append(raw, text)
		}
	default:
		return nil
	}

	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
