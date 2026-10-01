package watchsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	hostplugins "github.com/Silo-Server/silo-server/internal/plugins"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	testPluginProviderKey  = "plugin:4:anilist"
	testPluginCapabilityID = "anilist"
	testWatchHistoryID     = "history-1"
	testSecondHistoryID    = "history-2"
	testPlaybackSessionID  = "playback-1"
	testEpisodeMediaID     = "episode-1"
)

type fakeWatchSyncPluginClient struct {
	exchangeResponse    *pluginv1.WatchSyncCredentialResponse
	refreshResponse     *pluginv1.WatchSyncCredentialResponse
	accountResponse     *pluginv1.WatchSyncGetAccountResponse
	applyResponse       *pluginv1.WatchSyncApplyEventsResponse
	deviceStartResponse *pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse
	devicePollResponse  *pluginv1.WatchSyncDeviceAuthorizationServicePollResponse
	listResponse        *pluginv1.WatchSyncListRemoteStateResponse
	listResponses       []*pluginv1.WatchSyncListRemoteStateResponse
	applyStatus         pluginv1.WatchSyncApplyStatus // answers every event when applyResponse is nil
	applyFault          *pluginv1.WatchSyncFault      // attached to each applyStatus answer
	applyErr            error
	applyRequest        *pluginv1.WatchSyncApplyEventsRequest
	exchangeRequest     *pluginv1.WatchSyncExchangeAPIKeyRequest
	refreshRequest      *pluginv1.WatchSyncRefreshCredentialsRequest
	accountRequest      *pluginv1.WatchSyncGetAccountRequest
	deviceStartRequest  *pluginv1.WatchSyncDeviceAuthorizationServiceStartRequest
	devicePollRequest   *pluginv1.WatchSyncDeviceAuthorizationServicePollRequest
	listRequests        []*pluginv1.WatchSyncListRemoteStateRequest
}

func (f *fakeWatchSyncPluginClient) StartDeviceAuthorization(_ context.Context, req *pluginv1.WatchSyncDeviceAuthorizationServiceStartRequest) (*pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse, error) {
	f.deviceStartRequest = req
	if f.deviceStartResponse != nil {
		return f.deviceStartResponse, nil
	}
	return &pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse{}, nil
}

func (f *fakeWatchSyncPluginClient) PollDeviceAuthorization(_ context.Context, req *pluginv1.WatchSyncDeviceAuthorizationServicePollRequest) (*pluginv1.WatchSyncDeviceAuthorizationServicePollResponse, error) {
	f.devicePollRequest = req
	if f.devicePollResponse != nil {
		return f.devicePollResponse, nil
	}
	return &pluginv1.WatchSyncDeviceAuthorizationServicePollResponse{}, nil
}

func (f *fakeWatchSyncPluginClient) ExchangeAPIKey(_ context.Context, req *pluginv1.WatchSyncExchangeAPIKeyRequest) (*pluginv1.WatchSyncCredentialResponse, error) {
	f.exchangeRequest = req
	return f.exchangeResponse, nil
}
func (f *fakeWatchSyncPluginClient) RefreshCredentials(_ context.Context, req *pluginv1.WatchSyncRefreshCredentialsRequest) (*pluginv1.WatchSyncCredentialResponse, error) {
	f.refreshRequest = req
	if f.refreshResponse != nil {
		return f.refreshResponse, nil
	}
	return &pluginv1.WatchSyncCredentialResponse{}, nil
}
func (f *fakeWatchSyncPluginClient) GetAccount(_ context.Context, req *pluginv1.WatchSyncGetAccountRequest) (*pluginv1.WatchSyncGetAccountResponse, error) {
	f.accountRequest = req
	if f.accountResponse != nil {
		return f.accountResponse, nil
	}
	return &pluginv1.WatchSyncGetAccountResponse{Account: &pluginv1.WatchSyncAccount{ExternalSubject: testProviderAccountID}}, nil
}
func (f *fakeWatchSyncPluginClient) ApplyEvents(_ context.Context, req *pluginv1.WatchSyncApplyEventsRequest) (*pluginv1.WatchSyncApplyEventsResponse, error) {
	f.applyRequest = req
	if f.applyResponse == nil && f.applyStatus != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_UNSPECIFIED {
		response := &pluginv1.WatchSyncApplyEventsResponse{}
		for _, event := range req.GetEvents() {
			response.Results = append(response.Results, &pluginv1.WatchSyncApplyResult{EventId: event.GetEventId(), Status: f.applyStatus, Fault: f.applyFault})
		}
		return response, f.applyErr
	}
	return f.applyResponse, f.applyErr
}

func (f *fakeWatchSyncPluginClient) ListRemoteState(_ context.Context, req *pluginv1.WatchSyncListRemoteStateRequest) (*pluginv1.WatchSyncListRemoteStateResponse, error) {
	f.listRequests = append(f.listRequests, req)
	if len(f.listResponses) > 0 {
		response := f.listResponses[0]
		f.listResponses = f.listResponses[1:]
		return response, nil
	}
	if f.listResponse != nil {
		return f.listResponse, nil
	}
	return &pluginv1.WatchSyncListRemoteStateResponse{}, nil
}

type fakePluginCredentialRepository struct {
	saved Connection
	err   error
}

func (r *fakePluginCredentialRepository) UpsertConnection(_ context.Context, conn Connection) (Connection, error) {
	if r.err != nil {
		return Connection{}, r.err
	}
	r.saved = conn
	return conn, nil
}

func testPluginProvider(t *testing.T, client WatchSyncPluginClient) *PluginProvider {
	t.Helper()
	return testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ExportWatched: true,
		MaxBatchSize:  25,
	})
}

func testPluginProviderWithDescriptor(t *testing.T, client WatchSyncPluginClient, descriptor *pluginv1.WatchSyncProviderDescriptor) *PluginProvider {
	t.Helper()
	provider, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4,
		ProviderKey:    testPluginProviderKey,
		CapabilityID:   testPluginCapabilityID,
		DisplayName:    "AniList",
		Descriptor:     descriptor,
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return client, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestPluginProviderUsesConnectionSpecificHistorySource(t *testing.T) {
	provider := testPluginProvider(t, &fakeWatchSyncPluginClient{})
	if got := provider.HistorySource(); got != testPluginProviderKey {
		t.Fatalf("HistorySource() = %q, want %q", got, testPluginProviderKey)
	}
}

func TestPluginProviderRejectsUnsupportedInitialDescriptor(t *testing.T) {
	_, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4,
		ProviderKey:    testPluginProviderKey,
		CapabilityID:   testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{
			AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_AUTHORIZATION_CODE},
			ExportWatched: true,
		},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) { return nil, nil },
	})
	if err == nil {
		t.Fatal("expected authorization-code-only descriptor to be rejected")
	}

	_, err = NewPluginProvider(PluginProviderOptions{
		InstallationID: 4,
		ProviderKey:    testPluginProviderKey,
		CapabilityID:   testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{
			AuthMethods:         []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
			ExportWatched:       true,
			SupportedMediaTypes: []pluginv1.WatchSyncMediaType{pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_UNSPECIFIED},
		},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) { return nil, nil },
	})
	if err == nil {
		t.Fatal("expected unsupported media descriptor to be rejected")
	}

	_, err = NewPluginProvider(PluginProviderOptions{
		InstallationID: 4,
		ProviderKey:    testPluginProviderKey,
		CapabilityID:   testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE,
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) { return nil, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "multiple host authentication methods") {
		t.Fatalf("multiple auth methods error = %v", err)
	}
}

func TestPluginProviderConnectsAPIKeyWithoutPersistingInPluginConfig(t *testing.T) {
	client := &fakeWatchSyncPluginClient{exchangeResponse: &pluginv1.WatchSyncCredentialResponse{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: testValidatedToken, TokenType: testBearerTokenType},
		Account:     &pluginv1.WatchSyncAccount{ExternalSubject: "7", Username: testPluginUsername},
	}}
	provider := testPluginProvider(t, client)
	if provider.Key() != testPluginProviderKey {
		t.Fatalf("provider key = %q", provider.Key())
	}
	tokens, account, err := provider.ConnectWithAPIKey(context.Background(), "input-token")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != testValidatedToken || account.ID != "7" || account.Username != testPluginUsername {
		t.Fatalf("tokens=%#v account=%#v", tokens, account)
	}
}

func TestPluginProviderRejectsMissingAccountIdentity(t *testing.T) {
	for _, subject := range []string{"", " \t\n "} {
		client := &fakeWatchSyncPluginClient{exchangeResponse: &pluginv1.WatchSyncCredentialResponse{
			Credentials: &pluginv1.WatchSyncCredentials{AccessToken: testValidatedToken, TokenType: testBearerTokenType},
			Account:     &pluginv1.WatchSyncAccount{ExternalSubject: subject, Username: testPluginUsername},
		}}
		provider := testPluginProvider(t, client)
		if _, _, err := provider.ConnectWithAPIKey(context.Background(), "input-token"); err == nil || !strings.Contains(err.Error(), "account identity") {
			t.Fatalf("subject %q: error = %v", subject, err)
		}
	}
}

func TestPluginProviderValidatesAndOverlaysConnectionConfig(t *testing.T) {
	client := &fakeWatchSyncPluginClient{exchangeResponse: &pluginv1.WatchSyncCredentialResponse{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: testValidatedToken, TokenType: testBearerTokenType},
		Account:     &pluginv1.WatchSyncAccount{ExternalSubject: "7", Username: testPluginUsername},
	}}
	schema := &pluginv1.ConfigSchema{
		Key: "floppy", Title: "Floppy server", Required: true,
		JsonSchema: `{"type":"object","properties":{"base_url":{"type":"string","format":"uri"}},"required":["base_url"],"additionalProperties":false}`,
		AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{
			Key: "base_url", Label: "Base URL", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT, Required: true,
		}}},
	}
	provider, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		DisplayName: "Floppy", Descriptor: &pluginv1.WatchSyncProviderDescriptor{
			AuthMethods: []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{schema},
		ResolveClient:          func(context.Context, int, string) (WatchSyncPluginClient, error) { return client, nil },
		ResolveConfig: func(context.Context, int) (*pluginv1.WatchSyncProviderConfig, error) {
			return &pluginv1.WatchSyncProviderConfig{Values: map[string]string{"floppy.base_url": "https://legacy.example.com"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = provider.ConnectWithAPIKeyConfig(context.Background(), "token", ConnectionConfigValues{
		"floppy": {"base_url": "https://personal.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := client.exchangeRequest.GetProviderConfig().GetValues()["floppy.base_url"]; got != "https://personal.example.com" {
		t.Fatalf("base URL = %q", got)
	}
	views := provider.ConnectionConfigSchema()
	if len(views) != 1 || views[0].AdminForm == nil || views[0].AdminForm.Fields[0].Control != "TEXT" {
		t.Fatalf("connection config schema = %#v", views)
	}
	if _, _, err := provider.ConnectWithAPIKeyConfig(context.Background(), "token", nil); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing config error = %v", err)
	}
}

func TestPluginProviderClassifiesAndRedactsConnectionSecrets(t *testing.T) {
	client := &fakeWatchSyncPluginClient{exchangeResponse: &pluginv1.WatchSyncCredentialResponse{
		Fault: &pluginv1.WatchSyncFault{
			Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL,
			SafeMessage: "credentials " + testSecretValue + " were rejected",
		},
	}}
	schema := &pluginv1.ConfigSchema{
		Key: "account",
		JsonSchema: `{
			"type":"object",
			"properties":{
				"base_url":{"type":"string","format":"uri"},
				"client_secret":{"type":"string","format":"password"}
			},
			"required":["base_url","client_secret"],
			"additionalProperties":false
		}`,
		AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{
			{Key: "base_url", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT},
			// JSON Schema remains authoritative even when a form incorrectly
			// presents a credential as ordinary text.
			{Key: "client_secret", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT},
		}},
	}
	provider, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{schema},
		ResolveClient:          func(context.Context, int, string) (WatchSyncPluginClient, error) { return client, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = provider.ConnectWithAPIKeyConfig(context.Background(), "input-token", ConnectionConfigValues{
		"account": {
			"base_url":      "https://floppy.example.com",
			"client_secret": testSecretValue,
		},
	})
	if !isWatchSyncInvalidCredentialError(err) {
		t.Fatalf("error = %#v", err)
	}
	config := client.exchangeRequest.GetProviderConfig()
	if config.GetValues()["account.base_url"] != "https://floppy.example.com" ||
		config.GetSecretValues()["account.client_secret"] != testSecretValue {
		t.Fatalf("provider config = %#v", config)
	}
	if _, exposed := config.GetValues()["account.client_secret"]; exposed {
		t.Fatal("JSON-schema password was exposed as a public provider value")
	}
	if strings.Contains(err.Error(), testSecretValue) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("connection secrets were not redacted: %q", err)
	}
}

func TestConnectionConfigValidationRedactsNestedSecrets(t *testing.T) {
	const nestedSecret = "nested-connection-secret"
	schema := &pluginv1.ConfigSchema{
		Key:        "account",
		JsonSchema: `{"type":"object","properties":{"advanced":{"type":"object","properties":{"password":{"type":"string","format":"password"}}}}}`,
	}
	secrets := connectionConfigSecrets(
		[]*pluginv1.ConfigSchema{schema},
		ConnectionConfigValues{"account": {"advanced": map[string]any{"password": nestedSecret}}},
	)
	err := sanitizedConnectionConfigError(errors.New("rejected "+nestedSecret), secrets)
	if strings.Contains(err.Error(), nestedSecret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("nested connection secret was not redacted: %q", err)
	}
}

func TestPluginProviderRedactsUndeclaredConnectionSecrets(t *testing.T) {
	const undeclaredSecret = "undeclared-connection-secret"
	client := &fakeWatchSyncPluginClient{exchangeResponse: &pluginv1.WatchSyncCredentialResponse{
		Fault: &pluginv1.WatchSyncFault{
			Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL,
			SafeMessage: "credentials " + undeclaredSecret + " were rejected",
		},
	}}
	// The schema permits additional properties, so an undeclared field reaches
	// the plugin classified as a secret and must be redacted like a declared one.
	schema := &pluginv1.ConfigSchema{
		Key:        "account",
		JsonSchema: `{"type":"object","properties":{"base_url":{"type":"string","format":"uri"}},"required":["base_url"]}`,
		AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{
			{Key: "base_url", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT},
		}},
	}
	provider, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{schema},
		ResolveClient:          func(context.Context, int, string) (WatchSyncPluginClient, error) { return client, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = provider.ConnectWithAPIKeyConfig(context.Background(), "input-token", ConnectionConfigValues{
		"account": {
			"base_url":  "https://floppy.example.com",
			"api_token": undeclaredSecret,
		},
	})
	if !isWatchSyncInvalidCredentialError(err) {
		t.Fatalf("error = %#v", err)
	}
	config := client.exchangeRequest.GetProviderConfig()
	if config.GetSecretValues()["account.api_token"] != undeclaredSecret {
		t.Fatalf("undeclared field was not classified as a secret: %#v", config)
	}
	if strings.Contains(err.Error(), undeclaredSecret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("undeclared connection secret was not redacted: %q", err)
	}
}

func TestPluginProviderRejectsUnresolvableDynamicConnectionOptions(t *testing.T) {
	_, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{{
			Key: "server",
			AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{
				Key: "library", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SELECT,
				DynamicOptions: true,
			}}},
		}},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return &fakeWatchSyncPluginClient{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires dynamic options") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginProviderRejectsBlankConnectionSelectOption(t *testing.T) {
	_, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{{
			Key:        "server",
			JsonSchema: `{"type":"object","properties":{"mode":{"type":"string"}}}`,
			AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{
				Key: "mode", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SELECT,
				Options: []*pluginv1.AdminFormOption{{Value: " ", Label: "Choose a mode"}},
			}}},
		}},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return &fakeWatchSyncPluginClient{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "blank select option value") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginProviderRejectsDuplicateConnectionConfigKeys(t *testing.T) {
	_, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{
			{Key: "server", JsonSchema: `{"type":"object","properties":{}}`},
			{Key: "server", JsonSchema: `{"type":"object","properties":{}}`},
		},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return &fakeWatchSyncPluginClient{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginProviderRejectsBlankConnectionConfigKey(t *testing.T) {
	_, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{{
			Key: " \t", Required: true, JsonSchema: `{"type":"object","properties":{}}`,
		}},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return &fakeWatchSyncPluginClient{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "key is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginProviderRejectsUnsupportedConnectionConfigExclusivity(t *testing.T) {
	_, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{{
			Key: "server", JsonSchema: `{"type":"object","properties":{"primary":{"type":"boolean"},"group":{"type":"string"}}}`,
			AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{
				Key: "primary", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SWITCH,
				ExclusiveGroupField: "group",
			}}},
		}},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return &fakeWatchSyncPluginClient{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "exclusive_group_field") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginProviderRejectsRequiredConnectionConfigTheWebCannotRender(t *testing.T) {
	_, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{{
			Key: "server", Required: true,
			JsonSchema: `{"type":"object","properties":{"headers":{"type":"object"}}}`,
			AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{
				Key: "headers", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXTAREA,
			}}},
		}},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return &fakeWatchSyncPluginClient{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be inferred") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginProviderRejectsOptionalConnectionConfigTheWebCannotRender(t *testing.T) {
	_, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{{
			Key:        "headers",
			JsonSchema: `{"type":"object","properties":{"values":{"type":"object"}}}`,
		}},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return &fakeWatchSyncPluginClient{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be inferred") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginProviderAcceptsRenderableRequiredConnectionConfig(t *testing.T) {
	_, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{
			{
				Key: "server", Required: true,
				JsonSchema: `{"type":"object","properties":{"base_url":{"type":"string"},"username":{"type":"string"}},"required":["base_url","username"]}`,
				AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{
					Key: "base_url", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT,
				}}},
			},
			{
				Key: "features", Required: true,
				JsonSchema: `{"type":"object","properties":{"flags":{"type":"array","items":{"type":"boolean"}}}}`,
				AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{
					Key: "flags", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_MULTI_SELECT,
					Options: []*pluginv1.AdminFormOption{{Value: "true", Label: "Enabled"}, {Value: "false", Label: "Disabled"}},
				}}},
			},
			{
				Key: "mode", Required: true,
				JsonSchema: `{"type":"object","properties":{"value":{"enum":["standard","anime"]}}}`,
				AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{
					Key: "value", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SELECT,
					Options: []*pluginv1.AdminFormOption{{Value: "standard", Label: "Standard"}, {Value: "anime", Label: "Anime"}},
				}}},
			},
			{
				Key: "reference", Required: true,
				JsonSchema: `{"type":"object","properties":{"endpoint":{"$ref":"#/$defs/endpoint"}},"$defs":{"endpoint":{"type":"string","format":"uri"}}}`,
				AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{
					Key: "endpoint", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT,
				}}},
			},
		},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return &fakeWatchSyncPluginClient{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPluginProviderRejectsConnectionControlThatCannotEmitSchemaType(t *testing.T) {
	_, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{{
			Key:        "server",
			JsonSchema: `{"type":"object","properties":{"name":{"type":"string"}}}`,
			AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{
				Key: "name", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SWITCH,
			}}},
		}},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return &fakeWatchSyncPluginClient{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot emit json_schema type") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginProviderEnforcesVisibleRequiredConnectionAdminFields(t *testing.T) {
	client := &fakeWatchSyncPluginClient{exchangeResponse: &pluginv1.WatchSyncCredentialResponse{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: testValidatedToken},
		Account:     &pluginv1.WatchSyncAccount{ExternalSubject: "7"},
	}}
	provider, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{{
			Key:        "server",
			JsonSchema: `{"type":"object","properties":{"advanced":{"type":"boolean"},"endpoint":{"type":"string"}},"required":["advanced"],"additionalProperties":false}`,
			AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{
				{Key: "advanced", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SWITCH},
				{
					Key: "endpoint", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT, Required: true,
					ShowWhen: []*pluginv1.AdminFormCondition{{Field: "advanced", Equals: []string{"true"}}},
				},
			}},
		}},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return client, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := provider.ConnectWithAPIKeyConfig(context.Background(), "token", ConnectionConfigValues{
		"server": {"advanced": false},
	}); err != nil {
		t.Fatalf("hidden required field: %v", err)
	}
	if _, _, err := provider.ConnectWithAPIKeyConfig(context.Background(), "token", ConnectionConfigValues{
		"server": {"advanced": true},
	}); err == nil || !strings.Contains(err.Error(), `field "endpoint" is required`) {
		t.Fatalf("visible required field error = %v", err)
	}
}

func TestPluginProviderRejectsFlattenedConnectionConfigCollision(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	provider, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{
			{Key: "a", JsonSchema: `{"type":"object","properties":{"b.c":{"type":"string"}},"additionalProperties":false}`},
			{Key: "a.b", JsonSchema: `{"type":"object","properties":{"c":{"type":"string"}},"additionalProperties":false}`},
		},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return client, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = provider.ConnectWithAPIKeyConfig(context.Background(), "token", ConnectionConfigValues{
		"a":   {"b.c": "first"},
		"a.b": {"c": "second"},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts") || !strings.Contains(err.Error(), "after flattening") {
		t.Fatalf("error = %v", err)
	}
	if client.exchangeRequest != nil {
		t.Fatal("ambiguous connection config reached the plugin")
	}
}

func TestPluginProviderEnforcesConnectionAdminFormValidation(t *testing.T) {
	client := &fakeWatchSyncPluginClient{exchangeResponse: &pluginv1.WatchSyncCredentialResponse{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: testValidatedToken},
		Account:     &pluginv1.WatchSyncAccount{ExternalSubject: "7"},
	}}
	provider, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{{
			Key: "server", Required: true,
			JsonSchema: `{"type":"object","properties":{"name":{"type":"string"},"port":{},"password":{"type":"string","format":"password"}},"required":["name","port","password"]}`,
			AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{
				{Key: "name", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT, Validation: &pluginv1.AdminFormValidation{Pattern: `^[a-z]+$`, MinLength: 3, MaxLength: 8}},
				{Key: "port", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_NUMBER, Validation: &pluginv1.AdminFormValidation{HasMin: true, Min: 1, HasMax: true, Max: 65535}},
				{Key: "password", Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_PASSWORD, Secret: true, Validation: &pluginv1.AdminFormValidation{MinLength: 8}},
			}},
		}},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return client, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		values  map[string]any
		message string
	}{
		{name: "pattern", values: map[string]any{"name": "Bad", "port": 443.0, "password": "long-enough"}, message: "is invalid"},
		{name: "number", values: map[string]any{"name": "good", "port": 70000.0, "password": "long-enough"}, message: "at most 65535"},
		{name: "numeric string", values: map[string]any{"name": "good", "port": "70000", "password": "long-enough"}, message: "at most 65535"},
		{name: "secret length", values: map[string]any{"name": "good", "port": 443.0, "password": "leaky"}, message: "at least 8 characters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := provider.ConnectWithAPIKeyConfig(context.Background(), "token", ConnectionConfigValues{"server": tt.values})
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "leaky") {
				t.Fatalf("secret leaked in validation error: %q", err)
			}
		})
	}
}

func TestPluginProviderRejectsConnectionConfigForDeviceAuthorization(t *testing.T) {
	_, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{AuthMethods: []pluginv1.WatchSyncAuthMethod{
			pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE,
		}},
		ConnectionConfigSchema: []*pluginv1.ConfigSchema{{Key: "server"}},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return &fakeWatchSyncPluginClient{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "connection config requires API-key authentication") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginProviderRefreshReturnsCredentialsAlongsideFault(t *testing.T) {
	client := &fakeWatchSyncPluginClient{refreshResponse: &pluginv1.WatchSyncCredentialResponse{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: testRotatedAccessToken, TokenType: testBearerTokenType},
		Fault: &pluginv1.WatchSyncFault{
			Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL,
			SafeMessage: "credential rotated-access rejected; reconnect required",
		},
	}}
	provider := testPluginProvider(t, client)
	tokens, err := provider.RefreshToken(context.Background(), ServerConfig{}, Connection{
		AccessToken:  testOldAccessToken,
		RefreshToken: testOldRefreshToken,
	})
	if tokens.AccessToken != testRotatedAccessToken || tokens.RefreshToken != "" || tokens.TokenExpiresAt != nil {
		t.Fatalf("tokens = %#v", tokens)
	}
	if !isWatchSyncInvalidCredentialError(err) {
		t.Fatalf("error = %#v", err)
	}
	if strings.Contains(err.Error(), testRotatedAccessToken) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("returned credentials were not redacted: %q", err)
	}
}

func TestPluginProviderExportsRichEpisodeIdentity(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	client.applyResponse = &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{{
		EventId: testWatchHistoryID, Status: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED,
	}}}
	provider := testPluginProvider(t, client)
	result, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{AccessToken: testSecretValue}, []LocalPlay{{
		HistoryID:       testWatchHistoryID,
		MediaItemID:     testEpisodeMediaID,
		Kind:            historyimport.KindEpisode,
		SeriesTVDBID:    "123",
		SeriesTMDBID:    "456",
		SeasonNumber:    2,
		EpisodeNumber:   7,
		WatchedAt:       time.Now().UTC(),
		DurationSeconds: 1440,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sent) != 1 || result.Sent[0] != testWatchHistoryID {
		t.Fatalf("result = %#v", result)
	}
	event := client.applyRequest.GetEvents()[0]
	if client.applyRequest.GetContext().GetCredentials().GetAccessToken() != testSecretValue ||
		event.GetMedia().GetSeriesExternalIds()["tvdb"] != "123" ||
		event.GetMedia().GetEpisodeNumber() != 7 ||
		event.GetMedia().GetMediaType() != pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE {
		t.Fatalf("apply request = %#v", client.applyRequest)
	}
}

func TestPluginProviderBatchesEventsInOneRPC(t *testing.T) {
	client := &fakeWatchSyncPluginClient{applyResponse: &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{
		{EventId: testWatchHistoryID, Status: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED},
		{EventId: testSecondHistoryID, Status: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED},
	}}}
	provider := testPluginProvider(t, client)
	result, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{}, []LocalPlay{
		{HistoryID: testWatchHistoryID, Kind: historyimport.KindMovie},
		{HistoryID: testSecondHistoryID, Kind: historyimport.KindMovie},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.applyRequest.GetEvents()) != 2 || len(result.Sent) != 1 || len(result.NotFound) != 1 {
		t.Fatalf("request=%#v result=%#v", client.applyRequest, result)
	}
}

func TestPluginProviderRejectsUnspecifiedExportMedia(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProvider(t, client)
	result, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{}, []LocalPlay{{HistoryID: testWatchHistoryID}})
	if err != nil {
		t.Fatal(err)
	}
	if client.applyRequest != nil {
		t.Fatalf("unexpected apply request = %#v", client.applyRequest)
	}
	if result.Failed[testWatchHistoryID] != watchSyncUnsupportedMediaMessage {
		t.Fatalf("result = %#v", result)
	}
}

func TestPluginProviderSkipsUnsupportedExportMedia(t *testing.T) {
	client := &fakeWatchSyncPluginClient{applyResponse: &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{
		{EventId: testWatchHistoryID, Status: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED},
	}}}
	provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:         []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ExportWatched:       true,
		MaxBatchSize:        25,
		SupportedMediaTypes: []pluginv1.WatchSyncMediaType{pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE},
	})
	result, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{}, []LocalPlay{
		{HistoryID: testWatchHistoryID, Kind: historyimport.KindMovie},
		{HistoryID: testSecondHistoryID, Kind: historyimport.KindEpisode},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.applyRequest.GetEvents()) != 1 || client.applyRequest.GetEvents()[0].GetEventId() != testWatchHistoryID {
		t.Fatalf("apply request = %#v", client.applyRequest)
	}
	if got := result.Failed[testSecondHistoryID]; got != watchSyncUnsupportedEpisodeMediaMessage {
		t.Fatalf("result = %#v", result)
	}
}

func TestPluginProviderMapsPerEventRateLimitAndKeepsSuccesses(t *testing.T) {
	client := &fakeWatchSyncPluginClient{applyResponse: &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{
		{EventId: testWatchHistoryID, Status: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED},
		{
			EventId: testSecondHistoryID,
			Status:  pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY,
			Fault: &pluginv1.WatchSyncFault{
				Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_RATE_LIMITED,
				SafeMessage: "slow down",
				RetryAfter:  durationpb.New(45 * time.Second),
			},
		},
	}}}
	provider := testPluginProvider(t, client)
	result, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{}, []LocalPlay{
		{HistoryID: testWatchHistoryID, Kind: historyimport.KindMovie},
		{HistoryID: testSecondHistoryID, Kind: historyimport.KindMovie},
		{HistoryID: "history-3", Kind: historyimport.KindMovie},
	})
	limited, ok := AsRateLimited(err)
	if !ok || limited.RetryAfter != 45*time.Second {
		t.Fatalf("error = %#v", err)
	}
	if len(result.Sent) != 1 || result.Sent[0] != testWatchHistoryID || len(result.Failed) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestPluginProviderTransportFailureIsRetryableAndSanitized(t *testing.T) {
	client := &fakeWatchSyncPluginClient{applyErr: errors.New("rpc failed with access_token=secret")}
	provider := testPluginProvider(t, client)
	_, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{}, []LocalPlay{{HistoryID: testWatchHistoryID, Kind: historyimport.KindMovie}})
	if !isRetryableProviderError(err) || strings.Contains(err.Error(), testSecretValue) {
		t.Fatalf("error = %#v", err)
	}
}

func TestPluginProviderSanitizesFaultMessage(t *testing.T) {
	message := safeApplyMessage(&pluginv1.WatchSyncApplyResult{Fault: &pluginv1.WatchSyncFault{
		SafeMessage: "  failed\n\taccess-token " + strings.Repeat("x", 300),
	}}, "access-token")
	if strings.ContainsAny(message, "\n\t") || strings.Contains(message, "access-token") || len([]rune(message)) > 257 {
		t.Fatalf("message was not sanitized: %q", message)
	}
}

func TestPluginProviderNormalizesSecretsBeforeRedaction(t *testing.T) {
	message := safeApplyMessage(&pluginv1.WatchSyncApplyResult{Fault: &pluginv1.WatchSyncFault{
		SafeMessage: "credential line one line two was rejected",
	}}, "line one\nline two")
	if strings.Contains(message, "line one line two") || !strings.Contains(message, "[REDACTED]") {
		t.Fatalf("message was not redacted: %q", message)
	}
}

func TestPluginProviderMapsTemporaryRetryToFailed(t *testing.T) {
	client := &fakeWatchSyncPluginClient{applyResponse: &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{{
		EventId: testWatchHistoryID,
		Status:  pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY,
		Fault: &pluginv1.WatchSyncFault{
			Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_TEMPORARY,
			SafeMessage: "temporary upstream failure",
		},
	}}}}
	provider := testPluginProvider(t, client)
	result, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{}, []LocalPlay{{HistoryID: testWatchHistoryID, Kind: historyimport.KindMovie}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed[testWatchHistoryID] != "temporary upstream failure" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPluginProviderMapsRateLimitFault(t *testing.T) {
	client := &fakeWatchSyncPluginClient{applyResponse: &pluginv1.WatchSyncApplyEventsResponse{Fault: &pluginv1.WatchSyncFault{
		Code:       pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_RATE_LIMITED,
		RetryAfter: durationpb.New(30 * time.Second),
	}}}
	provider := testPluginProvider(t, client)
	_, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{}, []LocalPlay{{HistoryID: "h", ProviderItemKey: "p", Kind: historyimport.KindMovie}})
	limited, ok := AsRateLimited(err)
	if !ok || limited.RetryAfter != 30*time.Second {
		t.Fatalf("error = %#v", err)
	}
}

func TestPluginProviderRejectsUnsupportedScrobbleMedia(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:         []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ExportWatched:       true,
		MaxBatchSize:        25,
		SupportedMediaTypes: []pluginv1.WatchSyncMediaType{pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE},
	})
	err := provider.Stop(context.Background(), ServerConfig{}, Connection{}, ScrobbleEvent{
		Completed:         true,
		HistoryID:         testWatchHistoryID,
		PlaybackSessionID: testPlaybackSessionID,
		Kind:              historyimport.KindEpisode,
		OccurredAt:        time.Now().UTC(),
	})
	if err == nil || err.Error() != watchSyncUnsupportedEpisodeMediaMessage {
		t.Fatalf("error = %#v", err)
	}
	if client.applyRequest != nil {
		t.Fatalf("unexpected apply request = %#v", client.applyRequest)
	}
}

func TestPluginProviderPreservesScrobbleRetryClassification(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:      []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ScrobblePlayback: true,
		MaxBatchSize:     25,
	})
	event := ScrobbleEvent{
		PlaybackSessionID: testPlaybackSessionID,
		MediaItemID:       testMovieMediaID,
		Kind:              historyimport.KindMovie,
		OccurredAt:        time.Now().UTC(),
	}
	eventID := "scrobble:" + pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_START.String() + ":" + testPlaybackSessionID
	client.applyResponse = &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{{
		EventId: eventID,
		Status:  pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY,
		Fault: &pluginv1.WatchSyncFault{
			Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_TEMPORARY,
			SafeMessage: "retry later",
		},
	}}}
	if err := provider.Start(context.Background(), ServerConfig{}, Connection{}, event); !isRetryableProviderError(err) {
		t.Fatalf("retry error = %#v, want retryableProviderError", err)
	}

	client.applyResponse = &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{{
		EventId: eventID,
		Status:  pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED,
	}}}
	if err := provider.Start(context.Background(), ServerConfig{}, Connection{}, event); err == nil || isRetryableProviderError(err) {
		t.Fatalf("rejected error = %#v, want terminal error", err)
	}
}

func TestPluginProviderAuthenticatedContextUsesCapabilityAndCredentials(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProvider(t, client)
	if _, err := provider.LookupAccount(context.Background(), ServerConfig{}, Connection{AccessToken: "token"}); err != nil {
		t.Fatal(err)
	}
	if client.accountRequest.GetContext().GetCapabilityId() != testPluginCapabilityID ||
		client.accountRequest.GetContext().GetCredentials().GetAccessToken() != "token" {
		t.Fatalf("account request = %#v", client.accountRequest)
	}
	_ = testPlaybackSessionID
}

func TestPluginProviderSupportsDeviceAuthorizationAndFullCredentials(t *testing.T) {
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	client := &fakeWatchSyncPluginClient{
		deviceStartResponse: &pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse{
			UserCode:                "ABCD",
			VerificationUrl:         "https://provider.example/activate",
			VerificationUrlComplete: "https://provider.example/activate?code=ABCD",
			ProviderState:           []byte("opaque-device-state"),
			PollingInterval:         durationpb.New(7 * time.Second),
			ExpiresAt:               timestamppb.New(expiresAt),
		},
		devicePollResponse: &pluginv1.WatchSyncDeviceAuthorizationServicePollResponse{
			Status: pluginv1.WatchSyncDeviceAuthorizationStatus_WATCH_SYNC_DEVICE_AUTHORIZATION_STATUS_AUTHORIZED,
			Credentials: &pluginv1.WatchSyncCredentials{
				AccessToken: testAccessToken, RefreshToken: testRefreshToken, TokenType: testDPoPTokenType,
				Scopes: []string{testHistoryScope, "watchlist"}, SecretAttributes: map[string]string{"instance": testOneValue},
				ExpiresAt: timestamppb.New(expiresAt),
			},
		},
	}
	provider, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{
			AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE},
			ImportWatched: true, MaxBatchSize: 25,
		},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) { return client, nil },
		ResolveConfig: func(context.Context, int) (*pluginv1.WatchSyncProviderConfig, error) {
			return &pluginv1.WatchSyncProviderConfig{SecretValues: map[string]string{"provider.client_secret": testSecretValue}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := provider.StartDeviceAuth(context.Background(), ServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if session.UserCode != "ABCD" || session.IntervalSeconds != 7 ||
		session.VerificationURL != "https://provider.example/activate?code=ABCD" ||
		client.deviceStartRequest.GetProviderConfig().GetSecretValues()["provider.client_secret"] != testSecretValue {
		t.Fatalf("session=%#v request=%#v", session, client.deviceStartRequest)
	}
	tokens, err := provider.PollDeviceAuth(context.Background(), ServerConfig{}, session)
	if err != nil {
		t.Fatal(err)
	}
	if string(client.devicePollRequest.GetProviderState()) != "opaque-device-state" ||
		tokens.TokenType != testDPoPTokenType || len(tokens.Scopes) != 2 || tokens.SecretAttributes["instance"] != testOneValue {
		t.Fatalf("tokens=%#v request=%#v", tokens, client.devicePollRequest)
	}
}

func TestPluginProviderRejectsInvalidDeviceAuthorizationMetadata(t *testing.T) {
	valid := func() *pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse {
		return &pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse{
			UserCode:        "ABCD",
			VerificationUrl: "https://provider.example/activate",
			ProviderState:   []byte("opaque"),
			PollingInterval: durationpb.New(5 * time.Second),
			ExpiresAt:       timestamppb.New(time.Now().UTC().Add(10 * time.Minute)),
		}
	}
	tests := map[string]func(*pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse){
		"relative URL": func(response *pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse) {
			response.VerificationUrl = "/activate"
		},
		"URL userinfo": func(response *pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse) {
			response.VerificationUrl = "https://user:pass@provider.example/activate"
		},
		"unsafe complete URL": func(response *pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse) {
			response.VerificationUrlComplete = "javascript:alert(1)"
		},
		"invalid timestamp": func(response *pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse) {
			response.ExpiresAt = &timestamppb.Timestamp{Seconds: 253402300800}
		},
		"expired timestamp": func(response *pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse) {
			response.ExpiresAt = timestamppb.New(time.Now().UTC().Add(-time.Minute))
		},
		"invalid interval": func(response *pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse) {
			response.PollingInterval = durationpb.New(-time.Second)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			response := valid()
			mutate(response)
			client := &fakeWatchSyncPluginClient{deviceStartResponse: response}
			provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
				AuthMethods: []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE},
			})
			if _, err := provider.StartDeviceAuth(context.Background(), ServerConfig{}); err == nil {
				t.Fatal("StartDeviceAuth error = nil")
			}
		})
	}
}

func TestPluginProviderPendingDeviceAuthorizationCarriesRotatedState(t *testing.T) {
	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	client := &fakeWatchSyncPluginClient{devicePollResponse: &pluginv1.WatchSyncDeviceAuthorizationServicePollResponse{
		Status:          pluginv1.WatchSyncDeviceAuthorizationStatus_WATCH_SYNC_DEVICE_AUTHORIZATION_STATUS_PENDING,
		ProviderState:   []byte("rotated-state"),
		PollingInterval: durationpb.New(11 * time.Second),
		ExpiresAt:       timestamppb.New(expiresAt),
	}}
	provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods: []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE},
	})
	original := DeviceAuthSession{
		ID: "auth-1", Provider: testPluginProviderKey, UserID: 7, ProfileID: "profile-1",
		DeviceCode: base64.RawURLEncoding.EncodeToString([]byte("original-state")),
		UserCode:   "ABCD", VerificationURL: "https://provider.example/activate",
		IntervalSeconds: 5, ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	}
	_, err := provider.PollDeviceAuth(context.Background(), ServerConfig{}, original)
	var pending deviceAuthorizationPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("error = %#v, want deviceAuthorizationPendingError", err)
	}
	if pending.session.DeviceCode != base64.RawURLEncoding.EncodeToString([]byte("rotated-state")) ||
		pending.session.IntervalSeconds != 11 || !pending.session.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("pending session = %#v", pending.session)
	}
}

func TestPluginProviderPendingDeviceAuthorizationPreservesStatePresence(t *testing.T) {
	originalState := base64.RawURLEncoding.EncodeToString([]byte("original-state"))
	tests := map[string]struct {
		providerState []byte
		wantState     string
	}{
		"omitted retains state": {providerState: nil, wantState: originalState},
		"explicit empty clears state": {
			providerState: []byte{},
			wantState:     "",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			client := &fakeWatchSyncPluginClient{devicePollResponse: &pluginv1.WatchSyncDeviceAuthorizationServicePollResponse{
				Status:        pluginv1.WatchSyncDeviceAuthorizationStatus_WATCH_SYNC_DEVICE_AUTHORIZATION_STATUS_PENDING,
				ProviderState: test.providerState,
			}}
			provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
				AuthMethods: []pluginv1.WatchSyncAuthMethod{
					pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE,
				},
			})
			original := DeviceAuthSession{
				ID: "auth-1", Provider: testPluginProviderKey, UserID: 7, ProfileID: "profile-1",
				DeviceCode: originalState, UserCode: "ABCD", VerificationURL: "https://provider.example/activate",
				IntervalSeconds: 5, ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
			}
			_, err := provider.PollDeviceAuth(context.Background(), ServerConfig{}, original)
			var pending deviceAuthorizationPendingError
			if !errors.As(err, &pending) {
				t.Fatalf("error = %#v, want deviceAuthorizationPendingError", err)
			}
			if pending.session.DeviceCode != test.wantState {
				t.Fatalf("device state = %q, want %q", pending.session.DeviceCode, test.wantState)
			}
		})
	}
}

func TestPluginProviderPaginatesRemoteStateAndPersistsRotatedCredentials(t *testing.T) {
	now := time.Now().UTC()
	remote := func(key, imdb string) *pluginv1.WatchSyncRemoteState {
		return &pluginv1.WatchSyncRemoteState{
			ProviderItemKey: key,
			Media: &pluginv1.WatchSyncMedia{
				MediaType: pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE,
				Title:     "Movie", ExternalIds: map[string]string{"imdb": imdb},
			},
			Watched: &pluginv1.WatchSyncRemoteWatchedState{PlayCount: 1, LastWatchedAt: timestamppb.New(now)},
		}
	}
	client := &fakeWatchSyncPluginClient{listResponses: []*pluginv1.WatchSyncListRemoteStateResponse{
		{
			Items: []*pluginv1.WatchSyncRemoteState{remote(testOneValue, "tt1")}, NextPageToken: "page-2", CompleteSnapshot: true,
			UpdatedCredentials: &pluginv1.WatchSyncCredentials{
				AccessToken: "rotated", TokenType: testBearerTokenType, Scopes: []string{testHistoryScope},
			},
		},
		{Items: []*pluginv1.WatchSyncRemoteState{remote("two", "tt2")}, NextCursor: "cursor-2", CompleteSnapshot: true},
	}}
	repository := &fakePluginCredentialRepository{}
	provider, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{
			AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
			ImportWatched: true, MaxBatchSize: 25,
		},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) { return client, nil },
		Repository:    repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := provider.FetchWatchedBatch(context.Background(), ServerConfig{}, Connection{
		ID: "connection", Provider: testPluginProviderKey, UserID: 1, ProfileID: "profile",
		AccessToken: "old", SyncCursors: map[string]string{pluginWatchedCursorKey: testCursorOne},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Rows) != 2 || batch.UpdatedCursors[pluginWatchedCursorKey] != "cursor-2" ||
		len(client.listRequests) != 2 || client.listRequests[0].GetCursor() != testCursorOne ||
		client.listRequests[1].GetCursor() != testCursorOne || client.listRequests[1].GetPageToken() != "page-2" {
		t.Fatalf("batch=%#v requests=%#v", batch, client.listRequests)
	}
	if repository.saved.AccessToken != "rotated" || repository.saved.Scopes[0] != testHistoryScope {
		t.Fatalf("persisted credentials = %#v", repository.saved)
	}
}

func TestPluginProviderBoundsRemoteStateTraversalByItemCount(t *testing.T) {
	client := &fakeWatchSyncPluginClient{listResponses: []*pluginv1.WatchSyncListRemoteStateResponse{
		{Items: make([]*pluginv1.WatchSyncRemoteState, maxRemoteStateItems), NextPageToken: "page-2"},
		{Items: []*pluginv1.WatchSyncRemoteState{{}}, NextCursor: "must-not-commit"},
	}}
	provider := testPluginProvider(t, client)
	batch, err := provider.FetchWatchedBatch(context.Background(), ServerConfig{}, Connection{
		SyncCursors: map[string]string{pluginWatchedCursorKey: testCursorOne},
	})
	if err == nil || !strings.Contains(err.Error(), "item limit") {
		t.Fatalf("error = %v, want item limit", err)
	}
	if len(batch.UpdatedCursors) != 0 || len(client.listRequests) != 2 || client.listRequests[1].GetCursor() != testCursorOne {
		t.Fatalf("batch=%#v requests=%#v", batch, client.listRequests)
	}
}

func TestPluginProviderRejectsIncrementalOrderedWatchlist(t *testing.T) {
	client := &fakeWatchSyncPluginClient{listResponse: &pluginv1.WatchSyncListRemoteStateResponse{
		NextCursor:       "must-not-commit",
		CompleteSnapshot: false,
	}}
	provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:            []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ImportWatchlist:        true,
		ProvidesWatchlistOrder: true,
		MaxBatchSize:           25,
	})
	batch, err := provider.FetchWatchlistBatch(context.Background(), ServerConfig{}, Connection{
		SyncCursors: map[string]string{pluginWatchlistCursorKey: testCursorOne},
	})
	if err == nil || !strings.Contains(err.Error(), "incremental traversal for an ordered watchlist") {
		t.Fatalf("error = %v, want ordered watchlist snapshot requirement", err)
	}
	if len(batch.UpdatedCursors) != 0 || len(client.listRequests) != 1 {
		t.Fatalf("batch=%#v requests=%#v", batch, client.listRequests)
	}
}

func TestPluginProviderDecodesKeyOnlyListTombstone(t *testing.T) {
	row, err := remoteFavoriteFromProto(testPluginProviderKey, &pluginv1.WatchSyncRemoteState{
		ProviderItemKey: "remote-1",
	}, &pluginv1.WatchSyncRemoteListState{Removed: true})
	if err != nil {
		t.Fatal(err)
	}
	if !row.Removed || row.ProviderItemKey != "remote-1" || row.Kind != "" {
		t.Fatalf("row = %#v", row)
	}
}

func TestPluginProviderMapsAllCapabilitiesAndListOperations(t *testing.T) {
	descriptor := &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ImportWatched: true, ImportProgress: true, ExportWatched: true, ExportUnwatched: true,
		ImportFavorites: true, ExportFavorites: true, RemoveFavorites: true,
		ImportWatchlist: true, ExportWatchlist: true, RemoveWatchlist: true,
		ProvidesWatchlistOrder: true, ScrobblePlayback: true, MaxBatchSize: 25,
		ImportRatings: true, ExportRatings: true,
	}
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProviderWithDescriptor(t, client, descriptor)
	if provider.Capabilities() != (Capabilities{
		ImportWatched: true, ImportProgress: true, ExportWatched: true, ExportUnwatched: true,
		ImportFavorites: true, ExportFavorites: true, RemoveFavorites: true,
		ImportWatchlist: true, ExportWatchlist: true, RemoveWatchlist: true,
		ProvidesWatchlistOrder: true, ScrobblePlayback: true,
		ImportRatings: true, ExportRatings: true,
	}) {
		t.Fatalf("capabilities = %#v", provider.Capabilities())
	}

	item := LocalFavorite{MediaItemID: testMovieMediaID, ProviderItemKey: "remote-1", Kind: historyimport.KindMovie, IMDbID: "tt1"}
	eventID := pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_ADD_TO_WATCHLIST.String() + ":movie-1"
	client.applyResponse = &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{{
		EventId: eventID, Status: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED,
	}}}
	result, err := provider.ExportWatchlist(context.Background(), ServerConfig{}, Connection{}, []LocalFavorite{item})
	if err != nil {
		t.Fatal(err)
	}
	event := client.applyRequest.GetEvents()[0]
	if len(result.Sent) != 1 || event.GetOperation() != pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_ADD_TO_WATCHLIST ||
		event.GetProviderItemKey() != "remote-1" {
		t.Fatalf("result=%#v event=%#v", result, event)
	}
}

func TestPluginProviderListEventsKeepFailuresAndPresenceAwareOrder(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:         []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ExportWatchlist:     true,
		RemoveWatchlist:     true,
		SupportedMediaTypes: []pluginv1.WatchSyncMediaType{pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE},
		MaxBatchSize:        25,
	})
	items := []LocalFavorite{
		{MediaItemID: testEpisodeMediaID, Kind: historyimport.KindEpisode},
		{MediaItemID: testMovieMediaID, Kind: historyimport.KindMovie},
	}
	addEventID := pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_ADD_TO_WATCHLIST.String() + ":" + testMovieMediaID
	client.applyResponse = &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{{
		EventId: addEventID, Status: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED,
	}}}
	result, err := provider.ExportWatchlist(context.Background(), ServerConfig{}, Connection{}, items)
	if err != nil {
		t.Fatal(err)
	}
	event := client.applyRequest.GetEvents()[0]
	if result.Failed[testEpisodeMediaID] != watchSyncUnsupportedEpisodeMediaMessage ||
		event.ListPosition == nil || event.GetListPosition() != 0 {
		t.Fatalf("result=%#v event=%#v", result, event)
	}

	removeEventID := pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_REMOVE_FROM_WATCHLIST.String() + ":" + testMovieMediaID
	client.applyResponse = &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{{
		EventId: removeEventID, Status: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED,
	}}}
	result, err = provider.RemoveWatchlist(context.Background(), ServerConfig{}, Connection{}, items)
	if err != nil {
		t.Fatal(err)
	}
	event = client.applyRequest.GetEvents()[0]
	if result.Failed[testEpisodeMediaID] != watchSyncUnsupportedEpisodeMediaMessage || event.ListPosition != nil {
		t.Fatalf("result=%#v event=%#v", result, event)
	}
}

func TestPluginProviderRemoveHistoryRecordsUnsupportedMedia(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:         []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ExportUnwatched:     true,
		SupportedMediaTypes: []pluginv1.WatchSyncMediaType{pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE},
	})
	result, err := provider.RemoveHistory(context.Background(), ServerConfig{}, Connection{}, []LocalPlay{{
		HistoryID: testWatchHistoryID, Kind: historyimport.KindEpisode,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed[testWatchHistoryID] != watchSyncUnsupportedEpisodeMediaMessage || client.applyRequest != nil {
		t.Fatalf("result=%#v request=%#v", result, client.applyRequest)
	}
}

func TestPluginProviderForwardsLiveScrobbleLifecycle(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:      []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ScrobblePlayback: true, MaxBatchSize: 25,
	})
	event := ScrobbleEvent{PlaybackSessionID: testPlaybackSessionID, MediaItemID: testMovieMediaID, Kind: historyimport.KindMovie, PositionSeconds: 12.5}
	eventID := "scrobble:" + pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_START.String() + ":" + testPlaybackSessionID
	client.applyResponse = &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{{
		EventId: eventID, Status: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED,
	}}}
	if err := provider.Start(context.Background(), ServerConfig{}, Connection{}, event); err != nil {
		t.Fatal(err)
	}
	if got := client.applyRequest.GetEvents()[0]; got.GetOperation() != pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_START || got.GetPositionSeconds() != 12.5 {
		t.Fatalf("scrobble event = %#v", got)
	}
}

func TestPluginProviderForwardsAuthoritativeScrobbleCompletion(t *testing.T) {
	completed := watchEventFromScrobble(ScrobbleEvent{
		PlaybackSessionID: testPlaybackSessionID,
		MediaItemID:       testMovieMediaID,
		Kind:              historyimport.KindMovie,
		PositionSeconds:   90,
		DurationSeconds:   100,
		Completed:         true,
	}, pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP)
	if !completed.GetCompleted() {
		t.Fatal("completed event = false, want true")
	}

	incomplete := watchEventFromScrobble(ScrobbleEvent{
		PlaybackSessionID: testPlaybackSessionID,
		MediaItemID:       testMovieMediaID,
		Kind:              historyimport.KindMovie,
		PositionSeconds:   10,
		DurationSeconds:   100,
		Completed:         false,
	}, pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP)
	if incomplete.GetCompleted() {
		t.Fatal("incomplete event = true, want false")
	}
}

const testSeriesMediaID = "series-1"

func ratingTestDescriptor(media ...pluginv1.WatchSyncMediaType) *pluginv1.WatchSyncProviderDescriptor {
	return &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:         []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ImportRatings:       true,
		ExportRatings:       true,
		SupportedMediaTypes: media,
		MaxBatchSize:        25,
	}
}

func remoteRatingState(key string, mediaType pluginv1.WatchSyncMediaType, imdbID string, rating int32, ratedAt *timestamppb.Timestamp) *pluginv1.WatchSyncRemoteState {
	return &pluginv1.WatchSyncRemoteState{
		ProviderItemKey: key,
		Media:           &pluginv1.WatchSyncMedia{MediaType: mediaType, Title: "Title", ExternalIds: map[string]string{"imdb": imdbID}},
		Rating:          &pluginv1.WatchSyncRemoteRatingState{Rating: rating, RatedAt: ratedAt},
	}
}

func TestPluginProviderDecodesRatingSnapshot(t *testing.T) {
	ratedAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	client := &fakeWatchSyncPluginClient{listResponse: &pluginv1.WatchSyncListRemoteStateResponse{
		CompleteSnapshot: true,
		NextCursor:       "cursor-2",
		Items: []*pluginv1.WatchSyncRemoteState{
			remoteRatingState("m1", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE, "tt1", 8, timestamppb.New(ratedAt)),
			remoteRatingState("s1", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES, "tt2", 7, nil),
			// Silo does not sync episode ratings.
			remoteRatingState("e1", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE, "tt3", 9, nil),
		},
	}}
	provider := testPluginProviderWithDescriptor(t, client, ratingTestDescriptor(
		pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE,
		pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE,
		pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES,
	))
	batch, err := provider.FetchRatings(context.Background(), ServerConfig{}, Connection{
		SyncCursors: map[string]string{pluginRatingsCursorKey: testCursorOne},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.listRequests) != 1 || client.listRequests[0].GetCursor() != testCursorOne ||
		len(client.listRequests[0].GetStateKinds()) != 1 ||
		client.listRequests[0].GetStateKinds()[0] != pluginv1.WatchSyncRemoteStateKind_WATCH_SYNC_REMOTE_STATE_KIND_RATING {
		t.Fatalf("requests = %#v", client.listRequests)
	}
	if !slices.Equal(batch.SnapshotKinds, []string{historyimport.KindMovie, historyimport.KindSeries}) ||
		batch.UpdatedCursors[pluginRatingsCursorKey] != "cursor-2" || len(batch.Warnings) != 0 || len(batch.Rows) != 2 {
		t.Fatalf("batch = %#v", batch)
	}
	movie, series := batch.Rows[0], batch.Rows[1]
	if movie.Provider != testPluginProviderKey || movie.ProviderItemKey != "m1" || movie.Kind != historyimport.KindMovie ||
		movie.IMDbID != "tt1" || movie.Rating != 8 || !movie.RatedAt.Equal(ratedAt) || movie.Removed {
		t.Fatalf("movie row = %#v", movie)
	}
	if series.ProviderItemKey != "s1" || series.Kind != historyimport.KindSeries || series.IMDbID != "tt2" ||
		series.Rating != 7 || !series.RatedAt.IsZero() {
		t.Fatalf("series row = %#v", series)
	}

	// The cursor resets with the agreed ratings on an account change.
	kept := withoutRatingCursors(map[string]string{pluginRatingsCursorKey: "a", pluginWatchedCursorKey: "b"})
	if _, ok := kept[pluginRatingsCursorKey]; ok || kept[pluginWatchedCursorKey] != "b" {
		t.Fatalf("cursors kept after account change = %#v", kept)
	}
}

func TestPluginProviderRatingSnapshotKindsFollowSupportedMedia(t *testing.T) {
	for _, tc := range []struct {
		name  string
		media []pluginv1.WatchSyncMediaType
		want  []string
	}{
		{name: "default media", want: []string{historyimport.KindMovie}},
		{name: "series only", media: []pluginv1.WatchSyncMediaType{pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES}, want: []string{historyimport.KindSeries}},
		{name: "episodes only", media: []pluginv1.WatchSyncMediaType{pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeWatchSyncPluginClient{listResponse: &pluginv1.WatchSyncListRemoteStateResponse{CompleteSnapshot: true}}
			provider := testPluginProviderWithDescriptor(t, client, ratingTestDescriptor(tc.media...))
			batch, err := provider.FetchRatings(context.Background(), ServerConfig{}, Connection{})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(batch.SnapshotKinds, tc.want) {
				t.Fatalf("SnapshotKinds = %#v, want %#v", batch.SnapshotKinds, tc.want)
			}
		})
	}
}

func TestPluginProviderPaginatesIncrementalRatingsWithTombstone(t *testing.T) {
	client := &fakeWatchSyncPluginClient{listResponses: []*pluginv1.WatchSyncListRemoteStateResponse{
		{
			Items:         []*pluginv1.WatchSyncRemoteState{remoteRatingState("m1", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE, "tt1", 6, nil)},
			NextPageToken: "page-2",
		},
		{
			Items:      []*pluginv1.WatchSyncRemoteState{{ProviderItemKey: "m2", Rating: &pluginv1.WatchSyncRemoteRatingState{Removed: true}}},
			NextCursor: "cursor-2",
		},
	}}
	provider := testPluginProviderWithDescriptor(t, client, ratingTestDescriptor(
		pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE,
		pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES,
	))
	batch, err := provider.FetchRatings(context.Background(), ServerConfig{}, Connection{
		SyncCursors: map[string]string{pluginRatingsCursorKey: testCursorOne},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.listRequests) != 2 || client.listRequests[1].GetCursor() != testCursorOne ||
		client.listRequests[1].GetPageToken() != "page-2" {
		t.Fatalf("requests = %#v", client.listRequests)
	}
	// An incremental read is complete for no kind, so absent ratings stay unknown.
	if len(batch.SnapshotKinds) != 0 || batch.UpdatedCursors[pluginRatingsCursorKey] != "cursor-2" || len(batch.Rows) != 2 {
		t.Fatalf("batch = %#v", batch)
	}
	tombstone := batch.Rows[1]
	if !tombstone.Removed || tombstone.ProviderItemKey != "m2" || tombstone.Kind != "" || tombstone.Rating != 0 {
		t.Fatalf("tombstone = %#v", tombstone)
	}
}

func TestPluginProviderWarnsAndSkipsInvalidRatings(t *testing.T) {
	client := &fakeWatchSyncPluginClient{listResponse: &pluginv1.WatchSyncListRemoteStateResponse{
		CompleteSnapshot: true,
		Items: []*pluginv1.WatchSyncRemoteState{
			remoteRatingState("zero", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE, "tt0", 0, nil),
			remoteRatingState("eleven", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE, "tt11", 11, nil),
			remoteRatingState("ten", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE, "tt10", 10, nil),
			{Rating: &pluginv1.WatchSyncRemoteRatingState{Removed: true}},
		},
	}}
	provider := testPluginProviderWithDescriptor(t, client, ratingTestDescriptor())
	batch, err := provider.FetchRatings(context.Background(), ServerConfig{}, Connection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Rows) != 1 || batch.Rows[0].Rating != 10 {
		t.Fatalf("rows = %#v", batch.Rows)
	}
	want := []string{
		"watch sync plugin returned an out-of-range rating 0",
		"watch sync plugin returned an out-of-range rating 11",
		"watch sync plugin returned a rating tombstone without provider identity",
		watchSyncIncompleteRatingSnapshotWarning,
	}
	if !slices.Equal(batch.Warnings, want) {
		t.Fatalf("warnings = %#v", batch.Warnings)
	}
}

// A complete snapshot that drops an unreadable rating cannot say which kind the
// row was, so it covers no kind and absent ratings stay unknown. A dropped
// tombstone reads as absent, which the snapshot already treats as removed.
func TestPluginProviderRatingSnapshotWithUnreadableRatingCoversNoKind(t *testing.T) {
	valid := remoteRatingState("m1", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE, "tt1", 8, nil)
	for _, tc := range []struct {
		name      string
		complete  bool
		bad       *pluginv1.WatchSyncRemoteState
		wantKinds []string
		wantWarn  []string
	}{
		{
			name: "bad media", complete: true,
			bad:      remoteRatingState("x", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_UNSPECIFIED, "tt2", 7, nil),
			wantWarn: []string{"watch sync plugin returned unsupported remote media", watchSyncIncompleteRatingSnapshotWarning},
		},
		{
			name: "missing key", complete: true,
			bad:      remoteRatingState("", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES, "tt2", 7, nil),
			wantWarn: []string{"watch sync plugin returned remote state without identity", watchSyncIncompleteRatingSnapshotWarning},
		},
		{
			name: "out of range", complete: true,
			bad:      remoteRatingState("x", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE, "tt2", 11, nil),
			wantWarn: []string{"watch sync plugin returned an out-of-range rating 11", watchSyncIncompleteRatingSnapshotWarning},
		},
		{
			name: "missing rating payload", complete: true,
			bad:      &pluginv1.WatchSyncRemoteState{ProviderItemKey: "x"},
			wantWarn: []string{"watch sync plugin returned remote state without a rating", watchSyncIncompleteRatingSnapshotWarning},
		},
		{
			name: "nil item", complete: true,
			bad:      nil,
			wantWarn: []string{"watch sync plugin returned remote state without a rating", watchSyncIncompleteRatingSnapshotWarning},
		},
		{
			name: "bad tombstone keeps the snapshot", complete: true,
			bad:       &pluginv1.WatchSyncRemoteState{Rating: &pluginv1.WatchSyncRemoteRatingState{Removed: true}},
			wantKinds: []string{historyimport.KindMovie, historyimport.KindSeries},
			wantWarn:  []string{"watch sync plugin returned a rating tombstone without provider identity"},
		},
		{
			name:     "incremental read",
			bad:      remoteRatingState("", pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE, "tt2", 7, nil),
			wantWarn: []string{"watch sync plugin returned remote state without identity"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeWatchSyncPluginClient{listResponse: &pluginv1.WatchSyncListRemoteStateResponse{
				CompleteSnapshot: tc.complete,
				Items:            []*pluginv1.WatchSyncRemoteState{valid, tc.bad},
			}}
			provider := testPluginProviderWithDescriptor(t, client, ratingTestDescriptor(
				pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE,
				pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES,
			))
			batch, err := provider.FetchRatings(context.Background(), ServerConfig{}, Connection{})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(batch.SnapshotKinds, tc.wantKinds) {
				t.Fatalf("SnapshotKinds = %#v, want %#v", batch.SnapshotKinds, tc.wantKinds)
			}
			if !slices.Equal(batch.Warnings, tc.wantWarn) {
				t.Fatalf("warnings = %#v, want %#v", batch.Warnings, tc.wantWarn)
			}
			if len(batch.Rows) != 1 || batch.Rows[0].ProviderItemKey != "m1" {
				t.Fatalf("rows = %#v", batch.Rows)
			}
		})
	}
}

func TestPluginProviderMapsSeriesMediaBothWays(t *testing.T) {
	series := LocalFavorite{
		MediaItemID: testSeriesMediaID, Kind: historyimport.KindSeries, Title: "Show", Year: 2020,
		IMDbID: "tt9", TMDBID: "99", TVDBID: "77", ProviderItemKey: "imdb:tt9",
	}
	media := mediaFromLocalFavorite(series)
	if media.GetMediaType() != pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES ||
		media.GetTitle() != "Show" || media.GetYear() != 2020 ||
		media.GetExternalIds()["imdb"] != "tt9" || media.GetExternalIds()["tmdb"] != "99" || media.GetExternalIds()["tvdb"] != "77" ||
		len(media.GetSeriesExternalIds()) != 0 || media.GetSeasonNumber() != 0 || media.GetEpisodeNumber() != 0 {
		t.Fatalf("series media = %#v", media)
	}

	client := &fakeWatchSyncPluginClient{listResponses: []*pluginv1.WatchSyncListRemoteStateResponse{
		{CompleteSnapshot: true, Items: []*pluginv1.WatchSyncRemoteState{{ProviderItemKey: "s1", Media: media, Favorite: &pluginv1.WatchSyncRemoteListState{}}}},
		{Items: []*pluginv1.WatchSyncRemoteState{{ProviderItemKey: "s1", Media: media, Watched: &pluginv1.WatchSyncRemoteWatchedState{PlayCount: 1}}}},
	}}
	provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:     []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ImportFavorites: true, ImportWatched: true, MaxBatchSize: 25,
		SupportedMediaTypes: []pluginv1.WatchSyncMediaType{pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES},
	})
	favorites, err := provider.FetchFavoritesBatch(context.Background(), ServerConfig{}, Connection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(favorites.Rows) != 1 || favorites.Rows[0].Kind != historyimport.KindSeries || favorites.Rows[0].IMDbID != "tt9" ||
		favorites.Rows[0].TVDBID != "77" || favorites.Rows[0].SeriesIMDbID != "" {
		t.Fatalf("favorites = %#v", favorites)
	}
	// SERIES is not defined for watched state; a series-level row would
	// otherwise mark every local episode watched.
	watched, err := provider.FetchWatchedBatch(context.Background(), ServerConfig{}, Connection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(watched.Rows) != 0 || len(watched.Warnings) != 1 || !strings.Contains(watched.Warnings[0], "series-level watched") {
		t.Fatalf("watched = %#v", watched)
	}
}

func TestPluginProviderSendsSeriesListEventsOnlyWhenSupported(t *testing.T) {
	series := LocalFavorite{MediaItemID: testSeriesMediaID, Kind: historyimport.KindSeries, IMDbID: "tt9", ProviderItemKey: "imdb:tt9"}
	client := &fakeWatchSyncPluginClient{applyStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED}
	provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:     []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ExportFavorites: true, MaxBatchSize: 25,
		SupportedMediaTypes: []pluginv1.WatchSyncMediaType{
			pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE,
			pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES,
		},
	})
	result, err := provider.ExportFavorites(context.Background(), ServerConfig{}, Connection{}, []LocalFavorite{series})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sent) != 1 || client.applyRequest.GetEvents()[0].GetMedia().GetMediaType() != pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES {
		t.Fatalf("result=%#v request=%#v", result, client.applyRequest)
	}

	// A plugin that does not list SERIES still never receives series items.
	client = &fakeWatchSyncPluginClient{}
	result, err = testPluginProvider(t, client).ExportFavorites(context.Background(), ServerConfig{}, Connection{}, []LocalFavorite{series})
	if err != nil {
		t.Fatal(err)
	}
	if client.applyRequest != nil || result.Failed[testSeriesMediaID] != watchSyncUnsupportedSeriesMediaMessage {
		t.Fatalf("result=%#v request=%#v", result, client.applyRequest)
	}
}

func TestPluginProviderRatingEventsCarryValueAndDistinctIDs(t *testing.T) {
	client := &fakeWatchSyncPluginClient{applyStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED}
	provider := testPluginProviderWithDescriptor(t, client, ratingTestDescriptor(
		pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE,
		pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES,
	))
	movie := LocalFavorite{MediaItemID: testMovieMediaID, Kind: historyimport.KindMovie, IMDbID: "tt1", ProviderItemKey: "imdb:tt1"}
	ratedAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	set := func(rating int, at time.Time) *pluginv1.WatchSyncEvent {
		t.Helper()
		result, err := provider.ExportRatings(context.Background(), ServerConfig{}, Connection{}, []LocalRating{{LocalFavorite: movie, Rating: rating, RatedAt: at}})
		if err != nil || len(result.Sent) != 1 || result.Sent[0] != testMovieMediaID {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		return client.applyRequest.GetEvents()[0]
	}
	remove := func() *pluginv1.WatchSyncEvent {
		t.Helper()
		result, err := provider.RemoveRatings(context.Background(), ServerConfig{}, Connection{}, []LocalFavorite{movie})
		if err != nil || len(result.Sent) != 1 || result.Sent[0] != testMovieMediaID {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		return client.applyRequest.GetEvents()[0]
	}

	event := set(8, ratedAt)
	wantID := fmt.Sprintf("%s:%s:8:%d", pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SET_RATING, testMovieMediaID, ratedAt.UnixNano())
	if event.GetEventId() != wantID || event.GetOperation() != pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SET_RATING ||
		event.GetRating() != 8 || !event.GetOccurredAt().AsTime().Equal(ratedAt) || event.GetProviderItemKey() != "imdb:tt1" ||
		event.GetMedia().GetMediaType() != pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE || event.GetMedia().GetExternalIds()["imdb"] != "tt1" {
		t.Fatalf("set event = %#v", event)
	}
	if retry := set(8, ratedAt); retry.GetEventId() != event.GetEventId() {
		t.Fatalf("retry event ID = %q, want %q", retry.GetEventId(), event.GetEventId())
	}
	if rerated := set(8, ratedAt.Add(time.Hour)); rerated.GetEventId() == event.GetEventId() {
		t.Fatal("a later re-rate to the same value reused the event ID")
	}
	if changed := set(6, ratedAt); changed.GetEventId() == event.GetEventId() {
		t.Fatal("a different rating reused the event ID")
	}

	removedAt := ratedAt.Add(2 * time.Hour)
	provider.now = func() time.Time { return removedAt }
	removal := remove()
	wantID = fmt.Sprintf("%s:%s:%d", pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_REMOVE_RATING, testMovieMediaID, removedAt.UnixNano())
	if removal.GetEventId() != wantID || removal.GetOperation() != pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_REMOVE_RATING ||
		removal.GetRating() != 0 || removal.GetOccurredAt() != nil || removal.GetProviderItemKey() != "imdb:tt1" {
		t.Fatalf("remove event = %#v", removal)
	}
	provider.now = func() time.Time { return removedAt.Add(time.Hour) }
	if later := remove(); later.GetEventId() == removal.GetEventId() {
		t.Fatal("a later removal reused the event ID")
	}
}

// Clearing an absent rating must answer APPLIED or NO_CHANGE, so a REJECTED
// removal is a failure the service retries. A rejected SET_RATING, like a
// rejected list or watched event, still means the plugin has no such title.
func TestPluginProviderRejectedRatingRemovalFails(t *testing.T) {
	client := &fakeWatchSyncPluginClient{
		applyStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED,
		applyFault:  &pluginv1.WatchSyncFault{SafeMessage: "rating removal refused for " + testSecretValue},
	}
	provider := testPluginProviderWithDescriptor(t, client, ratingTestDescriptor(pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE))
	conn := Connection{AccessToken: testSecretValue}
	movie := LocalFavorite{MediaItemID: testMovieMediaID, Kind: historyimport.KindMovie, IMDbID: "tt1", ProviderItemKey: "imdb:tt1"}

	removed, err := provider.RemoveRatings(context.Background(), ServerConfig{}, conn, []LocalFavorite{movie})
	if err != nil {
		t.Fatal(err)
	}
	message := removed.Failed[testMovieMediaID]
	if len(removed.Sent) != 0 || len(removed.NotFound) != 0 || !strings.HasPrefix(message, "rating removal refused for ") ||
		strings.Contains(message, testSecretValue) {
		t.Fatalf("remove result = %#v", removed)
	}

	set, err := provider.ExportRatings(context.Background(), ServerConfig{}, conn, []LocalRating{{LocalFavorite: movie, Rating: 8}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Failed) != 0 || !slices.Equal(set.NotFound, []string{testMovieMediaID}) {
		t.Fatalf("set result = %#v", set)
	}

	listRemoved, err := provider.RemoveFavorites(context.Background(), ServerConfig{}, conn, []LocalFavorite{movie})
	if err != nil {
		t.Fatal(err)
	}
	if len(listRemoved.Failed) != 0 || !slices.Equal(listRemoved.NotFound, []string{testMovieMediaID}) {
		t.Fatalf("favorite removal result = %#v", listRemoved)
	}
}

func TestPluginProviderRatingEventsFailUnsupportedMedia(t *testing.T) {
	client := &fakeWatchSyncPluginClient{applyStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED}
	provider := testPluginProviderWithDescriptor(t, client, ratingTestDescriptor(pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE))
	series := LocalFavorite{MediaItemID: testSeriesMediaID, Kind: historyimport.KindSeries, IMDbID: "tt9"}
	result, err := provider.ExportRatings(context.Background(), ServerConfig{}, Connection{}, []LocalRating{
		{LocalFavorite: LocalFavorite{MediaItemID: testMovieMediaID, Kind: historyimport.KindMovie, IMDbID: "tt1"}, Rating: 8},
		{LocalFavorite: series, Rating: 6},
		{LocalFavorite: LocalFavorite{MediaItemID: "movie-2", Kind: historyimport.KindMovie, IMDbID: "tt2"}, Rating: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.applyRequest.GetEvents()) != 1 || len(result.Sent) != 1 || result.Sent[0] != testMovieMediaID ||
		result.Failed[testSeriesMediaID] != watchSyncUnsupportedSeriesMediaMessage || result.Failed["movie-2"] == "" {
		t.Fatalf("result=%#v request=%#v", result, client.applyRequest)
	}

	client.applyRequest = nil
	result, err = provider.RemoveRatings(context.Background(), ServerConfig{}, Connection{}, []LocalFavorite{series})
	if err != nil {
		t.Fatal(err)
	}
	if client.applyRequest != nil || result.Failed[testSeriesMediaID] != watchSyncUnsupportedSeriesMediaMessage {
		t.Fatalf("result=%#v request=%#v", result, client.applyRequest)
	}
}

func TestPluginProviderRatingCapabilitiesSurviveCapabilityStorage(t *testing.T) {
	records, err := hostplugins.CapabilityRecordsFromManifest(&pluginv1.PluginManifest{Capabilities: []*pluginv1.CapabilityDescriptor{{
		Type: "watch_sync_provider.v1", Id: testPluginCapabilityID, DisplayName: "AniList",
		WatchSyncProvider: ratingTestDescriptor(
			pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE,
			pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES,
		),
	}}})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	// Capability metadata is stored as JSON and decoded on provider reload.
	stored, err := json.Marshal(records[0].Metadata)
	if err != nil {
		t.Fatal(err)
	}
	records[0].Metadata = nil
	if err := json.Unmarshal(stored, &records[0].Metadata); err != nil {
		t.Fatal(err)
	}
	descriptor, err := hostplugins.DecodeCapability(&records[0])
	if err != nil {
		t.Fatal(err)
	}
	provider := testPluginProviderWithDescriptor(t, &fakeWatchSyncPluginClient{}, descriptor.GetWatchSyncProvider())
	capabilities := provider.Capabilities()
	if !capabilities.ImportRatings || !capabilities.ExportRatings ||
		!provider.supportsMedia(pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES) {
		t.Fatalf("capabilities=%#v descriptor=%#v", capabilities, descriptor.GetWatchSyncProvider())
	}
}

func TestSupportedWatchSyncMediaTypesIgnoresTypesFromNewerSDKs(t *testing.T) {
	future := pluginv1.WatchSyncMediaType(99)
	supported, err := supportedWatchSyncMediaTypes(&pluginv1.WatchSyncProviderDescriptor{
		SupportedMediaTypes: []pluginv1.WatchSyncMediaType{pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE, future},
	})
	if err != nil {
		t.Fatalf("a future media type must not reject the plugin: %v", err)
	}
	if _, ok := supported[future]; ok || len(supported) != 1 {
		t.Fatalf("supported = %v, want only movie", supported)
	}
	if _, err := supportedWatchSyncMediaTypes(&pluginv1.WatchSyncProviderDescriptor{
		SupportedMediaTypes: []pluginv1.WatchSyncMediaType{future},
	}); err == nil {
		t.Fatal("a plugin with no media type this server supports must be rejected")
	}
	if _, err := supportedWatchSyncMediaTypes(&pluginv1.WatchSyncProviderDescriptor{
		SupportedMediaTypes: []pluginv1.WatchSyncMediaType{pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_UNSPECIFIED},
	}); err == nil {
		t.Fatal("an unspecified media type must still be rejected")
	}
}

func TestPluginProviderSyncsRatingKindFollowsSupportedMedia(t *testing.T) {
	supported, err := supportedWatchSyncMediaTypes(&pluginv1.WatchSyncProviderDescriptor{
		SupportedMediaTypes: []pluginv1.WatchSyncMediaType{pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &PluginProvider{supportedMedia: supported}
	if !provider.SyncsRatingKind(historyimport.KindMovie) || provider.SyncsRatingKind(historyimport.KindSeries) {
		t.Fatal("a movie-only plugin must rate movies and skip series")
	}
}
