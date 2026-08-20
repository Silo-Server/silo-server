package watchsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicconfig "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/config"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	hostplugins "github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type WatchSyncPluginClient interface {
	ExchangeAPIKey(context.Context, *pluginv1.WatchSyncExchangeAPIKeyRequest) (*pluginv1.WatchSyncCredentialResponse, error)
	StartDeviceAuthorization(context.Context, *pluginv1.WatchSyncDeviceAuthorizationServiceStartRequest) (*pluginv1.WatchSyncDeviceAuthorizationServiceStartResponse, error)
	PollDeviceAuthorization(context.Context, *pluginv1.WatchSyncDeviceAuthorizationServicePollRequest) (*pluginv1.WatchSyncDeviceAuthorizationServicePollResponse, error)
	RefreshCredentials(context.Context, *pluginv1.WatchSyncRefreshCredentialsRequest) (*pluginv1.WatchSyncCredentialResponse, error)
	GetAccount(context.Context, *pluginv1.WatchSyncGetAccountRequest) (*pluginv1.WatchSyncGetAccountResponse, error)
	ApplyEvents(context.Context, *pluginv1.WatchSyncApplyEventsRequest) (*pluginv1.WatchSyncApplyEventsResponse, error)
	ListRemoteState(context.Context, *pluginv1.WatchSyncListRemoteStateRequest) (*pluginv1.WatchSyncListRemoteStateResponse, error)
}

type WatchSyncPluginClientResolver func(context.Context, int, string) (WatchSyncPluginClient, error)
type WatchSyncPluginConfigResolver func(context.Context, int) (*pluginv1.WatchSyncProviderConfig, error)

type PluginCredentialRepository interface {
	UpsertConnection(context.Context, Connection) (Connection, error)
}

type PluginProviderOptions struct {
	InstallationID         int
	ProviderKey            string
	CapabilityID           string
	DisplayName            string
	Descriptor             *pluginv1.WatchSyncProviderDescriptor
	ConnectionConfigSchema []*pluginv1.ConfigSchema
	ResolveClient          WatchSyncPluginClientResolver
	ResolveConfig          WatchSyncPluginConfigResolver
	Repository             PluginCredentialRepository
}

type PluginProvider struct {
	installationID         int
	providerKey            string
	capabilityID           string
	displayName            string
	descriptor             *pluginv1.WatchSyncProviderDescriptor
	connectionConfigSchema []*pluginv1.ConfigSchema
	authMethod             string
	supportedMedia         map[pluginv1.WatchSyncMediaType]struct{}
	resolveClient          WatchSyncPluginClientResolver
	resolveConfig          WatchSyncPluginConfigResolver
	repository             PluginCredentialRepository
}

const (
	watchSyncUnsupportedMovieMediaMessage   = "watch sync plugin does not support movie media"
	watchSyncUnsupportedEpisodeMediaMessage = "watch sync plugin does not support episode media"
	watchSyncUnsupportedMediaMessage        = "watch sync plugin does not support this media type"
	watchSyncJSONSchemaNumberType           = "number"
	watchSyncJSONSchemaBooleanType          = "boolean"
)

func NewPluginProvider(options PluginProviderOptions) (*PluginProvider, error) {
	if options.InstallationID <= 0 || strings.TrimSpace(options.CapabilityID) == "" {
		return nil, fmt.Errorf("watch sync plugin installation and capability are required")
	}
	if strings.TrimSpace(options.ProviderKey) == "" {
		return nil, fmt.Errorf("watch sync plugin provider key is required")
	}
	if options.Descriptor == nil {
		return nil, fmt.Errorf("watch sync plugin descriptor is required")
	}
	authMethod, err := supportedWatchSyncAuthMethod(options.Descriptor)
	if err != nil {
		return nil, fmt.Errorf("watch sync plugin %q %w", options.ProviderKey, err)
	}
	supportedMedia, err := supportedWatchSyncMediaTypes(options.Descriptor)
	if err != nil {
		return nil, fmt.Errorf("watch sync plugin %q %w", options.ProviderKey, err)
	}
	if err := validateWatchSyncConnectionConfigSchemas(options.ConnectionConfigSchema); err != nil {
		return nil, fmt.Errorf("watch sync plugin %q %w", options.ProviderKey, err)
	}
	if authMethod != AuthMethodAPIKey && hasWatchSyncConnectionConfigSchema(options.ConnectionConfigSchema) {
		return nil, fmt.Errorf("watch sync plugin %q connection config requires API-key authentication", options.ProviderKey)
	}
	if options.ResolveClient == nil {
		return nil, fmt.Errorf("watch sync plugin client resolver is required")
	}
	return &PluginProvider{
		installationID:         options.InstallationID,
		providerKey:            options.ProviderKey,
		capabilityID:           options.CapabilityID,
		displayName:            options.DisplayName,
		descriptor:             options.Descriptor,
		connectionConfigSchema: append([]*pluginv1.ConfigSchema(nil), options.ConnectionConfigSchema...),
		authMethod:             authMethod,
		supportedMedia:         supportedMedia,
		resolveClient:          options.ResolveClient,
		resolveConfig:          options.ResolveConfig,
		repository:             options.Repository,
	}, nil
}

func hasWatchSyncConnectionConfigSchema(schemas []*pluginv1.ConfigSchema) bool {
	for _, schema := range schemas {
		if schema != nil {
			return true
		}
	}
	return false
}

func (p *PluginProvider) Key() string { return p.providerKey }

func (p *PluginProvider) DisplayName() string {
	if strings.TrimSpace(p.displayName) != "" {
		return p.displayName
	}
	return p.capabilityID
}

func (p *PluginProvider) ProviderSource() string { return providerSourcePlugin }

// HistorySource is provider-specific so importing from one plugin suppresses
// only the echo back to that same connection. A shared generic source would
// also suppress legitimate Floppy-to-Trakt (or other cross-provider) sync.
func (p *PluginProvider) HistorySource() userstore.WatchHistorySource {
	return userstore.WatchHistorySource(p.providerKey)
}

func (p *PluginProvider) AuthMethod() string { return p.authMethod }

func (p *PluginProvider) ConnectionConfigSchema() []hostplugins.ConfigSchemaView {
	return hostplugins.ConfigSchemaViews(p.connectionConfigSchema)
}

func (p *PluginProvider) usesHostPluginConfig() {}

func (p *PluginProvider) authoritativeRefreshProvider() {}

func (p *PluginProvider) ExportBatchSize() int {
	if size := int(p.descriptor.GetMaxBatchSize()); size > 0 {
		return size
	}
	return 1
}

func (p *PluginProvider) Capabilities() Capabilities {
	return Capabilities{
		ImportWatched:          p.descriptor.GetImportWatched(),
		ImportProgress:         p.descriptor.GetImportProgress(),
		ExportWatched:          p.descriptor.GetExportWatched(),
		ExportUnwatched:        p.descriptor.GetExportUnwatched(),
		ImportFavorites:        p.descriptor.GetImportFavorites(),
		ExportFavorites:        p.descriptor.GetExportFavorites(),
		RemoveFavorites:        p.descriptor.GetRemoveFavorites(),
		ImportWatchlist:        p.descriptor.GetImportWatchlist(),
		ExportWatchlist:        p.descriptor.GetExportWatchlist(),
		RemoveWatchlist:        p.descriptor.GetRemoveWatchlist(),
		ProvidesWatchlistOrder: p.descriptor.GetProvidesWatchlistOrder(),
		ScrobblePlayback:       p.descriptor.GetScrobblePlayback(),
	}
}

func (p *PluginProvider) ConnectWithAPIKey(ctx context.Context, apiKey string) (TokenSet, ProviderAccount, error) {
	return p.ConnectWithAPIKeyConfig(ctx, apiKey, nil)
}

func (p *PluginProvider) ConnectWithAPIKeyConfig(
	ctx context.Context,
	apiKey string,
	connectionConfig ConnectionConfigValues,
) (TokenSet, ProviderAccount, error) {
	if p.authMethod != AuthMethodAPIKey {
		return TokenSet{}, ProviderAccount{}, errors.New("watch sync plugin does not support API-key authentication")
	}
	config, err := p.providerConfig(ctx)
	if err != nil {
		return TokenSet{}, ProviderAccount{}, err
	}
	connectionValues, connectionSecrets, err := p.connectionConfig(connectionConfig)
	if err != nil {
		return TokenSet{}, ProviderAccount{}, err
	}
	config = mergeWatchSyncProviderConfig(config, connectionValues)
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return TokenSet{}, ProviderAccount{}, watchSyncUnavailableError()
	}
	response, err := client.ExchangeAPIKey(ctx, &pluginv1.WatchSyncExchangeAPIKeyRequest{
		CapabilityId:   p.capabilityID,
		ProviderConfig: config,
		ApiKey:         apiKey,
	})
	if err != nil {
		return TokenSet{}, ProviderAccount{}, watchSyncRPCError()
	}
	faultSecrets := append([]string{apiKey}, connectionSecrets...)
	if err := watchSyncFaultError(p.Key(), response.GetFault(), faultSecrets...); err != nil {
		return TokenSet{}, ProviderAccount{}, err
	}
	tokens, err := tokenSetFromProto(response.GetCredentials())
	if err != nil {
		return TokenSet{}, ProviderAccount{}, err
	}
	account, err := accountFromProto(response.GetAccount())
	if err != nil {
		return TokenSet{}, ProviderAccount{}, err
	}
	return tokens, account, nil
}

func (p *PluginProvider) StartDeviceAuth(ctx context.Context, _ ServerConfig) (DeviceAuthSession, error) {
	if p.authMethod != AuthMethodDeviceCode {
		return DeviceAuthSession{}, errors.New("watch sync plugin does not support device authorization")
	}
	config, err := p.providerConfig(ctx)
	if err != nil {
		return DeviceAuthSession{}, err
	}
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return DeviceAuthSession{}, watchSyncUnavailableError()
	}
	response, err := client.StartDeviceAuthorization(ctx, &pluginv1.WatchSyncDeviceAuthorizationServiceStartRequest{
		CapabilityId:   p.capabilityID,
		ProviderConfig: config,
	})
	if err != nil {
		return DeviceAuthSession{}, watchSyncRPCError()
	}
	if err := watchSyncFaultError(p.Key(), response.GetFault()); err != nil {
		return DeviceAuthSession{}, err
	}
	if strings.TrimSpace(response.GetUserCode()) == "" || len(response.GetProviderState()) == 0 || response.GetExpiresAt() == nil {
		return DeviceAuthSession{}, errors.New("watch sync plugin returned an incomplete device authorization")
	}
	if err := response.GetExpiresAt().CheckValid(); err != nil || !response.GetExpiresAt().AsTime().After(time.Now()) {
		return DeviceAuthSession{}, errors.New("watch sync plugin returned an invalid device authorization expiry")
	}
	interval := 5
	if pollingInterval := response.GetPollingInterval(); pollingInterval != nil {
		if err := pollingInterval.CheckValid(); err != nil || pollingInterval.AsDuration() <= 0 {
			return DeviceAuthSession{}, errors.New("watch sync plugin returned an invalid device authorization polling interval")
		}
		interval = max(1, int(pollingInterval.AsDuration().Seconds()))
	}
	verificationURL, err := validDeviceVerificationURL(response.GetVerificationUrl())
	if err != nil {
		return DeviceAuthSession{}, err
	}
	if complete := strings.TrimSpace(response.GetVerificationUrlComplete()); complete != "" {
		verificationURL, err = validDeviceVerificationURL(complete)
		if err != nil {
			return DeviceAuthSession{}, err
		}
	}
	return DeviceAuthSession{
		DeviceCode:      base64.RawURLEncoding.EncodeToString(response.GetProviderState()),
		UserCode:        strings.TrimSpace(response.GetUserCode()),
		VerificationURL: verificationURL,
		IntervalSeconds: interval,
		ExpiresAt:       response.GetExpiresAt().AsTime(),
	}, nil
}

func (p *PluginProvider) PollDeviceAuth(ctx context.Context, _ ServerConfig, session DeviceAuthSession) (TokenSet, error) {
	state, err := base64.RawURLEncoding.DecodeString(session.DeviceCode)
	if err != nil {
		return TokenSet{}, errors.New("watch sync plugin device authorization state is invalid")
	}
	config, err := p.providerConfig(ctx)
	if err != nil {
		return TokenSet{}, err
	}
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return TokenSet{}, watchSyncUnavailableError()
	}
	response, err := client.PollDeviceAuthorization(ctx, &pluginv1.WatchSyncDeviceAuthorizationServicePollRequest{
		CapabilityId:   p.capabilityID,
		ProviderConfig: config,
		ProviderState:  state,
	})
	if err != nil {
		return TokenSet{}, watchSyncRPCError()
	}
	if err := watchSyncFaultError(p.Key(), response.GetFault()); err != nil {
		return TokenSet{}, err
	}
	switch response.GetStatus() {
	case pluginv1.WatchSyncDeviceAuthorizationStatus_WATCH_SYNC_DEVICE_AUTHORIZATION_STATUS_PENDING:
		updated, err := updatedPendingDeviceAuthSession(session, response)
		if err != nil {
			return TokenSet{}, err
		}
		return TokenSet{}, deviceAuthorizationPendingError{session: updated}
	case pluginv1.WatchSyncDeviceAuthorizationStatus_WATCH_SYNC_DEVICE_AUTHORIZATION_STATUS_DENIED:
		return TokenSet{}, errors.New("watch sync plugin device authorization was denied")
	case pluginv1.WatchSyncDeviceAuthorizationStatus_WATCH_SYNC_DEVICE_AUTHORIZATION_STATUS_EXPIRED:
		return TokenSet{}, errors.New("watch sync plugin device authorization expired")
	case pluginv1.WatchSyncDeviceAuthorizationStatus_WATCH_SYNC_DEVICE_AUTHORIZATION_STATUS_AUTHORIZED:
		return tokenSetFromProto(response.GetCredentials())
	default:
		return TokenSet{}, errors.New("watch sync plugin returned an invalid device authorization status")
	}
}

func validDeviceVerificationURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("watch sync plugin returned an invalid device authorization verification URL")
	}
	return parsed.String(), nil
}

func updatedPendingDeviceAuthSession(
	session DeviceAuthSession,
	response *pluginv1.WatchSyncDeviceAuthorizationServicePollResponse,
) (DeviceAuthSession, error) {
	if response.ProviderState != nil {
		session.DeviceCode = base64.RawURLEncoding.EncodeToString(response.GetProviderState())
	}
	if pollingInterval := response.GetPollingInterval(); pollingInterval != nil {
		if err := pollingInterval.CheckValid(); err != nil || pollingInterval.AsDuration() <= 0 {
			return DeviceAuthSession{}, errors.New("watch sync plugin returned an invalid device authorization polling interval")
		}
		session.IntervalSeconds = max(1, int(pollingInterval.AsDuration().Seconds()))
	}
	if expiresAt := response.GetExpiresAt(); expiresAt != nil {
		if err := expiresAt.CheckValid(); err != nil || !expiresAt.AsTime().After(time.Now()) {
			return DeviceAuthSession{}, errors.New("watch sync plugin returned an invalid device authorization expiry")
		}
		session.ExpiresAt = expiresAt.AsTime()
	}
	return session, nil
}

func (p *PluginProvider) RefreshToken(ctx context.Context, _ ServerConfig, conn Connection) (TokenSet, error) {
	authContext, err := p.authenticatedContext(ctx, conn)
	if err != nil {
		return TokenSet{}, err
	}
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return TokenSet{}, watchSyncUnavailableError()
	}
	response, err := client.RefreshCredentials(ctx, &pluginv1.WatchSyncRefreshCredentialsRequest{
		Context: authContext,
	})
	if err != nil {
		return TokenSet{}, watchSyncRPCError()
	}
	var tokens TokenSet
	if response.GetCredentials() != nil {
		tokens, err = tokenSetFromProto(response.GetCredentials())
		if err != nil {
			return TokenSet{}, err
		}
	}
	if err := watchSyncFaultError(p.Key(), response.GetFault(), conn.AccessToken, conn.RefreshToken, tokens.AccessToken, tokens.RefreshToken); err != nil {
		return tokens, err
	}
	if response.GetCredentials() == nil {
		return TokenSet{}, errors.New("watch sync plugin returned no access token")
	}
	return tokens, nil
}

func (p *PluginProvider) LookupAccount(ctx context.Context, _ ServerConfig, conn Connection) (ProviderAccount, error) {
	authContext, err := p.authenticatedContext(ctx, conn)
	if err != nil {
		return ProviderAccount{}, err
	}
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return ProviderAccount{}, watchSyncUnavailableError()
	}
	response, err := client.GetAccount(ctx, &pluginv1.WatchSyncGetAccountRequest{
		Context: authContext,
	})
	if err != nil {
		return ProviderAccount{}, watchSyncRPCError()
	}
	if err := watchSyncFaultError(p.Key(), response.GetFault(), conn.AccessToken, conn.RefreshToken); err != nil {
		return ProviderAccount{}, err
	}
	return accountFromProto(response.GetAccount())
}

func (p *PluginProvider) FetchHistory(context.Context, ServerConfig, Connection) ([]RemotePlay, error) {
	// A desired-state tracker is not a timestamped play-history source. Silo's
	// durable local export rows provide reconciliation for this provider.
	return nil, nil
}

func (p *PluginProvider) ExportHistory(ctx context.Context, _ ServerConfig, conn Connection, plays []LocalPlay) (ExportResult, error) {
	result := ExportResult{Failed: map[string]string{}}
	if len(plays) == 0 {
		return result, nil
	}
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return result, watchSyncUnavailableError()
	}

	// Perform one bounded RPC per service iteration. This lets the existing
	// exporter commit every per-event result before requesting the next batch,
	// and avoids holding one sync run across many sequential plugin deadlines.
	batchSize := len(plays)
	if maximum := p.ExportBatchSize(); batchSize > maximum {
		batchSize = maximum
	}
	events := make([]*pluginv1.WatchSyncEvent, 0, batchSize)
	selectedPlays := make([]LocalPlay, 0, batchSize)
	for _, play := range plays[:batchSize] {
		event := watchEventFromLocalPlay(play, pluginv1.WatchSyncOrigin_WATCH_SYNC_ORIGIN_RECONCILIATION)
		if !p.supportsMedia(event.GetMedia().GetMediaType()) {
			result.Failed[play.HistoryID] = unsupportedWatchSyncMediaMessage(event.GetMedia().GetMediaType())
			continue
		}
		events = append(events, event)
		selectedPlays = append(selectedPlays, play)
	}
	if len(events) == 0 {
		return result, nil
	}
	authContext, err := p.authenticatedContext(ctx, conn)
	if err != nil {
		return result, err
	}
	response, err := client.ApplyEvents(ctx, &pluginv1.WatchSyncApplyEventsRequest{
		Context: authContext,
		Events:  events,
	})
	if err != nil {
		return result, watchSyncRPCError()
	}
	if response.GetUpdatedCredentials() != nil {
		conn, err = p.persistUpdatedCredentials(ctx, conn, response.GetUpdatedCredentials())
		if err != nil {
			return result, err
		}
	}
	if err := watchSyncFaultError(p.Key(), response.GetFault(), conn.AccessToken, conn.RefreshToken); err != nil {
		// Batch-level faults apply to the whole request; no per-event results
		// are committed when the host is told to ignore them.
		return result, err
	}

	var rateLimited error
	for _, event := range events {
		apply := resultForEvent(response.GetResults(), event.GetEventId())
		if fault := apply.GetFault(); apply.GetStatus() == pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY &&
			fault != nil && fault.GetCode() == pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_RATE_LIMITED {
			retry := time.Duration(0)
			if fault.GetRetryAfter() != nil {
				retry = fault.GetRetryAfter().AsDuration()
			}
			rateLimited = RateLimitedError{Provider: p.Key(), RetryAfter: retry}
			break
		}
	}
	for index, event := range events {
		historyID := selectedPlays[index].HistoryID
		apply := resultForEvent(response.GetResults(), event.GetEventId())
		switch apply.GetStatus() {
		case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED,
			pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_NO_CHANGE:
			result.Sent = append(result.Sent, historyID)
		case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED:
			result.NotFound = append(result.NotFound, historyID)
		case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY:
			if fault := apply.GetFault(); fault != nil &&
				fault.GetCode() == pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_RATE_LIMITED {
				continue
			}
			result.Failed[historyID] = safeApplyMessage(apply, conn.AccessToken, conn.RefreshToken)
		default:
			if rateLimited == nil {
				result.Failed[historyID] = "watch sync plugin omitted a valid event result"
			}
		}
	}
	return result, rateLimited
}

func (p *PluginProvider) Start(ctx context.Context, _ ServerConfig, conn Connection, event ScrobbleEvent) error {
	return p.applyScrobble(ctx, conn, event, pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_START)
}

func (p *PluginProvider) Pause(ctx context.Context, _ ServerConfig, conn Connection, event ScrobbleEvent) error {
	return p.applyScrobble(ctx, conn, event, pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_PAUSE)
}

func (p *PluginProvider) Stop(ctx context.Context, _ ServerConfig, conn Connection, event ScrobbleEvent) error {
	return p.applyScrobble(ctx, conn, event, pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP)
}

func (p *PluginProvider) applyScrobble(
	ctx context.Context,
	conn Connection,
	event ScrobbleEvent,
	operation pluginv1.WatchSyncOperation,
) error {
	watchEvent := watchEventFromScrobble(event, operation)
	if !p.supportsMedia(watchEvent.GetMedia().GetMediaType()) {
		return watchSyncProviderFaultError{message: unsupportedWatchSyncMediaMessage(watchEvent.GetMedia().GetMediaType())}
	}
	result, statuses, err := p.applyPluginEventsDetailed(ctx, conn, []*pluginv1.WatchSyncEvent{watchEvent}, []string{watchEvent.GetEventId()})
	if err != nil {
		return err
	}
	switch statuses[watchEvent.GetEventId()] {
	case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED,
		pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_NO_CHANGE:
		return nil
	case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY:
		return retryableProviderError{message: result.Failed[watchEvent.GetEventId()]}
	case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED:
		return watchSyncProviderFaultError{
			code:    pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_PERMANENT,
			message: "watch sync plugin rejected the playback event",
		}
	default:
		return watchSyncProviderFaultError{
			code:    pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_PERMANENT,
			message: "watch sync plugin did not confirm the playback event",
		}
	}
}

func (p *PluginProvider) ScrobbleOrderingKey(conn Connection, event ScrobbleEvent) string {
	seriesID := firstNonEmptyWatchID(event.SeriesTVDBID, event.SeriesTMDBID, event.SeriesIMDbID, event.MediaItemID)
	return "plugin-watch-sync:" + conn.ID + ":" + seriesID
}

func (p *PluginProvider) authenticatedContext(ctx context.Context, conn Connection) (*pluginv1.WatchSyncAuthenticatedContext, error) {
	config, err := p.providerConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &pluginv1.WatchSyncAuthenticatedContext{
		CapabilityId:   p.capabilityID,
		ProviderConfig: config,
		Credentials:    credentialsFromConnection(conn),
	}, nil
}

func (p *PluginProvider) providerConfig(ctx context.Context) (*pluginv1.WatchSyncProviderConfig, error) {
	if p.resolveConfig == nil {
		return &pluginv1.WatchSyncProviderConfig{}, nil
	}
	config, err := p.resolveConfig(ctx, p.installationID)
	if err != nil {
		return nil, retryableProviderError{message: "watch sync plugin configuration is unavailable"}
	}
	if config == nil {
		config = &pluginv1.WatchSyncProviderConfig{}
	}
	return config, nil
}

func (p *PluginProvider) connectionConfig(values ConnectionConfigValues) (*pluginv1.WatchSyncProviderConfig, []string, error) {
	declared := make(map[string]*pluginv1.ConfigSchema, len(p.connectionConfigSchema))
	for _, schema := range p.connectionConfigSchema {
		if schema != nil && strings.TrimSpace(schema.GetKey()) != "" {
			declared[schema.GetKey()] = schema
		}
	}
	secrets := connectionConfigSecrets(p.connectionConfigSchema, values)
	for key := range values {
		if _, ok := declared[key]; !ok {
			return nil, nil, sanitizedConnectionConfigError(
				fmt.Errorf("watch sync connection config key %q is not declared", key),
				secrets,
			)
		}
	}

	result := &pluginv1.WatchSyncProviderConfig{
		Values:       make(map[string]string),
		SecretValues: make(map[string]string),
	}
	flattenedFields := make(map[string]string)
	for _, schema := range p.connectionConfigSchema {
		if schema == nil || strings.TrimSpace(schema.GetKey()) == "" {
			continue
		}
		value, exists := values[schema.GetKey()]
		if !exists {
			if schema.GetRequired() {
				return nil, nil, fmt.Errorf("watch sync connection config %q is required", schema.GetKey())
			}
			continue
		}
		if err := publicconfig.ValidateValue(schema, "watch sync connection config", schema.GetKey(), value); err != nil {
			return nil, nil, sanitizedConnectionConfigError(err, secrets)
		}
		if err := validateConnectionAdminFormValue(schema, value); err != nil {
			return nil, nil, sanitizedConnectionConfigError(err, secrets)
		}
		publicFieldNames, _ := hostplugins.ConfigSchemaFieldSets(schema)
		publicFields := make(map[string]struct{}, len(publicFieldNames))
		for _, field := range publicFieldNames {
			publicFields[field] = struct{}{}
		}
		for field, raw := range value {
			rawField := field
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			encoded, err := connectionConfigString(raw)
			if err != nil {
				return nil, nil, sanitizedConnectionConfigError(
					fmt.Errorf("encode watch sync connection config %q.%s: %w", schema.GetKey(), field, err),
					secrets,
				)
			}
			key := schema.GetKey() + "." + field
			source := fmt.Sprintf("config %q field %q", schema.GetKey(), rawField)
			if previous, exists := flattenedFields[key]; exists {
				return nil, nil, sanitizedConnectionConfigError(
					fmt.Errorf("watch sync connection %s conflicts with %s after flattening to %q", source, previous, key),
					secrets,
				)
			}
			flattenedFields[key] = source
			if _, public := publicFields[field]; public {
				result.Values[key] = encoded
			} else {
				result.SecretValues[key] = encoded
			}
		}
	}
	// Fields the schema never declared are classified as secret above, so redact
	// every flattened secret rather than only the declared ones.
	for _, encoded := range result.SecretValues {
		if strings.TrimSpace(encoded) != "" {
			secrets = append(secrets, encoded)
		}
	}
	return result, secrets, nil
}

func connectionConfigSecrets(schemas []*pluginv1.ConfigSchema, values ConnectionConfigValues) []string {
	var secrets []string
	for _, schema := range schemas {
		if schema == nil {
			continue
		}
		value, exists := values[schema.GetKey()]
		if !exists {
			continue
		}
		_, secretFields := hostplugins.ConfigSchemaFieldSets(schema)
		for _, field := range secretFields {
			raw, exists := value[field]
			if !exists {
				continue
			}
			if encoded, err := connectionConfigString(raw); err == nil && strings.TrimSpace(encoded) != "" {
				secrets = append(secrets, encoded)
			}
			secrets = append(secrets, connectionConfigSecretStrings(raw)...)
		}
	}
	return secrets
}

func sanitizedConnectionConfigError(err error, secrets []string) error {
	return errors.New(sanitizeWatchSyncMessage(
		err.Error(),
		"watch sync connection config is invalid",
		secrets...,
	))
}

func connectionConfigSecretStrings(value any) []string {
	switch typed := value.(type) {
	case map[string]any:
		var values []string
		for _, child := range typed {
			values = append(values, connectionConfigSecretStrings(child)...)
		}
		return values
	case []any:
		var values []string
		for _, child := range typed {
			values = append(values, connectionConfigSecretStrings(child)...)
		}
		return values
	default:
		encoded, err := connectionConfigString(typed)
		if err != nil || strings.TrimSpace(encoded) == "" {
			return nil
		}
		return []string{encoded}
	}
}

func validateWatchSyncConnectionConfigSchemas(schemas []*pluginv1.ConfigSchema) error {
	seen := make(map[string]struct{}, len(schemas))
	for _, schema := range schemas {
		if schema == nil {
			continue
		}
		key := strings.TrimSpace(schema.GetKey())
		if key == "" {
			return fmt.Errorf("connection config key is required")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("connection config key %q is duplicated", key)
		}
		seen[key] = struct{}{}
		for _, field := range schema.GetAdminForm().GetFields() {
			if field == nil {
				continue
			}
			if strings.TrimSpace(field.GetExclusiveGroupField()) != "" {
				return fmt.Errorf(
					"connection config %q field %q uses exclusive_group_field, which watch provider setup does not support",
					schema.GetKey(),
					field.GetKey(),
				)
			}
			if field.GetDynamicOptions() && len(field.GetOptions()) == 0 {
				return fmt.Errorf(
					"connection config %q field %q requires dynamic options, which watch provider setup does not support",
					schema.GetKey(),
					field.GetKey(),
				)
			}
			if field.GetControl() == pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SELECT {
				for _, option := range field.GetOptions() {
					if option != nil && strings.TrimSpace(option.GetValue()) == "" {
						return fmt.Errorf(
							"connection config %q field %q has a blank select option value",
							schema.GetKey(),
							field.GetKey(),
						)
					}
				}
			}
			if pattern := field.GetValidation().GetPattern(); pattern != "" {
				if _, err := regexp.Compile(pattern); err != nil {
					return fmt.Errorf(
						"connection config %q field %q has invalid validation pattern: %w",
						schema.GetKey(),
						field.GetKey(),
						err,
					)
				}
			}
		}
		if err := validateConnectionSchemaIsRenderable(schema); err != nil {
			return err
		}
	}
	return nil
}

func validateConnectionSchemaIsRenderable(schema *pluginv1.ConfigSchema) error {
	if strings.TrimSpace(schema.GetJsonSchema()) == "" && !schema.GetRequired() {
		return nil
	}
	var document struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type  string `json:"type"`
			Items *struct {
				Type string `json:"type"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema.GetJsonSchema()), &document); err != nil {
		return fmt.Errorf("connection config %q has invalid json_schema: %w", schema.GetKey(), err)
	}
	if document.Type != "object" || document.Properties == nil {
		return fmt.Errorf("connection config %q must have a renderable object json_schema", schema.GetKey())
	}
	explicit := make(map[string]*pluginv1.AdminFormField)
	if form := schema.GetAdminForm(); form != nil {
		for _, field := range form.GetFields() {
			if field != nil {
				explicit[field.GetKey()] = field
			}
		}
	}
	for key, property := range document.Properties {
		field := explicit[key]
		switch property.Type {
		case "string", watchSyncJSONSchemaNumberType, "integer", watchSyncJSONSchemaBooleanType:
			if field != nil && !connectionAdminFieldSupportsScalarType(field, property.Type) {
				return fmt.Errorf(
					"connection config %q field %q control %q cannot emit json_schema type %q",
					schema.GetKey(),
					key,
					field.GetControl().String(),
					property.Type,
				)
			}
			continue
		case "array":
			if field != nil && field.GetControl() == pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_MULTI_SELECT &&
				(property.Items == nil || property.Items.Type == "string" || property.Items.Type == watchSyncJSONSchemaNumberType ||
					property.Items.Type == "integer" || property.Items.Type == watchSyncJSONSchemaBooleanType) {
				continue
			}
		default:
			// A property whose shape comes from enum/const/$ref cannot be
			// inferred from type alone, but an explicit scalar form control is
			// still a complete input mechanism. Direct object properties remain
			// unsupported because none of these controls produces an object.
			if property.Type != "object" && connectionAdminFieldRendersValue(field) {
				continue
			}
		}
		return fmt.Errorf(
			"connection config %q property %q needs a renderable admin_form field because type %q cannot be inferred",
			schema.GetKey(),
			key,
			property.Type,
		)
	}
	return nil
}

func connectionAdminFieldSupportsScalarType(field *pluginv1.AdminFormField, propertyType string) bool {
	switch field.GetControl() {
	case pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SWITCH:
		return propertyType == watchSyncJSONSchemaBooleanType
	case pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_MULTI_SELECT:
		return false
	default:
		return true
	}
}

func connectionAdminFieldRendersValue(field *pluginv1.AdminFormField) bool {
	if field == nil {
		return false
	}
	switch field.GetControl() {
	case pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT,
		pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXTAREA,
		pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_PASSWORD,
		pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_NUMBER,
		pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SWITCH,
		pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SELECT,
		pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_MULTI_SELECT:
		return true
	default:
		return false
	}
}

func validateConnectionAdminFormValue(schema *pluginv1.ConfigSchema, value map[string]any) error {
	if schema == nil || schema.GetAdminForm() == nil {
		return nil
	}
	for _, field := range schema.GetAdminForm().GetFields() {
		if field == nil || !connectionAdminFieldIsVisible(schema.GetAdminForm(), field, value) {
			continue
		}
		raw, exists := value[field.GetKey()]
		if !exists || connectionConfigValueIsEmpty(raw) {
			if field.GetRequired() {
				return fmt.Errorf("connection config %q field %q is required", schema.GetKey(), field.GetKey())
			}
			continue
		}
		if field.GetValidation() == nil {
			continue
		}
		validation := field.GetValidation()
		if text, ok := raw.(string); ok {
			if pattern := validation.GetPattern(); pattern != "" {
				matched, err := regexp.MatchString(pattern, text)
				if err != nil {
					return fmt.Errorf("connection config %q field %q has an invalid validation pattern", schema.GetKey(), field.GetKey())
				}
				if !matched {
					return fmt.Errorf("connection config %q field %q is invalid", schema.GetKey(), field.GetKey())
				}
			}
			length := utf8.RuneCountInString(text)
			if minimum := int(validation.GetMinLength()); minimum > 0 && length < minimum {
				return fmt.Errorf("connection config %q field %q must be at least %d characters", schema.GetKey(), field.GetKey(), minimum)
			}
			if maximum := int(validation.GetMaxLength()); maximum > 0 && length > maximum {
				return fmt.Errorf("connection config %q field %q must be at most %d characters", schema.GetKey(), field.GetKey(), maximum)
			}
		}
		if number, ok := connectionConfigNumber(raw); ok {
			if validation.GetHasMin() && number < validation.GetMin() {
				return fmt.Errorf("connection config %q field %q must be at least %g", schema.GetKey(), field.GetKey(), validation.GetMin())
			}
			if validation.GetHasMax() && number > validation.GetMax() {
				return fmt.Errorf("connection config %q field %q must be at most %g", schema.GetKey(), field.GetKey(), validation.GetMax())
			}
		}
	}
	return nil
}

func connectionConfigValueIsEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}

func connectionAdminFieldIsVisible(
	form *pluginv1.AdminFormDescriptor,
	field *pluginv1.AdminFormField,
	values map[string]any,
) bool {
	if !connectionAdminConditionsMatch(field.GetShowWhen(), values, form.GetFields()) {
		return false
	}
	contained := false
	for _, section := range form.GetSections() {
		if section == nil || !stringSliceContains(section.GetFieldKeys(), field.GetKey()) {
			continue
		}
		contained = true
		if connectionAdminConditionsMatch(section.GetShowWhen(), values, form.GetFields()) {
			return true
		}
	}
	return !contained
}

func connectionAdminConditionsMatch(
	conditions []*pluginv1.AdminFormCondition,
	values map[string]any,
	fields []*pluginv1.AdminFormField,
) bool {
	for _, condition := range conditions {
		if condition == nil {
			continue
		}
		value, exists := values[condition.GetField()]
		if !exists {
			for _, field := range fields {
				if field != nil && field.GetKey() == condition.GetField() && field.GetDefaultValue() != nil {
					value = field.GetDefaultValue().AsInterface()
					break
				}
			}
		}
		if !stringSliceContains(condition.GetEquals(), connectionAdminConditionString(value)) {
			return false
		}
	}
	return true
}

func connectionAdminConditionString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func connectionConfigNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return number, err == nil
	default:
		return 0, false
	}
}

func connectionConfigString(value any) (string, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func mergeWatchSyncProviderConfig(base, connection *pluginv1.WatchSyncProviderConfig) *pluginv1.WatchSyncProviderConfig {
	merged := &pluginv1.WatchSyncProviderConfig{Values: map[string]string{}, SecretValues: map[string]string{}}
	if base != nil {
		for key, value := range base.GetValues() {
			merged.Values[key] = value
		}
		for key, value := range base.GetSecretValues() {
			merged.SecretValues[key] = value
		}
	}
	if connection != nil {
		for key, value := range connection.GetValues() {
			delete(merged.SecretValues, key)
			merged.Values[key] = value
		}
		for key, value := range connection.GetSecretValues() {
			delete(merged.Values, key)
			merged.SecretValues[key] = value
		}
	}
	return merged
}

func (p *PluginProvider) persistUpdatedCredentials(
	ctx context.Context,
	conn Connection,
	credentials *pluginv1.WatchSyncCredentials,
) (Connection, error) {
	if p.repository == nil {
		return Connection{}, retryableProviderError{message: "watch sync plugin credential storage is unavailable"}
	}
	tokens, err := tokenSetFromProto(credentials)
	if err != nil {
		return Connection{}, err
	}
	conn = connectionWithTokens(conn, tokens)
	persisted, err := p.repository.UpsertConnection(ctx, conn)
	if err != nil {
		return Connection{}, retryableProviderError{message: "watch sync plugin credential update could not be persisted"}
	}
	return persisted, nil
}

func watchEventFromLocalPlay(play LocalPlay, origin pluginv1.WatchSyncOrigin) *pluginv1.WatchSyncEvent {
	return &pluginv1.WatchSyncEvent{
		EventId:         play.HistoryID,
		Operation:       pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_MARK_WATCHED,
		Origin:          origin,
		OccurredAt:      timestamppb.New(play.WatchedAt),
		WatchHistoryId:  play.HistoryID,
		DurationSeconds: play.DurationSeconds,
		ProviderItemKey: play.ProviderItemKey,
		Media: mediaFromIdentity(play.MediaItemID, play.Kind, play.Title, play.Year,
			play.IMDbID, play.TMDBID, play.TVDBID, play.SeriesTitle, play.SeriesYear,
			play.SeriesIMDbID, play.SeriesTMDBID, play.SeriesTVDBID, play.SeasonNumber, play.EpisodeNumber),
	}
}

func watchEventFromScrobble(event ScrobbleEvent, operation pluginv1.WatchSyncOperation) *pluginv1.WatchSyncEvent {
	eventID := "scrobble:" + operation.String() + ":" + event.PlaybackSessionID
	if operation == pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_PAUSE {
		eventID += fmt.Sprintf(":%.3f", event.PositionSeconds)
	}
	completion := 0.0
	if event.DurationSeconds > 0 {
		completion = event.PositionSeconds / event.DurationSeconds * 100
	}
	return &pluginv1.WatchSyncEvent{
		EventId:           eventID,
		Operation:         operation,
		Origin:            pluginv1.WatchSyncOrigin_WATCH_SYNC_ORIGIN_PLAYBACK_COMPLETION,
		OccurredAt:        timestamppb.New(event.OccurredAt),
		WatchHistoryId:    event.HistoryID,
		PlaybackSessionId: event.PlaybackSessionID,
		PositionSeconds:   event.PositionSeconds,
		DurationSeconds:   event.DurationSeconds,
		CompletionPercent: completion,
		Completed:         event.Completed,
		ProviderItemKey:   event.ProviderItemKey,
		Media: mediaFromIdentity(event.MediaItemID, event.Kind, "", 0,
			event.IMDbID, event.TMDBID, event.TVDBID, "", 0,
			event.SeriesIMDbID, event.SeriesTMDBID, event.SeriesTVDBID, event.SeasonNumber, event.EpisodeNumber),
	}
}

func mediaFromIdentity(mediaItemID, kind, title string, year int, imdbID, tmdbID, tvdbID, seriesTitle string, seriesYear int, seriesIMDbID, seriesTMDBID, seriesTVDBID string, season, episode int) *pluginv1.WatchSyncMedia {
	return &pluginv1.WatchSyncMedia{
		MediaItemId:       mediaItemID,
		MediaType:         watchSyncMediaType(kind),
		Title:             title,
		Year:              int32(year),
		ExternalIds:       watchIDs(imdbID, tmdbID, tvdbID),
		SeriesTitle:       seriesTitle,
		SeriesYear:        int32(seriesYear),
		SeriesExternalIds: watchIDs(seriesIMDbID, seriesTMDBID, seriesTVDBID),
		SeasonNumber:      int32(season),
		EpisodeNumber:     int32(episode),
	}
}

func watchSyncMediaType(kind string) pluginv1.WatchSyncMediaType {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case historyimport.KindMovie:
		return pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE
	case historyimport.KindEpisode:
		return pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE
	default:
		return pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_UNSPECIFIED
	}
}

func watchIDs(imdbID, tmdbID, tvdbID string) map[string]string {
	ids := map[string]string{}
	if imdbID != "" {
		ids["imdb"] = imdbID
	}
	if tmdbID != "" {
		ids["tmdb"] = tmdbID
	}
	if tvdbID != "" {
		ids["tvdb"] = tvdbID
	}
	return ids
}

func credentialsFromConnection(conn Connection) *pluginv1.WatchSyncCredentials {
	credentials := &pluginv1.WatchSyncCredentials{
		AccessToken:      conn.AccessToken,
		RefreshToken:     conn.RefreshToken,
		TokenType:        conn.TokenType,
		Scopes:           append([]string(nil), conn.Scopes...),
		SecretAttributes: cloneStringMap(conn.SecretAttributes),
	}
	if conn.TokenExpiresAt != nil {
		credentials.ExpiresAt = timestamppb.New(*conn.TokenExpiresAt)
	}
	return credentials
}

func tokenSetFromProto(credentials *pluginv1.WatchSyncCredentials) (TokenSet, error) {
	if credentials == nil || strings.TrimSpace(credentials.GetAccessToken()) == "" {
		return TokenSet{}, errors.New("watch sync plugin returned no access token")
	}
	var expiresAt *time.Time
	if credentials.GetExpiresAt() != nil {
		if err := credentials.GetExpiresAt().CheckValid(); err != nil {
			return TokenSet{}, errors.New("watch sync plugin returned an invalid credential expiry")
		}
		value := credentials.GetExpiresAt().AsTime()
		expiresAt = &value
	}
	return TokenSet{
		AccessToken:      credentials.GetAccessToken(),
		RefreshToken:     credentials.GetRefreshToken(),
		TokenExpiresAt:   expiresAt,
		TokenType:        strings.TrimSpace(credentials.GetTokenType()),
		Scopes:           append([]string(nil), credentials.GetScopes()...),
		SecretAttributes: cloneStringMap(credentials.GetSecretAttributes()),
	}, nil
}

func accountFromProto(account *pluginv1.WatchSyncAccount) (ProviderAccount, error) {
	if account == nil || strings.TrimSpace(account.GetExternalSubject()) == "" {
		return ProviderAccount{}, errors.New("watch sync plugin returned no provider account identity")
	}
	return ProviderAccount{ID: account.GetExternalSubject(), Username: account.GetUsername()}, nil
}

func resultForEvent(results []*pluginv1.WatchSyncApplyResult, eventID string) *pluginv1.WatchSyncApplyResult {
	for _, result := range results {
		if result.GetEventId() == eventID {
			return result
		}
	}
	return nil
}

func supportedWatchSyncAuthMethod(descriptor *pluginv1.WatchSyncProviderDescriptor) (string, error) {
	supported := make(map[string]struct{}, 2)
	for _, method := range descriptor.GetAuthMethods() {
		switch method {
		case pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY:
			supported[AuthMethodAPIKey] = struct{}{}
		case pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE:
			supported[AuthMethodDeviceCode] = struct{}{}
		}
	}
	if len(supported) > 1 {
		return "", errors.New("advertises multiple host authentication methods; exactly one is required")
	}
	if _, ok := supported[AuthMethodAPIKey]; ok {
		return AuthMethodAPIKey, nil
	}
	if _, ok := supported[AuthMethodDeviceCode]; ok {
		return AuthMethodDeviceCode, nil
	}
	return "", errors.New("does not advertise an authentication method supported by the host")
}

func supportedWatchSyncMediaTypes(descriptor *pluginv1.WatchSyncProviderDescriptor) (map[pluginv1.WatchSyncMediaType]struct{}, error) {
	media := descriptor.GetSupportedMediaTypes()
	if len(media) == 0 {
		return map[pluginv1.WatchSyncMediaType]struct{}{
			pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE:   {},
			pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE: {},
		}, nil
	}
	supported := make(map[pluginv1.WatchSyncMediaType]struct{}, len(media))
	for _, mediaType := range media {
		switch mediaType {
		case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE,
			pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE:
			supported[mediaType] = struct{}{}
		default:
			return nil, fmt.Errorf("advertises unsupported media type %q", mediaType.String())
		}
	}
	return supported, nil
}

func (p *PluginProvider) supportsMedia(mediaType pluginv1.WatchSyncMediaType) bool {
	if mediaType == pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_UNSPECIFIED {
		return false
	}
	_, ok := p.supportedMedia[mediaType]
	return ok
}

func unsupportedWatchSyncMediaMessage(mediaType pluginv1.WatchSyncMediaType) string {
	switch mediaType {
	case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE:
		return watchSyncUnsupportedMovieMediaMessage
	case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE:
		return watchSyncUnsupportedEpisodeMediaMessage
	default:
		return watchSyncUnsupportedMediaMessage
	}
}

func safeApplyMessage(result *pluginv1.WatchSyncApplyResult, secrets ...string) string {
	if result == nil {
		return "watch sync provider could not apply the event"
	}
	return sanitizeWatchSyncMessage(result.GetFault().GetSafeMessage(), "watch sync provider could not apply the event", secrets...)
}

func sanitizeWatchSyncMessage(message string, fallback string, secrets ...string) string {
	message = normalizeWatchSyncText(message)
	for _, secret := range secrets {
		if secret = normalizeWatchSyncText(secret); secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	if message == "" {
		return fallback
	}
	const maxRunes = 256
	runes := []rune(message)
	if len(runes) > maxRunes {
		message = string(runes[:maxRunes]) + "…"
	}
	return message
}

func normalizeWatchSyncText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func watchSyncUnavailableError() error {
	return retryableProviderError{message: "watch sync plugin is unavailable"}
}

func watchSyncRPCError() error {
	return retryableProviderError{message: "watch sync plugin RPC failed"}
}

type watchSyncProviderFaultError struct {
	code    pluginv1.WatchSyncFaultCode
	message string
}

func (e watchSyncProviderFaultError) Error() string { return e.message }

func isWatchSyncInvalidCredentialError(err error) bool {
	var fault watchSyncProviderFaultError
	return errors.As(err, &fault) && fault.code == pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL
}

func watchSyncFaultError(provider string, fault *pluginv1.WatchSyncFault, secrets ...string) error {
	if fault == nil || fault.GetCode() == pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_UNSPECIFIED {
		return nil
	}
	message := sanitizeWatchSyncMessage(fault.GetSafeMessage(), "watch sync provider request failed", secrets...)
	if fault.GetCode() == pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_RATE_LIMITED {
		retry := time.Duration(0)
		if fault.GetRetryAfter() != nil {
			retry = fault.GetRetryAfter().AsDuration()
		}
		return RateLimitedError{Provider: provider, RetryAfter: retry}
	}
	if fault.GetCode() == pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_TEMPORARY {
		return retryableProviderError{message: message}
	}
	return watchSyncProviderFaultError{code: fault.GetCode(), message: message}
}

func firstNonEmptyWatchID(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "unknown"
}
