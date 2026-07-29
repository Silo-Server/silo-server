package handlers

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

func TestConfigValuesToJSONRedactsManifestSecrets(t *testing.T) {
	manifest := &pluginv1.PluginManifest{GlobalConfigSchema: []*pluginv1.ConfigSchema{{
		Key: "account",
		AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{
			{Key: "api_key", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_PASSWORD},
			{Key: "region", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT},
		}},
	}}}
	configs := []*plugins.RuntimeConfig{{
		Key: "account",
		Value: map[string]any{
			"api_key": "clawrouter-e2e-secret",
			"region":  "us-east",
		},
	}}

	result := configValuesToJSON(configs, manifest)
	if len(result) != 1 {
		t.Fatalf("result len = %d, want 1", len(result))
	}
	if _, leaked := result[0].Value["api_key"]; leaked {
		t.Fatalf("redacted response leaked api_key: %#v", result[0])
	}
	if result[0].Value["region"] != "us-east" {
		t.Fatalf("region = %#v", result[0].Value["region"])
	}
	if len(result[0].ConfiguredSecrets) != 1 || result[0].ConfiguredSecrets[0] != "api_key" {
		t.Fatalf("configured secrets = %#v", result[0].ConfiguredSecrets)
	}
}

func TestConfigValuesToJSONFailsClosedWithoutManifest(t *testing.T) {
	result := configValuesToJSON([]*plugins.RuntimeConfig{{
		Key:   "account",
		Value: map[string]any{"api_key": "clawrouter-e2e-secret"},
	}}, nil)
	if len(result) != 1 || len(result[0].Value) != 0 {
		t.Fatalf("result = %#v, want empty value", result)
	}
}

func TestConfigValuesToJSONFailsClosedWithoutMatchingSchema(t *testing.T) {
	manifest := &pluginv1.PluginManifest{GlobalConfigSchema: []*pluginv1.ConfigSchema{{
		Key: "replacement",
	}}}
	result := configValuesToJSON([]*plugins.RuntimeConfig{{
		Key:   "retired",
		Value: map[string]any{"api_key": "clawrouter-e2e-secret"},
	}}, manifest)
	if len(result) != 1 || len(result[0].Value) != 0 {
		t.Fatalf("result = %#v, want empty value for retired schema", result)
	}
}

func TestConfigValuesToJSONRedactsObjectsContainingNestedSecrets(t *testing.T) {
	for _, tc := range []struct {
		name       string
		jsonSchema string
	}{
		{
			name:       "inline nested schema",
			jsonSchema: `{"type":"object","properties":{"connection":{"type":"object","properties":{"api_key":{"type":"string","format":"password"},"endpoint":{"type":"string"}}},"region":{"type":"string"}}}`,
		},
		{
			name:       "local schema reference",
			jsonSchema: `{"type":"object","properties":{"connection":{"$ref":"#/$defs/connection"},"region":{"type":"string"}},"$defs":{"connection":{"type":"object","properties":{"api_key":{"type":"string","writeOnly":true}}}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := &pluginv1.PluginManifest{GlobalConfigSchema: []*pluginv1.ConfigSchema{{
				Key:        "account",
				JsonSchema: tc.jsonSchema,
			}}}
			result := configValuesToJSON([]*plugins.RuntimeConfig{{
				Key: "account",
				Value: map[string]any{
					"connection": map[string]any{
						"api_key":  "clawrouter-e2e-secret",
						"endpoint": "https://metadata.example.invalid",
					},
					"region": "us-east",
				},
			}}, manifest)

			if len(result) != 1 {
				t.Fatalf("result len = %d, want 1", len(result))
			}
			if _, leaked := result[0].Value["connection"]; leaked {
				t.Fatalf("response leaked nested secret container: %#v", result[0])
			}
			if result[0].Value["region"] != "us-east" {
				t.Fatalf("region = %#v, want us-east", result[0].Value["region"])
			}
			if len(result[0].ConfiguredSecrets) != 1 || result[0].ConfiguredSecrets[0] != "connection" {
				t.Fatalf("configured secrets = %#v, want connection", result[0].ConfiguredSecrets)
			}
		})
	}
}
