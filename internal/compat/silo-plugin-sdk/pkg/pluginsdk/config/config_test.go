package config_test

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/config"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

func TestValidateManifestGlobalValue(t *testing.T) {
	manifest := &pluginv1.PluginManifest{
		PluginId: "example.plugin",
		Version:  "1.0.0",
		GlobalConfigSchema: []*pluginv1.ConfigSchema{
			{
				Key:        "connection",
				Title:      "Connection",
				JsonSchema: `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}`,
			},
		},
	}

	t.Run("accepts valid payload", func(t *testing.T) {
		if err := config.ValidateManifestGlobalValue(manifest, "connection", map[string]any{
			"url": "https://api.example.com",
		}); err != nil {
			t.Fatalf("ValidateManifestGlobalValue() returned error: %v", err)
		}
	})

	t.Run("rejects undeclared keys", func(t *testing.T) {
		if err := config.ValidateManifestGlobalValue(manifest, "missing", map[string]any{}); err == nil {
			t.Fatal("expected undeclared config key to be rejected")
		}
	})

	t.Run("rejects invalid payload", func(t *testing.T) {
		if err := config.ValidateManifestGlobalValue(manifest, "connection", map[string]any{
			"url": 42,
		}); err == nil {
			t.Fatal("expected invalid config payload to be rejected")
		}
	})
}

func TestValidateManifestUserValue(t *testing.T) {
	manifest := &pluginv1.PluginManifest{
		UserConfigSchema: []*pluginv1.ConfigSchema{
			{
				Key:        "preferences",
				Title:      "Preferences",
				JsonSchema: `{"type":"object","properties":{"theme":{"type":"string"}},"additionalProperties":false}`,
			},
		},
	}

	if err := config.ValidateManifestUserValue(manifest, "preferences", map[string]any{
		"theme": "midnight",
	}); err != nil {
		t.Fatalf("ValidateManifestUserValue() returned error: %v", err)
	}
}

func TestValidateAdminForm(t *testing.T) {
	manifest := &pluginv1.PluginManifest{
		PluginId: "example.plugin",
		Version:  "1.0.0",
		GlobalConfigSchema: []*pluginv1.ConfigSchema{
			{
				Key:        "connection",
				Title:      "Connection",
				JsonSchema: `{"type":"object","properties":{"api_key":{"type":"string"},"pin":{"type":"string"},"enabled":{"type":"boolean"}},"required":["api_key"],"additionalProperties":false}`,
				AdminForm: &pluginv1.AdminFormDescriptor{
					Fields: []*pluginv1.AdminFormField{
						{
							Key:      "api_key",
							Label:    "API Key",
							Control:  pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_PASSWORD,
							Required: true,
						},
						{
							Key:     "enabled",
							Label:   "Enabled",
							Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SWITCH,
						},
					},
				},
			},
		},
	}

	if err := publicmanifest.Validate(manifest); err != nil {
		t.Fatalf("ValidateManifest() returned error: %v", err)
	}
}

func TestValidateManifestRejectsInvalidAdminForm(t *testing.T) {
	manifest := &pluginv1.PluginManifest{
		GlobalConfigSchema: []*pluginv1.ConfigSchema{
			{
				Key:        "connection",
				Title:      "Connection",
				JsonSchema: `{"type":"object","properties":{"api_key":{"type":"string"}},"required":["api_key"],"additionalProperties":false}`,
				AdminForm: &pluginv1.AdminFormDescriptor{
					Fields: []*pluginv1.AdminFormField{
						{
							Key:     "missing",
							Label:   "Missing",
							Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT,
						},
					},
				},
			},
		},
	}

	if err := publicmanifest.Validate(manifest); err == nil {
		t.Fatal("expected invalid admin form field to be rejected")
	}
}
