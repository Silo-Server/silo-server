package watchsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	pluginWatchedCursorKey   = "plugin.remote.watched"
	pluginProgressCursorKey  = "plugin.remote.progress"
	pluginFavoritesCursorKey = "plugin.remote.favorites"
	pluginWatchlistCursorKey = "plugin.remote.watchlist"
	maxRemoteStatePages      = 10_000
	maxRemoteStateItems      = 100_000
)

type pluginRemoteTraversal struct {
	items            []*pluginv1.WatchSyncRemoteState
	nextCursor       string
	completeSnapshot bool
	warnings         []string
}

func (p *PluginProvider) FetchWatched(
	ctx context.Context,
	cfg ServerConfig,
	conn Connection,
) ([]RemoteWatch, error) {
	batch, err := p.FetchWatchedBatch(ctx, cfg, conn)
	return batch.Rows, err
}

func (p *PluginProvider) FetchWatchedBatch(
	ctx context.Context,
	_ ServerConfig,
	conn Connection,
) (WatchedImportBatch, error) {
	traversal, err := p.listRemoteState(ctx, conn, pluginWatchedCursorKey,
		pluginv1.WatchSyncRemoteStateKind_WATCH_SYNC_REMOTE_STATE_KIND_WATCHED)
	if err != nil {
		return WatchedImportBatch{}, err
	}
	batch := WatchedImportBatch{
		UpdatedCursors: cursorUpdate(pluginWatchedCursorKey, traversal.nextCursor),
		Warnings:       traversal.warnings,
	}
	for _, state := range traversal.items {
		if state.GetWatched() == nil {
			continue
		}
		row, err := remoteWatchFromProto(p.Key(), state)
		if err != nil {
			batch.Warnings = append(batch.Warnings, err.Error())
			continue
		}
		batch.Rows = append(batch.Rows, row)
	}
	return batch, nil
}

func (p *PluginProvider) FetchProgress(
	ctx context.Context,
	cfg ServerConfig,
	conn Connection,
) ([]RemoteProgress, error) {
	batch, err := p.FetchProgressBatch(ctx, cfg, conn)
	return batch.Rows, err
}

func (p *PluginProvider) FetchProgressBatch(
	ctx context.Context,
	_ ServerConfig,
	conn Connection,
) (ProgressImportBatch, error) {
	traversal, err := p.listRemoteState(ctx, conn, pluginProgressCursorKey,
		pluginv1.WatchSyncRemoteStateKind_WATCH_SYNC_REMOTE_STATE_KIND_PROGRESS)
	if err != nil {
		return ProgressImportBatch{}, err
	}
	batch := ProgressImportBatch{
		UpdatedCursors: cursorUpdate(pluginProgressCursorKey, traversal.nextCursor),
		Warnings:       traversal.warnings,
	}
	for _, state := range traversal.items {
		if state.GetProgress() == nil {
			continue
		}
		row, err := remoteProgressFromProto(p.Key(), state)
		if err != nil {
			batch.Warnings = append(batch.Warnings, err.Error())
			continue
		}
		batch.Rows = append(batch.Rows, row)
	}
	return batch, nil
}

func (p *PluginProvider) FetchFavorites(
	ctx context.Context,
	cfg ServerConfig,
	conn Connection,
) ([]RemoteFavorite, error) {
	batch, err := p.FetchFavoritesBatch(ctx, cfg, conn)
	return batch.Rows, err
}

func (p *PluginProvider) FetchFavoritesBatch(
	ctx context.Context,
	_ ServerConfig,
	conn Connection,
) (FavoriteImportBatch, error) {
	return p.fetchListState(ctx, conn, pluginFavoritesCursorKey,
		pluginv1.WatchSyncRemoteStateKind_WATCH_SYNC_REMOTE_STATE_KIND_FAVORITE)
}

func (p *PluginProvider) FetchWatchlist(
	ctx context.Context,
	cfg ServerConfig,
	conn Connection,
) ([]RemoteFavorite, error) {
	batch, err := p.FetchWatchlistBatch(ctx, cfg, conn)
	return batch.Rows, err
}

func (p *PluginProvider) FetchWatchlistBatch(
	ctx context.Context,
	_ ServerConfig,
	conn Connection,
) (FavoriteImportBatch, error) {
	return p.fetchListState(ctx, conn, pluginWatchlistCursorKey,
		pluginv1.WatchSyncRemoteStateKind_WATCH_SYNC_REMOTE_STATE_KIND_WATCHLIST)
}

func (p *PluginProvider) fetchListState(
	ctx context.Context,
	conn Connection,
	cursorKey string,
	kind pluginv1.WatchSyncRemoteStateKind,
) (FavoriteImportBatch, error) {
	traversal, err := p.listRemoteState(ctx, conn, cursorKey, kind)
	if err != nil {
		return FavoriteImportBatch{}, err
	}
	batch := FavoriteImportBatch{
		UpdatedCursors: cursorUpdate(cursorKey, traversal.nextCursor),
		Warnings:       traversal.warnings,
		Incremental:    !traversal.completeSnapshot,
	}
	for _, state := range traversal.items {
		var listed *pluginv1.WatchSyncRemoteListState
		if kind == pluginv1.WatchSyncRemoteStateKind_WATCH_SYNC_REMOTE_STATE_KIND_FAVORITE {
			listed = state.GetFavorite()
		} else {
			listed = state.GetWatchlist()
		}
		if listed == nil {
			continue
		}
		row, err := remoteFavoriteFromProto(p.Key(), state, listed)
		if err != nil {
			batch.Warnings = append(batch.Warnings, err.Error())
			continue
		}
		batch.Rows = append(batch.Rows, row)
	}
	return batch, nil
}

func (p *PluginProvider) listRemoteState(
	ctx context.Context,
	conn Connection,
	cursorKey string,
	kind pluginv1.WatchSyncRemoteStateKind,
) (pluginRemoteTraversal, error) {
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return pluginRemoteTraversal{}, watchSyncUnavailableError()
	}
	cursor := conn.SyncCursors[cursorKey]
	pageToken := ""
	seenPageTokens := make(map[string]struct{})
	result := pluginRemoteTraversal{}
	snapshotSet := false
	for page := 0; page < maxRemoteStatePages; page++ {
		authContext, err := p.authenticatedContext(ctx, conn)
		if err != nil {
			return pluginRemoteTraversal{}, err
		}
		response, err := client.ListRemoteState(ctx, &pluginv1.WatchSyncListRemoteStateRequest{
			Context:    authContext,
			Cursor:     cursor,
			PageToken:  pageToken,
			PageSize:   int32(max(1, p.ExportBatchSize())),
			StateKinds: []pluginv1.WatchSyncRemoteStateKind{kind},
		})
		if err != nil {
			return pluginRemoteTraversal{}, watchSyncRPCError()
		}
		if response.GetUpdatedCredentials() != nil {
			conn, err = p.persistUpdatedCredentials(ctx, conn, response.GetUpdatedCredentials())
			if err != nil {
				return pluginRemoteTraversal{}, err
			}
		}
		if err := watchSyncFaultError(p.Key(), response.GetFault(), conn.AccessToken, conn.RefreshToken); err != nil {
			return pluginRemoteTraversal{}, err
		}
		if kind == pluginv1.WatchSyncRemoteStateKind_WATCH_SYNC_REMOTE_STATE_KIND_WATCHLIST &&
			p.descriptor.GetProvidesWatchlistOrder() && !response.GetCompleteSnapshot() {
			return pluginRemoteTraversal{}, errors.New("watch sync plugin returned an incremental traversal for an ordered watchlist")
		}
		if snapshotSet && result.completeSnapshot != response.GetCompleteSnapshot() {
			return pluginRemoteTraversal{}, errors.New("watch sync plugin changed snapshot mode during pagination")
		}
		result.completeSnapshot = response.GetCompleteSnapshot()
		snapshotSet = true
		items := response.GetItems()
		if len(items) > maxRemoteStateItems-len(result.items) {
			return pluginRemoteTraversal{}, errors.New("watch sync plugin exceeded the remote-state item limit")
		}
		result.items = append(result.items, items...)

		nextPage := strings.TrimSpace(response.GetNextPageToken())
		if nextPage == "" {
			result.nextCursor = response.GetNextCursor()
			return result, nil
		}
		if strings.TrimSpace(response.GetNextCursor()) != "" {
			return pluginRemoteTraversal{}, errors.New("watch sync plugin returned a durable cursor before the final page")
		}
		if _, duplicate := seenPageTokens[nextPage]; duplicate {
			return pluginRemoteTraversal{}, errors.New("watch sync plugin repeated a page token")
		}
		seenPageTokens[nextPage] = struct{}{}
		pageToken = nextPage
	}
	return pluginRemoteTraversal{}, errors.New("watch sync plugin exceeded the remote-state page limit")
}

func (p *PluginProvider) RemoveHistory(
	ctx context.Context,
	_ ServerConfig,
	conn Connection,
	plays []LocalPlay,
) (ExportResult, error) {
	result := ExportResult{Failed: make(map[string]string)}
	events := make([]*pluginv1.WatchSyncEvent, 0, len(plays))
	keys := make([]string, 0, len(plays))
	for _, play := range plays {
		event := watchEventFromLocalPlay(play, pluginv1.WatchSyncOrigin_WATCH_SYNC_ORIGIN_MANUAL)
		event.EventId = "unwatched:" + play.HistoryID
		event.Operation = pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_MARK_UNWATCHED
		event.ProviderItemKey = play.ProviderItemKey
		if !p.supportsMedia(event.GetMedia().GetMediaType()) {
			result.Failed[play.HistoryID] = unsupportedWatchSyncMediaMessage(event.GetMedia().GetMediaType())
			continue
		}
		events = append(events, event)
		keys = append(keys, play.HistoryID)
	}
	applied, err := p.applyPluginEvents(ctx, conn, events, keys)
	return mergeExportFailures(applied, result.Failed), err
}

func (p *PluginProvider) ExportFavorites(ctx context.Context, _ ServerConfig, conn Connection, items []LocalFavorite) (ExportResult, error) {
	return p.applyListEvents(ctx, conn, items, pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_ADD_FAVORITE)
}

func (p *PluginProvider) RemoveFavorites(ctx context.Context, _ ServerConfig, conn Connection, items []LocalFavorite) (ExportResult, error) {
	return p.applyListEvents(ctx, conn, items, pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_REMOVE_FAVORITE)
}

func (p *PluginProvider) ExportWatchlist(ctx context.Context, _ ServerConfig, conn Connection, items []LocalFavorite) (ExportResult, error) {
	return p.applyListEvents(ctx, conn, items, pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_ADD_TO_WATCHLIST)
}

func (p *PluginProvider) RemoveWatchlist(ctx context.Context, _ ServerConfig, conn Connection, items []LocalFavorite) (ExportResult, error) {
	return p.applyListEvents(ctx, conn, items, pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_REMOVE_FROM_WATCHLIST)
}

func (p *PluginProvider) applyListEvents(
	ctx context.Context,
	conn Connection,
	items []LocalFavorite,
	operation pluginv1.WatchSyncOperation,
) (ExportResult, error) {
	result := ExportResult{Failed: make(map[string]string)}
	events := make([]*pluginv1.WatchSyncEvent, 0, len(items))
	keys := make([]string, 0, len(items))
	for _, item := range items {
		media := mediaFromIdentity(item.MediaItemID, item.Kind, item.Title, item.Year,
			item.IMDbID, item.TMDBID, item.TVDBID, "", 0,
			item.SeriesIMDbID, item.SeriesTMDBID, item.SeriesTVDBID, 0, 0)
		if !p.supportsMedia(media.GetMediaType()) {
			result.Failed[item.MediaItemID] = unsupportedWatchSyncMediaMessage(media.GetMediaType())
			continue
		}
		event := &pluginv1.WatchSyncEvent{
			EventId:         fmt.Sprintf("%s:%s", operation.String(), item.MediaItemID),
			Operation:       operation,
			Origin:          pluginv1.WatchSyncOrigin_WATCH_SYNC_ORIGIN_MANUAL,
			OccurredAt:      timestampOrNil(item.FavoritedAt),
			Media:           media,
			ProviderItemKey: item.ProviderItemKey,
		}
		if operation == pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_ADD_TO_WATCHLIST {
			position := int32(len(events))
			event.ListPosition = &position
		}
		events = append(events, event)
		keys = append(keys, item.MediaItemID)
	}
	applied, err := p.applyPluginEvents(ctx, conn, events, keys)
	return mergeExportFailures(applied, result.Failed), err
}

func (p *PluginProvider) applyPluginEvents(
	ctx context.Context,
	conn Connection,
	events []*pluginv1.WatchSyncEvent,
	keys []string,
) (ExportResult, error) {
	result, _, err := p.applyPluginEventsDetailed(ctx, conn, events, keys)
	return result, err
}

func (p *PluginProvider) applyPluginEventsDetailed(
	ctx context.Context,
	conn Connection,
	events []*pluginv1.WatchSyncEvent,
	keys []string,
) (ExportResult, map[string]pluginv1.WatchSyncApplyStatus, error) {
	result := ExportResult{Failed: make(map[string]string)}
	statuses := make(map[string]pluginv1.WatchSyncApplyStatus, len(events))
	if len(events) == 0 {
		return result, statuses, nil
	}
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return result, statuses, watchSyncUnavailableError()
	}
	for offset := 0; offset < len(events); offset += max(1, p.ExportBatchSize()) {
		end := min(len(events), offset+max(1, p.ExportBatchSize()))
		authContext, err := p.authenticatedContext(ctx, conn)
		if err != nil {
			return result, statuses, err
		}
		response, err := client.ApplyEvents(ctx, &pluginv1.WatchSyncApplyEventsRequest{
			Context: authContext,
			Events:  events[offset:end],
		})
		if err != nil {
			return result, statuses, watchSyncRPCError()
		}
		if response.GetUpdatedCredentials() != nil {
			conn, err = p.persistUpdatedCredentials(ctx, conn, response.GetUpdatedCredentials())
			if err != nil {
				return result, statuses, err
			}
		}
		if err := watchSyncFaultError(p.Key(), response.GetFault(), conn.AccessToken, conn.RefreshToken); err != nil {
			return result, statuses, err
		}
		for index, event := range events[offset:end] {
			key := keys[offset+index]
			apply := resultForEvent(response.GetResults(), event.GetEventId())
			statuses[key] = apply.GetStatus()
			switch apply.GetStatus() {
			case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED,
				pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_NO_CHANGE:
				result.Sent = append(result.Sent, key)
			case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED:
				result.NotFound = append(result.NotFound, key)
			case pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY:
				fault := apply.GetFault()
				if fault.GetCode() == pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_RATE_LIMITED {
					retry := time.Duration(0)
					if fault.GetRetryAfter() != nil {
						retry = fault.GetRetryAfter().AsDuration()
					}
					return result, statuses, RateLimitedError{Provider: p.Key(), RetryAfter: retry}
				}
				result.Failed[key] = safeApplyMessage(apply, conn.AccessToken, conn.RefreshToken)
			default:
				result.Failed[key] = "watch sync plugin omitted a valid event result"
			}
		}
	}
	return result, statuses, nil
}

func mergeExportFailures(result ExportResult, failures map[string]string) ExportResult {
	if result.Failed == nil {
		result.Failed = make(map[string]string, len(failures))
	}
	for key, message := range failures {
		result.Failed[key] = message
	}
	return result
}

func remoteWatchFromProto(provider string, state *pluginv1.WatchSyncRemoteState) (RemoteWatch, error) {
	identity, err := remoteIdentityFromProto(state)
	if err != nil {
		return RemoteWatch{}, err
	}
	watched := state.GetWatched()
	if watched.GetPlayCount() < 1 {
		return RemoteWatch{}, errors.New("watch sync plugin returned watched state with no plays")
	}
	return RemoteWatch{
		Provider: provider, ProviderItemKey: state.GetProviderItemKey(),
		Kind: identity.kind, Title: identity.title, Year: identity.year,
		IMDbID: identity.imdbID, TMDBID: identity.tmdbID, TVDBID: identity.tvdbID,
		SeriesTitle: identity.seriesTitle, SeriesYear: identity.seriesYear,
		SeriesIMDbID: identity.seriesIMDbID, SeriesTMDBID: identity.seriesTMDBID, SeriesTVDBID: identity.seriesTVDBID,
		SeasonNumber: identity.season, EpisodeNumber: identity.episode,
		PlayCount: int(watched.GetPlayCount()), LastWatchedAt: timePointer(watched.GetLastWatchedAt()),
	}, nil
}

func remoteProgressFromProto(provider string, state *pluginv1.WatchSyncRemoteState) (RemoteProgress, error) {
	identity, err := remoteIdentityFromProto(state)
	if err != nil {
		return RemoteProgress{}, err
	}
	progress := state.GetProgress()
	if progress.GetProgressPercent() < 0 || progress.GetProgressPercent() >= 100 {
		return RemoteProgress{}, errors.New("watch sync plugin returned progress outside [0,100)")
	}
	pausedAt := timePointer(progress.GetPausedAt())
	if pausedAt == nil {
		return RemoteProgress{}, errors.New("watch sync plugin returned progress without a valid timestamp")
	}
	return RemoteProgress{
		Provider: provider, ProviderItemKey: state.GetProviderItemKey(),
		Kind: identity.kind, Title: identity.title, Year: identity.year,
		IMDbID: identity.imdbID, TMDBID: identity.tmdbID, TVDBID: identity.tvdbID,
		SeriesTitle: identity.seriesTitle, SeriesYear: identity.seriesYear,
		SeriesIMDbID: identity.seriesIMDbID, SeriesTMDBID: identity.seriesTMDBID, SeriesTVDBID: identity.seriesTVDBID,
		SeasonNumber: identity.season, EpisodeNumber: identity.episode,
		ProgressPercent: progress.GetProgressPercent(), PausedAt: *pausedAt,
	}, nil
}

func remoteFavoriteFromProto(provider string, state *pluginv1.WatchSyncRemoteState, listed *pluginv1.WatchSyncRemoteListState) (RemoteFavorite, error) {
	if state == nil || listed == nil {
		return RemoteFavorite{}, errors.New("watch sync plugin returned list state without identity")
	}
	providerItemKey := strings.TrimSpace(state.GetProviderItemKey())
	if listed.GetRemoved() {
		if providerItemKey == "" {
			return RemoteFavorite{}, errors.New("watch sync plugin returned a list tombstone without provider identity")
		}
		return RemoteFavorite{
			Provider:        provider,
			ProviderItemKey: providerItemKey,
			Removed:         true,
		}, nil
	}
	identity, err := remoteIdentityFromProto(state)
	if err != nil {
		return RemoteFavorite{}, err
	}
	listedAt := time.Now().UTC()
	if value := timePointer(listed.GetListedAt()); value != nil {
		listedAt = *value
	}
	return RemoteFavorite{
		Provider: provider, ProviderItemKey: providerItemKey,
		Kind: identity.kind, Title: identity.title, Year: identity.year,
		IMDbID: identity.imdbID, TMDBID: identity.tmdbID, TVDBID: identity.tvdbID,
		SeriesTitle: identity.seriesTitle, SeriesYear: identity.seriesYear,
		SeriesIMDbID: identity.seriesIMDbID, SeriesTMDBID: identity.seriesTMDBID, SeriesTVDBID: identity.seriesTVDBID,
		SeasonNumber: identity.season, EpisodeNumber: identity.episode, FavoritedAt: listedAt,
	}, nil
}

type remoteIdentity struct {
	kind, title, imdbID, tmdbID, tvdbID                   string
	seriesTitle, seriesIMDbID, seriesTMDBID, seriesTVDBID string
	year, seriesYear, season, episode                     int
}

func remoteIdentityFromProto(state *pluginv1.WatchSyncRemoteState) (remoteIdentity, error) {
	if state == nil || state.GetMedia() == nil || strings.TrimSpace(state.GetProviderItemKey()) == "" {
		return remoteIdentity{}, errors.New("watch sync plugin returned remote state without identity")
	}
	media := state.GetMedia()
	var kind string
	switch media.GetMediaType() {
	case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE:
		kind = historyimport.KindMovie
	case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE:
		kind = historyimport.KindEpisode
	default:
		return remoteIdentity{}, errors.New("watch sync plugin returned unsupported remote media")
	}
	return remoteIdentity{
		kind: kind, title: media.GetTitle(), year: int(media.GetYear()),
		imdbID: media.GetExternalIds()["imdb"], tmdbID: media.GetExternalIds()["tmdb"], tvdbID: media.GetExternalIds()["tvdb"],
		seriesTitle: media.GetSeriesTitle(), seriesYear: int(media.GetSeriesYear()),
		seriesIMDbID: media.GetSeriesExternalIds()["imdb"], seriesTMDBID: media.GetSeriesExternalIds()["tmdb"], seriesTVDBID: media.GetSeriesExternalIds()["tvdb"],
		season: int(media.GetSeasonNumber()), episode: int(media.GetEpisodeNumber()),
	}, nil
}

func cursorUpdate(key, value string) map[string]string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return map[string]string{key: value}
}

func timePointer(value interface {
	AsTime() time.Time
	CheckValid() error
}) *time.Time {
	if value == nil || value.CheckValid() != nil {
		return nil
	}
	result := value.AsTime()
	return &result
}

func timestampOrNil(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}
