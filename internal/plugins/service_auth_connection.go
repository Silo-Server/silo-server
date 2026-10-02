package plugins

import (
	"context"
	"errors"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrAuthConnectionTestUnsupported reports an auth plugin without a
// connection test: its capability does not declare connection_test, or the
// plugin answers Unimplemented (built before SDK v0.22.0).
var ErrAuthConnectionTestUnsupported = errors.New("auth provider has no connection test")

// StagedConfig is one global configuration entry as the operator has it on
// screen, not yet saved. Blank secret fields fall back to the stored secret;
// ClearSecrets drops stored secrets from the test.
type StagedConfig struct {
	Key          string
	Value        map[string]any
	ClearSecrets []string
}

// AuthConnectionTestStep is one check the plugin ran, in order.
type AuthConnectionTestStep struct {
	ID      string
	Label   string
	OK      bool
	Message string
}

// AuthConnectionTestResult is the plugin's verdict on the staged settings.
type AuthConnectionTestResult struct {
	OK    bool
	Steps []AuthConnectionTestStep
	// AuthModes are the tested capability's sign-in modes, so a caller can
	// tell an OAuth provider (which has a redirect URI) from a password one.
	AuthModes []string
}

// TestAuthProviderConnection sends the staged configuration, merged over the
// stored entries it does not replace, to the running plugin's
// AuthProviderChecks.TestConnection. The plugin tests it and persists
// nothing, so no temporary instance is started and nothing is saved.
func (s *Service) TestAuthProviderConnection(ctx context.Context, installationID int, capabilityID string, staged []StagedConfig) (AuthConnectionTestResult, error) {
	_, pluginManifest, err := s.loadForManifestRead(ctx, installationID, true)
	if err != nil {
		return AuthConnectionTestResult{}, err
	}
	descriptor := authProviderCapability(pluginManifest, capabilityID)
	if descriptor == nil {
		return AuthConnectionTestResult{}, &ConfigValidationError{Message: "the plugin has no auth provider capability " + strings.TrimSpace(capabilityID)}
	}
	if !manifest.AuthProviderSupportsConnectionTest(descriptor) {
		return AuthConnectionTestResult{}, ErrAuthConnectionTestUnsupported
	}

	configsByKey, err := s.storedGlobalConfigs(ctx, installationID)
	if err != nil {
		return AuthConnectionTestResult{}, err
	}
	seenKeys := make(map[string]bool, len(staged))
	for _, entry := range staged {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			return AuthConnectionTestResult{}, &ConfigValidationError{Message: "config key is required"}
		}
		if seenKeys[key] {
			return AuthConnectionTestResult{}, &ConfigValidationError{Message: "duplicate config key: " + key}
		}
		seenKeys[key] = true
		merged, err := s.prepareStagedGlobalConfig(ctx, installationID, pluginManifest, key, entry.Value, entry.ClearSecrets, func(err error) error {
			return &ConfigValidationError{Message: err.Error(), Cause: err}
		})
		if err != nil {
			return AuthConnectionTestResult{}, err
		}
		configsByKey[key] = merged
	}
	entries, err := configEntriesFromValues(configsByKey, installationID)
	if err != nil {
		return AuthConnectionTestResult{}, err
	}

	client, err := s.ensureClient(ctx, installationID)
	if err != nil {
		return AuthConnectionTestResult{}, err
	}
	authClient, err := client.AuthProvider(descriptor.GetId())
	if err != nil {
		return AuthConnectionTestResult{}, err
	}
	response, err := authClient.TestConnection(ctx, &pluginv1.AuthTestConnectionRequest{Config: entries})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unimplemented {
			return AuthConnectionTestResult{}, ErrAuthConnectionTestUnsupported
		}
		// A plugin that cannot answer is a failed test the operator should
		// see, not a server error.
		return AuthConnectionTestResult{Steps: []AuthConnectionTestStep{{
			ID: "plugin", Label: "Plugin reachable", Message: status.Convert(err).Message(),
		}}, AuthModes: descriptor.GetAuthModes()}, nil
	}
	result := AuthConnectionTestResult{OK: response.GetOk(), Steps: make([]AuthConnectionTestStep, 0, len(response.GetSteps())), AuthModes: descriptor.GetAuthModes()}
	allPassed := len(response.GetSteps()) > 0
	for _, step := range response.GetSteps() {
		result.Steps = append(result.Steps, AuthConnectionTestStep{ID: step.GetId(), Label: step.GetLabel(), OK: step.GetOk(), Message: step.GetMessage()})
		allPassed = allPassed && step.GetOk()
	}
	// ok is trusted only when every reported step passed.
	result.OK = result.OK && allPassed
	return result, nil
}

// authProviderCapability finds the auth_provider.v1 capability id, or the
// first one when id is empty.
func authProviderCapability(pluginManifest *pluginv1.PluginManifest, id string) *pluginv1.CapabilityDescriptor {
	id = strings.TrimSpace(id)
	for _, capability := range pluginManifest.GetCapabilities() {
		if capability.GetType() != "auth_provider.v1" {
			continue
		}
		if id == "" || capability.GetId() == id {
			return capability
		}
	}
	return nil
}
