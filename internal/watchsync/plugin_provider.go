package watchsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type WatchSyncPluginClient interface {
	ExchangeAPIKey(context.Context, *pluginv1.WatchSyncExchangeAPIKeyRequest) (*pluginv1.WatchSyncCredentialResponse, error)
	RefreshCredentials(context.Context, *pluginv1.WatchSyncRefreshCredentialsRequest) (*pluginv1.WatchSyncCredentialResponse, error)
	GetAccount(context.Context, *pluginv1.WatchSyncGetAccountRequest) (*pluginv1.WatchSyncGetAccountResponse, error)
	ApplyEvents(context.Context, *pluginv1.WatchSyncApplyEventsRequest) (*pluginv1.WatchSyncApplyEventsResponse, error)
}

type WatchSyncPluginClientResolver func(context.Context, int, string) (WatchSyncPluginClient, error)

type PluginProviderOptions struct {
	InstallationID int
	ProviderKey    string
	CapabilityID   string
	DisplayName    string
	Descriptor     *pluginv1.WatchSyncProviderDescriptor
	ResolveClient  WatchSyncPluginClientResolver
}

type PluginProvider struct {
	installationID int
	providerKey    string
	capabilityID   string
	displayName    string
	descriptor     *pluginv1.WatchSyncProviderDescriptor
	supportedMedia map[pluginv1.WatchSyncMediaType]struct{}
	resolveClient  WatchSyncPluginClientResolver
}

const (
	watchSyncUnsupportedMovieMediaMessage   = "watch sync plugin does not support movie media"
	watchSyncUnsupportedEpisodeMediaMessage = "watch sync plugin does not support episode media"
	watchSyncUnsupportedMediaMessage        = "watch sync plugin does not support this media type"
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
	if !supportsWatchSyncAPIKey(options.Descriptor) {
		return nil, fmt.Errorf("watch sync plugin %q does not support API-key authentication", options.ProviderKey)
	}
	if !options.Descriptor.GetExportWatched() {
		return nil, fmt.Errorf("watch sync plugin %q does not export watched state", options.ProviderKey)
	}
	if options.Descriptor.GetExportUnwatched() || options.Descriptor.GetImportWatched() || options.Descriptor.GetImportProgress() {
		return nil, fmt.Errorf("watch sync plugin %q advertises operations unsupported by the initial server adapter", options.ProviderKey)
	}
	supportedMedia, err := supportedWatchSyncMediaTypes(options.Descriptor)
	if err != nil {
		return nil, fmt.Errorf("watch sync plugin %q %w", options.ProviderKey, err)
	}
	if options.ResolveClient == nil {
		return nil, fmt.Errorf("watch sync plugin client resolver is required")
	}
	return &PluginProvider{
		installationID: options.InstallationID,
		providerKey:    options.ProviderKey,
		capabilityID:   options.CapabilityID,
		displayName:    options.DisplayName,
		descriptor:     options.Descriptor,
		supportedMedia: supportedMedia,
		resolveClient:  options.ResolveClient,
	}, nil
}

func (p *PluginProvider) Key() string { return p.providerKey }

func (p *PluginProvider) DisplayName() string {
	if strings.TrimSpace(p.displayName) != "" {
		return p.displayName
	}
	return p.capabilityID
}

func (p *PluginProvider) ProviderSource() string { return providerSourcePlugin }

func (p *PluginProvider) authoritativeRefreshProvider() {}

func (p *PluginProvider) ExportBatchSize() int {
	if size := int(p.descriptor.GetMaxBatchSize()); size > 0 {
		return size
	}
	return 1
}

func (p *PluginProvider) Capabilities() Capabilities {
	return Capabilities{
		ExportWatched:    true,
		ScrobblePlayback: true,
	}
}

func (p *PluginProvider) ConnectWithAPIKey(ctx context.Context, apiKey string) (TokenSet, ProviderAccount, error) {
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return TokenSet{}, ProviderAccount{}, watchSyncUnavailableError()
	}
	response, err := client.ExchangeAPIKey(ctx, &pluginv1.WatchSyncExchangeAPIKeyRequest{
		CapabilityId: p.capabilityID,
		ApiKey:       apiKey,
	})
	if err != nil {
		return TokenSet{}, ProviderAccount{}, watchSyncRPCError()
	}
	if err := watchSyncFaultError(p.Key(), response.GetFault(), apiKey); err != nil {
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

func (p *PluginProvider) StartDeviceAuth(context.Context, ServerConfig) (DeviceAuthSession, error) {
	return DeviceAuthSession{}, errors.New("watch sync plugin does not support device authorization")
}

func (p *PluginProvider) PollDeviceAuth(context.Context, ServerConfig, DeviceAuthSession) (TokenSet, error) {
	return TokenSet{}, errors.New("watch sync plugin does not support device authorization")
}

func (p *PluginProvider) RefreshToken(ctx context.Context, _ ServerConfig, conn Connection) (TokenSet, error) {
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return TokenSet{}, watchSyncUnavailableError()
	}
	response, err := client.RefreshCredentials(ctx, &pluginv1.WatchSyncRefreshCredentialsRequest{
		Context: p.authenticatedContext(conn),
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
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return ProviderAccount{}, watchSyncUnavailableError()
	}
	response, err := client.GetAccount(ctx, &pluginv1.WatchSyncGetAccountRequest{
		Context: p.authenticatedContext(conn),
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
	response, err := client.ApplyEvents(ctx, &pluginv1.WatchSyncApplyEventsRequest{
		Context: p.authenticatedContext(conn),
		Events:  events,
	})
	if err != nil {
		return result, watchSyncRPCError()
	}
	if response.GetUpdatedCredentials() != nil {
		return result, retryableProviderError{message: "watch sync plugin credential rotation during event application is not supported"}
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

func (p *PluginProvider) Start(context.Context, ServerConfig, Connection, ScrobbleEvent) error {
	return nil
}

func (p *PluginProvider) Pause(context.Context, ServerConfig, Connection, ScrobbleEvent) error {
	return nil
}

func (p *PluginProvider) Stop(ctx context.Context, _ ServerConfig, conn Connection, event ScrobbleEvent) error {
	if !event.Completed {
		return nil
	}
	watchEvent := watchEventFromScrobble(event)
	if !p.supportsMedia(watchEvent.GetMedia().GetMediaType()) {
		return watchSyncProviderFaultError{message: unsupportedWatchSyncMediaMessage(watchEvent.GetMedia().GetMediaType())}
	}
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return watchSyncUnavailableError()
	}
	response, err := client.ApplyEvents(ctx, &pluginv1.WatchSyncApplyEventsRequest{
		Context: p.authenticatedContext(conn),
		Events:  []*pluginv1.WatchSyncEvent{watchEvent},
	})
	if err != nil {
		return watchSyncRPCError()
	}
	if response.GetUpdatedCredentials() != nil {
		return retryableProviderError{message: "watch sync plugin credential rotation during event application is not supported"}
	}
	if err := watchSyncFaultError(p.Key(), response.GetFault(), conn.AccessToken, conn.RefreshToken); err != nil {
		return err
	}
	result := resultForEvent(response.GetResults(), watchEvent.GetEventId())
	switch result.GetStatus() {
	case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED,
		pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_NO_CHANGE:
		return nil
	case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED:
		return errors.New(safeApplyMessage(result, conn.AccessToken, conn.RefreshToken))
	case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY:
		if fault := result.GetFault(); fault != nil &&
			fault.GetCode() == pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_RATE_LIMITED {
			retry := time.Duration(0)
			if fault.GetRetryAfter() != nil {
				retry = fault.GetRetryAfter().AsDuration()
			}
			return RateLimitedError{Provider: p.Key(), RetryAfter: retry}
		}
		return fmt.Errorf("%s watch sync event needs retry: %s", p.Key(), safeApplyMessage(result, conn.AccessToken, conn.RefreshToken))
	default:
		return fmt.Errorf("%s watch sync plugin omitted a valid result for event %s", p.Key(), watchEvent.GetEventId())
	}
}

func (p *PluginProvider) ScrobbleOrderingKey(conn Connection, event ScrobbleEvent) string {
	seriesID := firstNonEmptyWatchID(event.SeriesTVDBID, event.SeriesTMDBID, event.SeriesIMDbID, event.MediaItemID)
	return "plugin-watch-sync:" + conn.ID + ":" + seriesID
}

func (p *PluginProvider) authenticatedContext(conn Connection) *pluginv1.WatchSyncAuthenticatedContext {
	return &pluginv1.WatchSyncAuthenticatedContext{
		CapabilityId: p.capabilityID,
		Credentials:  credentialsFromConnection(conn),
	}
}

func watchEventFromLocalPlay(play LocalPlay, origin pluginv1.WatchSyncOrigin) *pluginv1.WatchSyncEvent {
	return &pluginv1.WatchSyncEvent{
		EventId:         play.HistoryID,
		Operation:       pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_MARK_WATCHED,
		Origin:          origin,
		OccurredAt:      timestamppb.New(play.WatchedAt),
		WatchHistoryId:  play.HistoryID,
		DurationSeconds: play.DurationSeconds,
		Media: mediaFromIdentity(play.MediaItemID, play.Kind, play.Title, play.Year,
			play.IMDbID, play.TMDBID, play.TVDBID, play.SeriesTitle, play.SeriesYear,
			play.SeriesIMDbID, play.SeriesTMDBID, play.SeriesTVDBID, play.SeasonNumber, play.EpisodeNumber),
	}
}

func watchEventFromScrobble(event ScrobbleEvent) *pluginv1.WatchSyncEvent {
	eventID := event.HistoryID
	if eventID == "" {
		eventID = "scrobble:" + event.PlaybackSessionID
	}
	completion := 0.0
	if event.DurationSeconds > 0 {
		completion = event.PositionSeconds / event.DurationSeconds * 100
	}
	return &pluginv1.WatchSyncEvent{
		EventId:           eventID,
		Operation:         pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_MARK_WATCHED,
		Origin:            pluginv1.WatchSyncOrigin_WATCH_SYNC_ORIGIN_PLAYBACK_COMPLETION,
		OccurredAt:        timestamppb.New(event.OccurredAt),
		WatchHistoryId:    event.HistoryID,
		PlaybackSessionId: event.PlaybackSessionID,
		PositionSeconds:   event.PositionSeconds,
		DurationSeconds:   event.DurationSeconds,
		CompletionPercent: completion,
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
	credentials := &pluginv1.WatchSyncCredentials{AccessToken: conn.AccessToken, RefreshToken: conn.RefreshToken}
	if conn.TokenExpiresAt != nil {
		credentials.ExpiresAt = timestamppb.New(*conn.TokenExpiresAt)
	}
	return credentials
}

func tokenSetFromProto(credentials *pluginv1.WatchSyncCredentials) (TokenSet, error) {
	if credentials == nil || strings.TrimSpace(credentials.GetAccessToken()) == "" {
		return TokenSet{}, errors.New("watch sync plugin returned no access token")
	}
	tokenType := strings.TrimSpace(credentials.GetTokenType())
	if (tokenType != "" && !strings.EqualFold(tokenType, "Bearer")) || len(credentials.GetScopes()) > 0 || len(credentials.GetSecretAttributes()) > 0 {
		return TokenSet{}, errors.New("watch sync plugin returned a credential shape unsupported by the initial server adapter")
	}
	var expiresAt *time.Time
	if credentials.GetExpiresAt() != nil {
		value := credentials.GetExpiresAt().AsTime()
		expiresAt = &value
	}
	return TokenSet{AccessToken: credentials.GetAccessToken(), RefreshToken: credentials.GetRefreshToken(), TokenExpiresAt: expiresAt}, nil
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

func supportsWatchSyncAPIKey(descriptor *pluginv1.WatchSyncProviderDescriptor) bool {
	for _, method := range descriptor.GetAuthMethods() {
		if method == pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY {
			return true
		}
	}
	return false
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
