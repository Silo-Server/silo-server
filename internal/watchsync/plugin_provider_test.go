package watchsync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"google.golang.org/protobuf/types/known/durationpb"
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
	exchangeResponse *pluginv1.WatchSyncCredentialResponse
	applyResponse    *pluginv1.WatchSyncApplyEventsResponse
	applyErr         error
	applyRequest     *pluginv1.WatchSyncApplyEventsRequest
	refreshRequest   *pluginv1.WatchSyncRefreshCredentialsRequest
	accountRequest   *pluginv1.WatchSyncGetAccountRequest
}

func (f *fakeWatchSyncPluginClient) ExchangeAPIKey(_ context.Context, _ *pluginv1.WatchSyncExchangeAPIKeyRequest) (*pluginv1.WatchSyncCredentialResponse, error) {
	return f.exchangeResponse, nil
}
func (f *fakeWatchSyncPluginClient) RefreshCredentials(_ context.Context, req *pluginv1.WatchSyncRefreshCredentialsRequest) (*pluginv1.WatchSyncCredentialResponse, error) {
	f.refreshRequest = req
	return &pluginv1.WatchSyncCredentialResponse{}, nil
}
func (f *fakeWatchSyncPluginClient) GetAccount(_ context.Context, req *pluginv1.WatchSyncGetAccountRequest) (*pluginv1.WatchSyncGetAccountResponse, error) {
	f.accountRequest = req
	return &pluginv1.WatchSyncGetAccountResponse{}, nil
}
func (f *fakeWatchSyncPluginClient) ApplyEvents(_ context.Context, req *pluginv1.WatchSyncApplyEventsRequest) (*pluginv1.WatchSyncApplyEventsResponse, error) {
	f.applyRequest = req
	return f.applyResponse, f.applyErr
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
}

func TestPluginProviderConnectsAPIKeyWithoutPersistingInPluginConfig(t *testing.T) {
	client := &fakeWatchSyncPluginClient{exchangeResponse: &pluginv1.WatchSyncCredentialResponse{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: "validated-token", TokenType: "Bearer"},
		Account:     &pluginv1.WatchSyncAccount{ExternalSubject: "7", Username: "alice"},
	}}
	provider := testPluginProvider(t, client)
	if provider.Key() != testPluginProviderKey {
		t.Fatalf("provider key = %q", provider.Key())
	}
	tokens, account, err := provider.ConnectWithAPIKey(context.Background(), "input-token")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "validated-token" || account.ID != "7" || account.Username != "alice" {
		t.Fatalf("tokens=%#v account=%#v", tokens, account)
	}
}

func TestPluginProviderExportsRichEpisodeIdentity(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	client.applyResponse = &pluginv1.WatchSyncApplyEventsResponse{Results: []*pluginv1.WatchSyncApplyResult{{
		EventId: testWatchHistoryID, Status: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED,
	}}}
	provider := testPluginProvider(t, client)
	result, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{AccessToken: "secret"}, []LocalPlay{{
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
	if client.applyRequest.GetContext().GetCredentials().GetAccessToken() != "secret" ||
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
		{HistoryID: testWatchHistoryID},
		{HistoryID: testSecondHistoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.applyRequest.GetEvents()) != 2 || len(result.Sent) != 1 || len(result.NotFound) != 1 {
		t.Fatalf("request=%#v result=%#v", client.applyRequest, result)
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
		{HistoryID: testWatchHistoryID},
		{HistoryID: testSecondHistoryID},
		{HistoryID: "history-3"},
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
	_, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{}, []LocalPlay{{HistoryID: testWatchHistoryID}})
	if !isRetryableProviderError(err) || strings.Contains(err.Error(), "secret") {
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
	result, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{}, []LocalPlay{{HistoryID: testWatchHistoryID}})
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
	_, err := provider.ExportHistory(context.Background(), ServerConfig{}, Connection{}, []LocalPlay{{HistoryID: "h", ProviderItemKey: "p"}})
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
