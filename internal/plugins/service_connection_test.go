package plugins

import (
	"context"
	"errors"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

type fakeServiceConfigStore struct {
	configsByInstallation map[int][]*RuntimeConfig
	puts                  []putGlobalConfigCall
	putErr                error
}

type putGlobalConfigCall struct {
	installationID int
	key            string
	value          map[string]any
}

func (f *fakeServiceConfigStore) ListGlobalConfigs(
	_ context.Context,
	installationID int,
) ([]*RuntimeConfig, error) {
	configs := f.configsByInstallation[installationID]
	result := make([]*RuntimeConfig, 0, len(configs))
	for _, config := range configs {
		if config == nil {
			continue
		}
		cloned := *config
		cloned.Value = cloneConfigMap(config.Value)
		result = append(result, &cloned)
	}
	return result, nil
}

func (f *fakeServiceConfigStore) PutGlobalConfig(
	_ context.Context,
	installationID int,
	key string,
	value map[string]any,
) error {
	f.puts = append(f.puts, putGlobalConfigCall{
		installationID: installationID,
		key:            key,
		value:          cloneConfigMap(value),
	})
	return f.putErr
}

func TestServiceTestGlobalConfigUsesMergedDraftAndStopsTemporaryInstance(t *testing.T) {
	originalProbe := runPluginConnectionCheck
	t.Cleanup(func() {
		runPluginConnectionCheck = originalProbe
	})

	probeCalls := 0
	runPluginConnectionCheck = func(
		_ context.Context,
		client pluginClient,
		manifest *pluginv1.PluginManifest,
	) error {
		probeCalls++
		if client == nil {
			t.Fatal("probe client = nil, want started client")
		}
		if manifest.GetPluginId() != "silo.metadb" {
			t.Fatalf("manifest plugin id = %q, want silo.metadb", manifest.GetPluginId())
		}
		return nil
	}

	manifest := connectionTestManifest(t, "silo.metadb", "0.0.36")
	installPath := writeInstalledPluginManifest(t, manifest)
	host := &fakeServiceHost{
		startResult: &fakePluginClient{manifest: manifest},
	}
	service := &Service{
		installations: newFakeServiceInstallationStore(&Installation{
			ID:          42,
			PluginID:    manifest.GetPluginId(),
			Version:     manifest.GetVersion(),
			InstallPath: installPath,
			Enabled:     false,
		}),
		configs: &fakeServiceConfigStore{
			configsByInstallation: map[int][]*RuntimeConfig{
				42: {
					{
						InstallationID: 42,
						Key:            "connection",
						Value: map[string]any{
							"api_key": "persisted",
						},
					},
					{
						InstallationID: 42,
						Key:            "secondary",
						Value: map[string]any{
							"enabled": true,
						},
					},
				},
			},
		},
		host: host,
	}

	if err := service.TestGlobalConfig(context.Background(), 42, "connection", map[string]any{
		"api_key": "draft",
	}); err != nil {
		t.Fatalf("TestGlobalConfig() returned error: %v", err)
	}

	if probeCalls != 1 {
		t.Fatalf("probe calls = %d, want 1", probeCalls)
	}
	if len(host.started) != 1 {
		t.Fatalf("start calls = %d, want 1", len(host.started))
	}
	if len(host.stopped) != 1 {
		t.Fatalf("stop calls = %d, want 1", len(host.stopped))
	}

	startReq := host.started[0]
	if startReq.InstallationID >= 0 {
		t.Fatalf("temporary installation id = %d, want negative id", startReq.InstallationID)
	}
	if host.stopped[0] != startReq.InstallationID {
		t.Fatalf("stopped installation id = %d, want %d", host.stopped[0], startReq.InstallationID)
	}
	if len(startReq.Config) != 2 {
		t.Fatalf("config entries = %d, want 2", len(startReq.Config))
	}

	valuesByKey := make(map[string]map[string]any, len(startReq.Config))
	for _, entry := range startReq.Config {
		valuesByKey[entry.GetKey()] = entry.GetValue().AsMap()
	}
	if got := valuesByKey["connection"]["api_key"]; got != "draft" {
		t.Fatalf("connection api_key = %#v, want draft", got)
	}
	if got := valuesByKey["secondary"]["enabled"]; got != true {
		t.Fatalf("secondary enabled = %#v, want true", got)
	}
}

func TestRunPluginConnectionCheckSkipsMovieProbeForAudiobookOnlyProvider(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{
		"default_priority": map[string]any{
			"audiobook": 2,
		},
	})
	if err != nil {
		t.Fatalf("NewStruct() error = %v", err)
	}

	manifest := connectionTestManifest(t, "silo.audiobook-metadata", "0.1.2")
	manifest.Capabilities = []*pluginv1.CapabilityDescriptor{
		{
			Type:        "metadata_provider.v1",
			Id:          "audiobook-metadata",
			DisplayName: "Audiobook Metadata",
			Metadata:    metadata,
		},
	}
	client := &fakePluginClient{manifest: manifest}

	if err := runPluginConnectionCheck(context.Background(), client, manifest); err != nil {
		t.Fatalf("runPluginConnectionCheck() error = %v", err)
	}
	if client.metadataProviderCalls != 0 {
		t.Fatalf("metadata provider calls = %d, want 0", client.metadataProviderCalls)
	}
}

func TestServiceTestGlobalConfigReturnsUnsupportedWithoutStartingPlugin(t *testing.T) {
	manifest := connectionTestManifest(t, "silo.simple", "0.0.1")
	manifest.Capabilities = []*pluginv1.CapabilityDescriptor{
		{
			Type:        "scheduled_task.v1",
			Id:          "refresh",
			DisplayName: "Refresh",
		},
	}
	installPath := writeInstalledPluginManifest(t, manifest)
	host := &fakeServiceHost{}
	service := &Service{
		installations: newFakeServiceInstallationStore(&Installation{
			ID:          7,
			PluginID:    manifest.GetPluginId(),
			Version:     manifest.GetVersion(),
			InstallPath: installPath,
			Enabled:     true,
		}),
		host: host,
	}

	err := service.TestGlobalConfig(context.Background(), 7, "connection", map[string]any{
		"api_key": "draft",
	})
	if err == nil {
		t.Fatal("TestGlobalConfig() returned nil error, want unsupported error")
	}

	var connectionErr *ConnectionTestError
	if !errors.As(err, &connectionErr) {
		t.Fatalf("error = %v, want ConnectionTestError", err)
	}
	if !strings.Contains(connectionErr.Error(), "not supported") {
		t.Fatalf("connection error message = %q, want unsupported message", connectionErr.Error())
	}
	if len(host.started) != 0 {
		t.Fatalf("start calls = %d, want 0", len(host.started))
	}
	if len(host.stopped) != 0 {
		t.Fatalf("stop calls = %d, want 0", len(host.stopped))
	}
}

func TestServiceTestGlobalConfigStopsTemporaryInstanceOnProbeFailure(t *testing.T) {
	originalProbe := runPluginConnectionCheck
	t.Cleanup(func() {
		runPluginConnectionCheck = originalProbe
	})

	runPluginConnectionCheck = func(
		_ context.Context,
		_ pluginClient,
		_ *pluginv1.PluginManifest,
	) error {
		return &ConnectionTestError{Message: "probe failed"}
	}

	manifest := connectionTestManifest(t, "silo.metadb", "0.0.36")
	installPath := writeInstalledPluginManifest(t, manifest)
	host := &fakeServiceHost{
		startResult: &fakePluginClient{manifest: manifest},
	}
	service := &Service{
		installations: newFakeServiceInstallationStore(&Installation{
			ID:          19,
			PluginID:    manifest.GetPluginId(),
			Version:     manifest.GetVersion(),
			InstallPath: installPath,
			Enabled:     true,
		}),
		host: host,
	}

	err := service.TestGlobalConfig(context.Background(), 19, "connection", map[string]any{
		"api_key": "draft",
	})
	if err == nil {
		t.Fatal("TestGlobalConfig() returned nil error, want probe failure")
	}
	if len(host.started) != 1 {
		t.Fatalf("start calls = %d, want 1", len(host.started))
	}
	if len(host.stopped) != 1 {
		t.Fatalf("stop calls = %d, want 1", len(host.stopped))
	}
	if host.stopped[0] != host.started[0].InstallationID {
		t.Fatalf("stopped installation id = %d, want %d", host.stopped[0], host.started[0].InstallationID)
	}
}

func TestServiceTestGlobalConfigUsesUniqueTemporaryInstallationIDs(t *testing.T) {
	originalProbe := runPluginConnectionCheck
	t.Cleanup(func() {
		runPluginConnectionCheck = originalProbe
	})

	runPluginConnectionCheck = func(
		_ context.Context,
		_ pluginClient,
		_ *pluginv1.PluginManifest,
	) error {
		return nil
	}

	manifest := connectionTestManifest(t, "silo.metadb", "0.0.36")
	installPath := writeInstalledPluginManifest(t, manifest)
	host := &fakeServiceHost{
		startResult: &fakePluginClient{manifest: manifest},
	}
	service := &Service{
		installations: newFakeServiceInstallationStore(&Installation{
			ID:          5,
			PluginID:    manifest.GetPluginId(),
			Version:     manifest.GetVersion(),
			InstallPath: installPath,
			Enabled:     true,
		}),
		host: host,
	}

	if err := service.TestGlobalConfig(context.Background(), 5, "connection", map[string]any{
		"api_key": "first",
	}); err != nil {
		t.Fatalf("first TestGlobalConfig() returned error: %v", err)
	}
	if err := service.TestGlobalConfig(context.Background(), 5, "connection", map[string]any{
		"api_key": "second",
	}); err != nil {
		t.Fatalf("second TestGlobalConfig() returned error: %v", err)
	}

	if len(host.started) != 2 {
		t.Fatalf("start calls = %d, want 2", len(host.started))
	}
	if host.started[0].InstallationID == host.started[1].InstallationID {
		t.Fatalf("temporary installation ids matched: %d", host.started[0].InstallationID)
	}
}

func connectionTestManifest(t *testing.T, pluginID, version string) *pluginv1.PluginManifest {
	t.Helper()

	manifest := testPluginManifest(t, pluginID, version)
	manifest.GlobalConfigSchema = []*pluginv1.ConfigSchema{
		{
			Key:        "connection",
			Title:      "Connection",
			Required:   true,
			JsonSchema: `{"type":"object","properties":{"api_key":{"type":"string"}},"required":["api_key"],"additionalProperties":false}`,
		},
	}
	return manifest
}
