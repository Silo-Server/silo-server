package plugins

import (
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestProvisionedReplacementProtectsNestedCredentialDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	id := seedResidentQueryInstallation(t, pool, true)
	cipher := instanceStateTestCipher(t)
	configs := NewRuntimeConfigStore(pool, cipher)
	const credential = "synthetic-credential-value"
	if err := configs.PutGlobalConfig(ctx, id, "app", map[string]any{
		"account": map[string]any{"token": credential},
	}); err != nil {
		t.Fatal(err)
	}
	before := &pluginv1.PluginManifest{GlobalConfigSchema: []*pluginv1.ConfigSchema{{Key: "app",
		JsonSchema: `{"type":"object","properties":{"account":{"type":"object","properties":{"token":{"type":"string","writeOnly":true}}}}}`}}}
	writer := &ProvisionWriter{configs: configs}
	for _, tc := range []struct {
		name, schema string
		submitted    map[string]any
		rejected     bool
	}{
		{name: "drops whole entry", rejected: true},
		{name: "another nested credential",
			schema:   `{"type":"object","properties":{"account":{"type":"object","properties":{"new_token":{"writeOnly":true}}}}}`,
			rejected: true},
		{name: "moves credential into public property",
			schema:    `{"type":"object","properties":{"account":{"type":"object","properties":{"label":{"type":"string"}}}}}`,
			submitted: map[string]any{"account": map[string]any{"label": credential}},
			rejected:  true},
		{name: "new safe value in public property",
			schema:    `{"type":"object","properties":{"account":{"type":"object","properties":{"label":{"type":"string"}}}}}`,
			submitted: map[string]any{"account": map[string]any{"label": "new label"}}},
		{name: "parent stays protected", schema: `{"type":"object","properties":{"account":{"type":"object","writeOnly":true}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := &pluginv1.PluginManifest{}
			if tc.schema != "" {
				candidate.GlobalConfigSchema = []*pluginv1.ConfigSchema{{Key: "app", JsonSchema: tc.schema}}
			}
			item := preparedProvision{pluginID: "silo.test", pkg: &PreparedProvisionPackage{
				Manifest: candidate, previousManifest: before, existing: &Installation{ID: id}, stage: &provisionPackageStage{},
			}}
			if tc.submitted != nil {
				item.config = map[string]map[string]any{"app": tc.submitted}
			}
			_, err := writer.planConfigValues(ctx, pool, item, id)
			if tc.rejected {
				if err == nil || !strings.Contains(err.Error(), "account.token") || strings.Contains(err.Error(), credential) {
					t.Fatalf("wanted credential refusal naming only its path, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("compatible replacement refused: %v", err)
			}
		})
	}
}
