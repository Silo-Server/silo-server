package plugins

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestWatchSyncProviderConfigClassifiesManifestFields(t *testing.T) {
	manifest := &pluginv1.PluginManifest{GlobalConfigSchema: []*pluginv1.ConfigSchema{{
		Key: "provider",
		AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{
			{Key: "base_url", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT},
			{Key: "client_secret", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_PASSWORD, Secret: true},
		}},
	}}}
	config, err := watchSyncProviderConfig(manifest, []*RuntimeConfig{nil, {
		Key: " provider ",
		Value: map[string]any{
			" base_url ":    "https://floppy.example",
			"client_secret": "secret",
			"undeclared":    map[string]any{"token": "also-secret"},
			"   ":           "ignored-empty-field",
		},
	}, {
		Key: "   ",
		Value: map[string]any{
			"base_url": "ignored-empty-key",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if config.GetValues()["provider.base_url"] != "https://floppy.example" ||
		config.GetSecretValues()["provider.client_secret"] != "secret" ||
		config.GetSecretValues()["provider.undeclared"] != `{"token":"also-secret"}` {
		t.Fatalf("config = %#v", config)
	}
	if _, exposed := config.GetValues()["provider.undeclared"]; exposed {
		t.Fatal("undeclared plugin config was exposed as public")
	}
	if _, present := config.GetSecretValues()["."]; present {
		t.Fatal("empty plugin config key or field was emitted")
	}
}
