package watchsync

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/historyimport"
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
	}
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProviderWithDescriptor(t, client, descriptor)
	if provider.Capabilities() != (Capabilities{
		ImportWatched: true, ImportProgress: true, ExportWatched: true, ExportUnwatched: true,
		ImportFavorites: true, ExportFavorites: true, RemoveFavorites: true,
		ImportWatchlist: true, ExportWatchlist: true, RemoveWatchlist: true,
		ProvidesWatchlistOrder: true, ScrobblePlayback: true,
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
