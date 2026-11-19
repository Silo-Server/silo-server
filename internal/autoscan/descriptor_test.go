package autoscan

import (
	"testing"
)

// Webhook delivery is accepted only for the host's built-in ARR identity (see
// resolveDeliveryMode). Honouring a plugin's claim would offer a setup option
// whose every submission is rejected with HTTP 400.
func TestDescriptorFromMetadataIgnoresPluginDeclaredWebhook(t *testing.T) {
	got := DescriptorFromMetadata(map[string]any{
		"scan_source": map[string]any{"delivery_modes": []any{"webhook"}},
	})
	if got.SupportsDeliveryMode(DeliveryModeWebhook) {
		t.Errorf("delivery modes = %v, plugin-declared webhook must be dropped", got.DeliveryModes)
	}
	if !got.SupportsDeliveryMode(DeliveryModePoll) {
		t.Errorf("delivery modes = %v, want fallback to poll", got.DeliveryModes)
	}
}

// Secret-marked fields are dropped: source_config is plaintext JSONB, so a
// masked input would misrepresent how the value is stored.
func TestConfigFormDropsSecretFields(t *testing.T) {
	got := DescriptorFromMetadata(map[string]any{
		"config_schema": []any{
			map[string]any{"key": "scan_source", "admin_form": map[string]any{
				"fields": []any{
					map[string]any{"key": "root", "label": "Root", "control": "TEXT"},
					map[string]any{"key": "token", "label": "Token", "control": "PASSWORD"},
					map[string]any{"key": "other", "label": "Other", "control": "TEXT", "secret": true},
				},
			}},
		},
	})
	if got.ConfigForm == nil {
		t.Fatal("expected the non-secret field to survive")
	}
	for _, field := range got.ConfigForm.Fields {
		if field.Key != "root" {
			t.Errorf("secret-bearing field %q must not be collected", field.Key)
		}
	}
}

// Dynamic-option fields have no option source on a scan-source form, so a
// required one could never be satisfied. They are dropped at the contract.
func TestConfigFormDropsDynamicOptionFields(t *testing.T) {
	got := DescriptorFromMetadata(map[string]any{
		"metadata": map[string]any{
			"scan_source": map[string]any{
				"config_form": map[string]any{
					"fields": []any{
						map[string]any{"key": "root", "label": "Root", "control": "TEXT"},
						map[string]any{
							"key": "profile", "label": "Profile", "control": "SELECT",
							"dynamic_options": true,
						},
					},
				},
			},
		},
	})

	if got.ConfigForm == nil {
		t.Fatal("expected the static field to survive")
	}
	for _, field := range got.ConfigForm.Fields {
		if field.Key == "profile" {
			t.Error("dynamic-option field must not be offered on a source form")
		}
	}
}

// A plugin explicitly declaring a value that equals a host default must still
// win over the compatibility descriptor — otherwise "manifest wins" is false
// for exactly the values a plugin is most likely to state.
func TestCompatibilityRespectsExplicitDefaultValuedDeclaration(t *testing.T) {
	declared := DescriptorFromMetadata(map[string]any{
		"metadata": map[string]any{
			"scan_source": map[string]any{"connection": "optional"},
		},
	})
	got := ApplyCompatibilityDescriptor(cephFSPluginID, cephFSCapabilityID, declared)

	if got.Connection != ConnectionOptional {
		t.Errorf("connection = %q, explicit declaration was overwritten by compat", got.Connection)
	}
}
