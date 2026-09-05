package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// Leaf item types whose progress is per item rather than aggregated.
const (
	itemTypeMovie     = "movie"
	itemTypeEpisode   = "episode"
	itemTypeEbook     = "ebook"
	itemTypeAudiobook = "audiobook"
)

// WatchedStateView is what marking an item watched or unwatched answers:
// the target, the kind it resolved to, and how many leaf items changed.
type WatchedStateView = watchedStateResponse

// WatchDetail resolves the playable detail v1 GET /watch/{id} answers, with
// the profile's progress on a leaf item folded in (none when profileID is
// empty: the route does not require a profile). A series is not directly
// playable (400 invalid_watch_target); an unknown or inaccessible target is
// 404. The v1 handler and the v2 operation share it.
func (h *ItemsHandler) WatchDetail(ctx context.Context, userID int, profileID, contentID string, filter catalog.AccessFilter) (*catalog.WatchDetail, error) {
	detail, err := h.detailSvc.GetWatchDetail(ctx, contentID, filter)
	if err != nil {
		switch {
		case catalog.IsWatchTargetNotPlayable(err):
			return nil, apiError(http.StatusBadRequest, "invalid_watch_target", "Content is not directly playable")
		case isNotFound(err):
			return nil, apiError(http.StatusNotFound, "not_found", "Watch target not found")
		default:
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to get watch detail")
		}
	}

	if detail.Type == itemTypeMovie || detail.Type == itemTypeEpisode || detail.Type == itemTypeEbook || detail.Type == itemTypeAudiobook {
		detail.UserData = h.leafUserData(ctx, userID, profileID, detail.ContentID, detail.Type)
		applyEffectiveEditionPreference(detail.UserData, &detail.EffectiveVersionEditionKey)
	}
	return detail, nil
}

// SetWatchedState marks a movie, ebook, episode, season or series watched
// (played) or unwatched for the profile; a season or series expands to its
// episodes and an ebook's read state lives in the reader-progress store. It
// is the command behind v1 POST/DELETE /watched/{id} and the v2 operations.
func (h *ItemsHandler) SetWatchedState(ctx context.Context, userID int, profileID, contentID string, played bool, filter catalog.AccessFilter) (WatchedStateView, error) {
	targetType, targets, err := h.resolveWatchedTargets(ctx, contentID, filter)
	if err != nil {
		if isNotFound(err) {
			return WatchedStateView{}, apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return WatchedStateView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to update watched state")
	}

	switch {
	case targetType == itemTypeEbook:
		// Ebook read state lives in ebook_reader_progress, not in
		// user_watch_progress/user_watch_history; watch providers do not sync
		// books, so no local watch event is dispatched.
		err = h.setEbookReadState(ctx, userID, profileID, contentID, played, filter)
	case h.watchState == nil:
		return WatchedStateView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	case played:
		leafTargets := make([]watchstate.LeafWatchTarget, 0, len(targets))
		for _, target := range targets {
			leafTargets = append(leafTargets, watchstate.LeafWatchTarget{
				MediaItemID:     target.ContentID,
				DurationSeconds: target.DurationSeconds,
			})
		}
		updatedAt := time.Now().UTC()
		var result watchstate.ManualMarkResult
		result, err = h.watchState.RecordManualMarkWatchedWithResult(ctx, userID, profileID, leafTargets, updatedAt)
		if err == nil {
			h.dispatchLocalWatchEvent(ctx, watchsync.LocalWatchEventMarkedWatched, userID, profileID, result)
		}
	default:
		targetIDs := make([]string, 0, len(targets))
		for _, target := range targets {
			targetIDs = append(targetIDs, target.ContentID)
		}
		var result watchstate.ManualMarkResult
		result, err = h.watchState.RecordManualMarkUnwatchedWithResult(ctx, userID, profileID, targetIDs)
		if err == nil {
			h.dispatchLocalWatchEvent(ctx, watchsync.LocalWatchEventMarkedUnwatched, userID, profileID, result)
		}
	}
	if err != nil {
		if isNotFound(err) {
			return WatchedStateView{}, apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return WatchedStateView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to update watched state")
	}

	triggerProfileRefresh(ctx, h.profileStaler, h.profileRefreshRequester, userID, profileID)
	publishUserStateEvent(ctx, h.EventsHub, userID, profileID, contentID, "", "watched", userStateEventState{
		Played: boolPtr(played),
	})

	return WatchedStateView{
		ContentID:     contentID,
		Type:          targetType,
		AffectedCount: len(targets),
		Played:        played,
	}, nil
}
