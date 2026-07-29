package autoscan

import "testing"

// A capability that declares nothing must behave exactly as sources did before
// descriptors existed: host-polled, connection allowed but not demanded. This
// is the guarantee that keeps already-installed plugins working.
func TestDescriptorFromMetadataDefaultsPreserveLegacyBehavior(t *testing.T) {
	for name, metadata := range map[string]map[string]any{
		"nil metadata":   nil,
		"empty metadata": {},
		"unrelated keys": {"display_name": "Something"},
	} {
		t.Run(name, func(t *testing.T) {
			got := DescriptorFromMetadata(metadata)
			if got.Connection != ConnectionOptional {
				t.Errorf("connection = %q, want %q", got.Connection, ConnectionOptional)
			}
			if !got.SupportsDeliveryMode(DeliveryModePoll) {
				t.Errorf("delivery modes = %v, want poll", got.DeliveryModes)
			}
			if got.SupportsDeliveryMode(DeliveryModeWebhook) {
				t.Errorf("delivery modes = %v, must not default to webhook", got.DeliveryModes)
			}
			if got.EmitsNativePaths {
				t.Error("emits_native_paths must default false so rewrites stay offered")
			}
		})
	}
}

func TestDescriptorFromMetadataReadsDeclaredContract(t *testing.T) {
	got := DescriptorFromMetadata(map[string]any{
		"scan_source": map[string]any{
			"delivery_modes":     []any{"webhook"},
			"connection":         "none",
			"connection_kinds":   []any{"sonarr", "radarr"},
			"emits_native_paths": true,
			"summary":            "  Pushes to Silo.  ",
			"icon_url":           "/assets/icon.svg",
		},
	})

	if got.Connection != ConnectionNone {
		t.Errorf("connection = %q, want none", got.Connection)
	}
	if got.SupportsDeliveryMode(DeliveryModePoll) {
		t.Errorf("delivery modes = %v, want webhook only", got.DeliveryModes)
	}
	if !got.EmitsNativePaths {
		t.Error("emits_native_paths not read")
	}
	if got.Summary != "Pushes to Silo." {
		t.Errorf("summary = %q, want trimmed", got.Summary)
	}
	if len(got.ConnectionKinds) != 2 {
		t.Errorf("connection kinds = %v", got.ConnectionKinds)
	}
}

// A malformed or unrecognized descriptor must degrade to defaults rather than
// make an otherwise working plugin undiscoverable.
func TestDescriptorFromMetadataToleratesBadValues(t *testing.T) {
	tests := map[string]map[string]any{
		"wrong block type": {"scan_source": "not-an-object"},
		"unknown modes": {"scan_source": map[string]any{
			"delivery_modes": []any{"telepathy"},
		}},
		"wrong mode element type": {"scan_source": map[string]any{
			"delivery_modes": []any{7},
		}},
		"unknown connection value": {"scan_source": map[string]any{
			"connection": "sometimes",
		}},
	}

	for name, metadata := range tests {
		t.Run(name, func(t *testing.T) {
			got := DescriptorFromMetadata(metadata)
			if !got.SupportsDeliveryMode(DeliveryModePoll) {
				t.Errorf("delivery modes = %v, want fallback to poll", got.DeliveryModes)
			}
			if got.Connection != ConnectionOptional {
				t.Errorf("connection = %q, want fallback to optional", got.Connection)
			}
		})
	}
}

func TestDescriptorReadsConfigFormFromCapabilityConfigSchema(t *testing.T) {
	got := DescriptorFromMetadata(map[string]any{
		"config_schema": []any{
			map[string]any{"key": "unrelated", "admin_form": map[string]any{
				"fields": []any{map[string]any{"key": "ignored", "label": "Ignored"}},
			}},
			map[string]any{"key": "scan_source", "admin_form": map[string]any{
				"fields": []any{
					map[string]any{"key": "root", "label": "Root path", "control": "TEXT"},
				},
			}},
		},
	})

	if got.ConfigForm == nil {
		t.Fatal("expected config form from the scan_source config_schema entry")
	}
	if len(got.ConfigForm.Fields) != 1 || got.ConfigForm.Fields[0].Key != "root" {
		t.Fatalf("wrong form picked: %+v", got.ConfigForm.Fields)
	}
}

func TestDefaultDeliveryModePrefersPoll(t *testing.T) {
	webhookOnly := ScanSourceDescriptor{DeliveryModes: []string{DeliveryModeWebhook}}
	if got := webhookOnly.DefaultDeliveryMode(); got != DeliveryModeWebhook {
		t.Errorf("webhook-only default = %q", got)
	}

	both := ScanSourceDescriptor{DeliveryModes: []string{DeliveryModeWebhook, DeliveryModePoll}}
	if got := both.DefaultDeliveryMode(); got != DeliveryModePoll {
		t.Errorf("dual-mode default = %q, want poll", got)
	}

	// An empty descriptor must still name a usable mode.
	if got := (ScanSourceDescriptor{}).DefaultDeliveryMode(); got != DeliveryModePoll {
		t.Errorf("empty default = %q, want poll", got)
	}
}

// The builtin arr webhook identity is the host's own descriptor, and the UI
// relies on it to skip both the delivery and connection steps.
func TestBuiltinArrWebhookDescriptor(t *testing.T) {
	d := BuiltinArrWebhookSource().Descriptor

	if d.Connection != ConnectionNone {
		t.Errorf("connection = %q, want none (the arr pushes to Silo)", d.Connection)
	}
	if d.SupportsDeliveryMode(DeliveryModePoll) {
		t.Errorf("delivery modes = %v, want webhook only", d.DeliveryModes)
	}
	if d.ConfigForm == nil || len(d.ConfigForm.Fields) == 0 {
		t.Fatal("expected a provider config form")
	}
	if d.ConfigForm.Fields[0].Key != WebhookProviderConfigKey {
		t.Errorf("form field = %q, want %q", d.ConfigForm.Fields[0].Key, WebhookProviderConfigKey)
	}
}

// The compatibility layer is a stopgap for first-party plugins that have not
// published a descriptor yet. A plugin that declares its own contract must win.
func TestApplyCompatibilityDescriptorFillsOnlyUnsetFields(t *testing.T) {
	declared := DescriptorFromMetadata(nil) // plugin said nothing
	got := ApplyCompatibilityDescriptor(cephFSPluginID, cephFSCapabilityID, declared)

	if got.Connection != ConnectionNone {
		t.Errorf("connection = %q, want none from compat", got.Connection)
	}
	if got.ConfigForm == nil {
		t.Fatal("expected compat config form for cephfs")
	}
	if got.Summary == "" {
		t.Error("expected compat summary")
	}
}

func TestApplyCompatibilityDescriptorDoesNotOverrideManifest(t *testing.T) {
	declared := DescriptorFromMetadata(map[string]any{
		"scan_source": map[string]any{
			"connection": "required",
			"summary":    "Plugin's own words",
		},
	})
	got := ApplyCompatibilityDescriptor(cephFSPluginID, cephFSCapabilityID, declared)

	if got.Connection != ConnectionRequired {
		t.Errorf("connection = %q, manifest must win over compat", got.Connection)
	}
	if got.Summary != "Plugin's own words" {
		t.Errorf("summary = %q, manifest must win over compat", got.Summary)
	}
}

func TestApplyCompatibilityDescriptorLeavesUnknownPluginsAlone(t *testing.T) {
	declared := DescriptorFromMetadata(nil)
	got := ApplyCompatibilityDescriptor("com.example.future", "watcher", declared)

	if got.ConfigForm != nil {
		t.Error("unknown plugin must not inherit another plugin's form")
	}
	if got.Connection != ConnectionOptional {
		t.Errorf("connection = %q, want untouched default", got.Connection)
	}
}
