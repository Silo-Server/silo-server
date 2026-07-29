package plugins

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

type fakeServiceConfigStore struct {
	configsByInstallation map[int][]*RuntimeConfig
	puts                  []putGlobalConfigCall
	putErr                error
	casFailures           int
	casCalls              int
	concurrentUpdates     map[string]any
}

type putGlobalConfigCall struct {
	installationID int
	key            string
	value          map[string]any
}

func TestPreserveStoredSecretsKeepsRedactedBlankAndAcceptsReplacement(t *testing.T) {
	store := &fakeServiceConfigStore{configsByInstallation: map[int][]*RuntimeConfig{
		7: {
			{InstallationID: 7, Key: "account", Value: map[string]any{"api_key": "saved", "region": "old"}},
		},
	}}
	service := &Service{configs: store}

	merged, err := service.preserveStoredSecrets(
		context.Background(),
		7,
		"account",
		map[string]any{"api_key": "", "region": "new"},
		[][]string{{"api_key"}},
	)
	if err != nil {
		t.Fatalf("preserveStoredSecrets: %v", err)
	}
	if merged["api_key"] != "saved" || merged["region"] != "new" {
		t.Fatalf("merged = %#v", merged)
	}

	replaced, err := service.preserveStoredSecrets(
		context.Background(),
		7,
		"account",
		map[string]any{"api_key": "clawrouter-e2e-secret"},
		[][]string{{"api_key"}},
	)
	if err != nil {
		t.Fatalf("preserveStoredSecrets replacement: %v", err)
	}
	if replaced["api_key"] != "clawrouter-e2e-secret" {
		t.Fatalf("replacement = %#v", replaced)
	}
}

func TestPreserveStoredSecretsMergesNestedObjectsWithoutMutatingInputs(t *testing.T) {
	savedConnection := map[string]any{
		"credentials": map[string]any{
			"api_key": "clawrouter-e2e-secret",
			"labels":  []any{"one", "two"},
		},
		"endpoint": "https://old.example.invalid",
	}
	incomingConnection := map[string]any{
		"credentials": map[string]any{
			"account": "updated",
			"api_key": "  ",
		},
		"endpoint": "",
	}
	wantSaved := cloneConfigValue(savedConnection)
	wantIncoming := cloneConfigValue(incomingConnection)
	store := &fakeServiceConfigStore{configsByInstallation: map[int][]*RuntimeConfig{
		7: {{
			InstallationID: 7,
			Key:            "account",
			Value:          map[string]any{"connection": savedConnection},
		}},
	}}
	service := &Service{configs: store}

	merged, err := service.preserveStoredSecrets(
		context.Background(),
		7,
		"account",
		map[string]any{"connection": incomingConnection},
		[][]string{{"connection", "credentials", "api_key"}},
	)
	if err != nil {
		t.Fatalf("preserveStoredSecrets: %v", err)
	}

	connection, ok := merged["connection"].(map[string]any)
	if !ok {
		t.Fatalf("merged connection = %#v, want object", merged["connection"])
	}
	credentials, ok := connection["credentials"].(map[string]any)
	if !ok {
		t.Fatalf("merged credentials = %#v, want object", connection["credentials"])
	}
	if credentials["api_key"] != "clawrouter-e2e-secret" || credentials["account"] != "updated" {
		t.Fatalf("merged credentials = %#v", credentials)
	}
	if connection["endpoint"] != "" {
		t.Fatalf("merged endpoint = %#v, want blank non-secret value", connection["endpoint"])
	}
	credentials["api_key"] = "mutated"
	credentials["labels"].([]any)[0] = "mutated"
	connection["endpoint"] = "https://mutated.example.invalid"
	if !reflect.DeepEqual(savedConnection, wantSaved) {
		t.Fatalf("saved input mutated: got %#v want %#v", savedConnection, wantSaved)
	}
	if !reflect.DeepEqual(incomingConnection, wantIncoming) {
		t.Fatalf("incoming input mutated: got %#v want %#v", incomingConnection, wantIncoming)
	}

	replacement, err := service.preserveStoredSecrets(
		context.Background(),
		7,
		"account",
		map[string]any{"connection": map[string]any{
			"credentials": map[string]any{"api_key": "test-auth-token"},
		}},
		[][]string{{"connection", "credentials", "api_key"}},
	)
	if err != nil {
		t.Fatalf("preserveStoredSecrets replacement: %v", err)
	}
	replacementConnection := replacement["connection"].(map[string]any)
	replacementCredentials := replacementConnection["credentials"].(map[string]any)
	if replacementCredentials["api_key"] != "test-auth-token" {
		t.Fatalf("replacement credentials = %#v", replacementCredentials)
	}
}

func TestSetGlobalConfigWithClearsValidatesTheClearedResult(t *testing.T) {
	for _, tc := range []struct {
		name      string
		required  bool
		wantError bool
	}{
		{name: "required field is rejected", required: true, wantError: true},
		{name: "optional field is removed", required: false, wantError: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := connectionTestManifest(t, "silo.metadb", "0.0.36")
			if tc.required {
				manifest.GlobalConfigSchema[0].JsonSchema = `{"type":"object","properties":{"api_key":{"type":"string","format":"password"}},"required":["api_key"],"additionalProperties":false}`
			} else {
				manifest.GlobalConfigSchema[0].JsonSchema = `{"type":"object","properties":{"api_key":{"type":"string","format":"password"}},"additionalProperties":false}`
			}
			installPath := writeInstalledPluginManifest(t, manifest)
			store := &fakeServiceConfigStore{configsByInstallation: map[int][]*RuntimeConfig{
				7: {{
					InstallationID: 7,
					Key:            "connection",
					Value: map[string]any{
						"api_key":      "clawrouter-e2e-secret",
						"plugin_owned": "retained",
					},
				}},
			}}
			service := &Service{
				installations: newFakeServiceInstallationStore(&Installation{
					ID:          7,
					PluginID:    manifest.GetPluginId(),
					Version:     manifest.GetVersion(),
					InstallPath: installPath,
					Enabled:     true,
				}),
				configs: store,
			}

			err := service.SetGlobalConfigWithClears(
				context.Background(),
				7,
				"connection",
				map[string]any{},
				[]string{"api_key"},
			)
			if tc.wantError {
				var validationErr *ConfigValidationError
				if !errors.As(err, &validationErr) {
					t.Fatalf("error = %v, want ConfigValidationError", err)
				}
				if len(store.puts) != 0 {
					t.Fatalf("persisted invalid cleared config: %#v", store.puts)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(store.puts) != 1 {
				t.Fatalf("put calls = %d, want 1", len(store.puts))
			}
			if _, present := store.puts[0].value["api_key"]; present {
				t.Fatalf("cleared field remained in config: %#v", store.puts[0].value)
			}
			if store.puts[0].value["plugin_owned"] != "retained" {
				t.Fatalf("plugin-owned field was not retained: %#v", store.puts[0].value)
			}
		})
	}
}

func TestSetGlobalConfigWithClearsPreservesAndExplicitlyClearsNestedSecretObject(t *testing.T) {
	manifest := connectionTestManifest(t, "silo.metadb", "0.0.36")
	manifest.GlobalConfigSchema[0].JsonSchema = `{
		"type":"object",
		"properties":{
			"connection":{
				"type":"object",
				"properties":{
					"api_key":{"type":"string","format":"password"},
					"endpoint":{"type":"string"}
				},
				"additionalProperties":false
			},
			"region":{"type":"string"}
		},
		"additionalProperties":false
	}`
	manifest.GlobalConfigSchema[0].AdminForm = nil
	installPath := writeInstalledPluginManifest(t, manifest)
	store := &fakeServiceConfigStore{configsByInstallation: map[int][]*RuntimeConfig{
		7: {{
			InstallationID: 7,
			Key:            "connection",
			Value: map[string]any{
				"connection": map[string]any{
					"api_key":  "clawrouter-e2e-secret",
					"endpoint": "https://old.example.invalid",
				},
				"region": "old",
			},
		}},
	}}
	service := &Service{
		installations: newFakeServiceInstallationStore(&Installation{
			ID:          7,
			PluginID:    manifest.GetPluginId(),
			Version:     manifest.GetVersion(),
			InstallPath: installPath,
			Enabled:     true,
		}),
		configs: store,
	}

	err := service.SetGlobalConfigWithClears(
		context.Background(),
		7,
		"connection",
		map[string]any{
			"connection": map[string]any{
				"api_key":  "",
				"endpoint": "",
			},
			"region": "new",
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.puts) != 1 {
		t.Fatalf("put calls = %d, want 1", len(store.puts))
	}
	connection := store.puts[0].value["connection"].(map[string]any)
	if connection["api_key"] != "clawrouter-e2e-secret" ||
		connection["endpoint"] != "" {
		t.Fatalf("persisted connection = %#v", connection)
	}

	err = service.SetGlobalConfigWithClears(
		context.Background(),
		7,
		"connection",
		map[string]any{"region": "new"},
		[]string{"connection"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.puts) != 2 {
		t.Fatalf("put calls = %d, want 2", len(store.puts))
	}
	if _, present := store.puts[1].value["connection"]; present {
		t.Fatalf("explicitly cleared connection remained: %#v", store.puts[1].value)
	}
}

func TestSetGlobalConfigPreservesOmittedPluginOwnedFields(t *testing.T) {
	manifest := connectionTestManifest(t, "silo.metadb", "0.0.36")
	manifest.GlobalConfigSchema[0].JsonSchema = `{
		"type":"object",
		"properties":{
			"display_name":{"type":"string"},
			"settings":{
				"type":"object",
				"properties":{"endpoint":{"type":"string"}},
				"additionalProperties":false
			}
		},
		"additionalProperties":false
	}`
	manifest.GlobalConfigSchema[0].AdminForm = nil
	installPath := writeInstalledPluginManifest(t, manifest)
	pluginState := map[string]any{
		"cursor":  "plugin-managed-cursor",
		"options": []any{"one", "two"},
	}
	store := &fakeServiceConfigStore{configsByInstallation: map[int][]*RuntimeConfig{
		7: {{
			InstallationID: 7,
			Key:            "connection",
			Value: map[string]any{
				"display_name": "old",
				"plugin_state": pluginState,
				"settings": map[string]any{
					"endpoint":     "https://example.invalid",
					"plugin_owned": "retained",
				},
			},
		}},
	}}
	service := &Service{
		installations: newFakeServiceInstallationStore(&Installation{
			ID:          7,
			PluginID:    manifest.GetPluginId(),
			Version:     manifest.GetVersion(),
			InstallPath: installPath,
			Enabled:     true,
		}),
		configs: store,
	}

	err := service.SetGlobalConfig(
		context.Background(),
		7,
		"connection",
		map[string]any{"display_name": "new"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.puts) != 1 {
		t.Fatalf("put calls = %d, want 1", len(store.puts))
	}
	if store.puts[0].value["display_name"] != "new" {
		t.Fatalf("display_name = %#v, want new", store.puts[0].value["display_name"])
	}
	if !reflect.DeepEqual(store.puts[0].value["plugin_state"], pluginState) {
		t.Fatalf(
			"plugin_state = %#v, want %#v",
			store.puts[0].value["plugin_state"],
			pluginState,
		)
	}
	wantSettings := map[string]any{
		"endpoint":     "https://example.invalid",
		"plugin_owned": "retained",
	}
	if !reflect.DeepEqual(store.puts[0].value["settings"], wantSettings) {
		t.Fatalf(
			"settings = %#v, want %#v",
			store.puts[0].value["settings"],
			wantSettings,
		)
	}

	err = service.SetGlobalConfig(
		context.Background(),
		7,
		"connection",
		map[string]any{"unexpected": "submitted"},
	)
	var validationErr *ConfigValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("submitted opaque field error = %v, want ConfigValidationError", err)
	}
	if len(store.puts) != 1 {
		t.Fatalf("invalid submitted field persisted: %#v", store.puts)
	}
}

func TestSetGlobalConfigRetriesConcurrentMerge(t *testing.T) {
	manifest := connectionTestManifest(t, "silo.metadb", "0.0.36")
	manifest.GlobalConfigSchema[0].JsonSchema = `{
		"type":"object",
		"properties":{"display_name":{"type":"string"}}
	}`
	manifest.GlobalConfigSchema[0].AdminForm = nil
	installPath := writeInstalledPluginManifest(t, manifest)
	store := &fakeServiceConfigStore{
		configsByInstallation: map[int][]*RuntimeConfig{
			7: {{
				InstallationID: 7,
				Key:            "connection",
				Value: map[string]any{
					"display_name": "old",
					"plugin_state": map[string]any{"cursor": "old"},
				},
			}},
		},
		casFailures: 1,
		concurrentUpdates: map[string]any{
			"plugin_state": map[string]any{"cursor": "concurrent"},
		},
	}
	service := &Service{
		installations: newFakeServiceInstallationStore(&Installation{
			ID:          7,
			PluginID:    manifest.GetPluginId(),
			Version:     manifest.GetVersion(),
			InstallPath: installPath,
			Enabled:     true,
		}),
		configs: store,
	}

	err := service.SetGlobalConfig(
		context.Background(),
		7,
		"connection",
		map[string]any{"display_name": "new"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.casCalls != 2 {
		t.Fatalf("CAS calls = %d, want 2", store.casCalls)
	}
	if len(store.puts) != 1 {
		t.Fatalf("successful writes = %d, want 1", len(store.puts))
	}
	if store.puts[0].value["display_name"] != "new" {
		t.Fatalf("display_name = %#v, want new", store.puts[0].value["display_name"])
	}
	wantState := map[string]any{"cursor": "concurrent"}
	if !reflect.DeepEqual(store.puts[0].value["plugin_state"], wantState) {
		t.Fatalf(
			"plugin_state = %#v, want concurrent update %#v",
			store.puts[0].value["plugin_state"],
			wantState,
		)
	}
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

func (f *fakeServiceConfigStore) CompareAndSwapGlobalConfig(
	_ context.Context,
	installationID int,
	key string,
	value map[string]any,
	expectedUpdatedAt *time.Time,
) (bool, error) {
	f.casCalls++
	if f.putErr != nil {
		return false, f.putErr
	}
	configs := f.configsByInstallation[installationID]
	var existing *RuntimeConfig
	for _, config := range configs {
		if config != nil && config.Key == key {
			existing = config
			break
		}
	}
	if f.casFailures > 0 {
		f.casFailures--
		if existing != nil {
			for field, update := range f.concurrentUpdates {
				existing.Value[field] = cloneConfigValue(update)
			}
			existing.UpdatedAt = existing.UpdatedAt.Add(time.Second)
		}
		return false, nil
	}
	switch {
	case existing == nil && expectedUpdatedAt != nil:
		return false, nil
	case existing != nil && (expectedUpdatedAt == nil ||
		!existing.UpdatedAt.Equal(*expectedUpdatedAt)):
		return false, nil
	}
	if existing == nil {
		existing = &RuntimeConfig{
			InstallationID: installationID,
			Key:            key,
		}
		f.configsByInstallation[installationID] = append(configs, existing)
	}
	existing.Value = cloneConfigValue(value).(map[string]any)
	existing.UpdatedAt = existing.UpdatedAt.Add(time.Second)
	f.puts = append(f.puts, putGlobalConfigCall{
		installationID: installationID,
		key:            key,
		value:          cloneConfigValue(value).(map[string]any),
	})
	return true, nil
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
	manifest.GlobalConfigSchema[0].JsonSchema = `{"type":"object","properties":{"api_key":{"type":"string","format":"password"}},"required":["api_key"],"additionalProperties":false}`
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

	err := service.TestGlobalConfigWithClears(
		context.Background(),
		42,
		"connection",
		map[string]any{},
		[]string{"api_key"},
	)
	var connectionErr *ConnectionTestError
	if !errors.As(err, &connectionErr) {
		t.Fatalf("cleared required secret error = %v, want ConnectionTestError", err)
	}
	if probeCalls != 1 || len(host.started) != 1 {
		t.Fatalf("invalid cleared config reached probe: probes=%d starts=%d", probeCalls, len(host.started))
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
