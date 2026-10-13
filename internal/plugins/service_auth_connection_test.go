package plugins

import (
	"errors"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestAuthConnectionRejectsDuplicateStagedKeys(t *testing.T) {
	pluginManifest := connectionTestManifest(t, "silo.auth.oidc", "0.1.0")
	pluginManifest.Capabilities = []*pluginv1.CapabilityDescriptor{{
		Type: "auth_provider.v1", Id: "oidc", AuthModes: []string{"oauth2"},
		Metadata: &structpb.Struct{Fields: map[string]*structpb.Value{"connection_test": structpb.NewBoolValue(true)}},
	}}
	service := &Service{
		installations: newFakeServiceInstallationStore(&Installation{
			ID: 7, PluginID: pluginManifest.PluginId, Version: pluginManifest.Version,
			InstallPath: writeInstalledPluginManifest(t, pluginManifest), Enabled: true,
		}),
		configs: &fakeServiceConfigStore{},
	}
	_, err := service.TestAuthProviderConnection(t.Context(), 7, "oidc", []StagedConfig{
		{Key: "connection", Value: map[string]any{"api_key": "first-draft"}},
		{Key: " connection ", Value: map[string]any{"api_key": "second-draft"}},
	})
	validation, ok := errors.AsType[*ConfigValidationError](err)
	if !ok || !strings.Contains(validation.Message, "duplicate config key") {
		t.Fatalf("duplicate draft reached the plugin instead of validation: %v", err)
	}
}
