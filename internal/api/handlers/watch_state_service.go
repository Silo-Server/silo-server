package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
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

	if detail.Type == itemTypeMovie || detail.Type == itemTypeEpisode {
		h.populateWatchMarkers(ctx, detail, filter.SelectedFileID)
	}
	if detail.Type == itemTypeMovie || detail.Type == itemTypeEpisode || detail.Type == itemTypeEbook || detail.Type == itemTypeAudiobook {
		detail.UserData = h.leafUserData(ctx, userID, profileID, detail.ContentID, detail.Type)
		applyEffectiveEditionPreference(detail.UserData, &detail.EffectiveVersionEditionKey)
	}
	h.scheduleSelectedVirtualProbe(detail.ContentID, detail.Type, detail.Versions, filter)
	return detail, nil
}

// scheduleSelectedVirtualProbe schedules the pre-play work for an explicitly
// selected virtual version: the probe chain when its track inventory has not
// been enumerated yet, and the provider fetch warm-up in every case. The
// versions are the access-filtered rows the caller is about to answer with; the
// selection is filter.SelectedFileID.
//
// Probe-on-select exists because selecting a non-default row previously left
// its audio/subtitle menus unknown until the row played. Warm-on-select exists
// because probe evidence does not mean the provider has fetched the bytes: a
// cold release (AltMount's NZB download, for example) still blocks the first
// play even when the row was probed long ago, so the explicit pick starts the
// same fill a play would. The warm-up is fire-and-forget and re-reads the row
// under the same access filter, so a bare listing or polling read (no explicit
// selection) schedules neither.
func (h *ItemsHandler) scheduleSelectedVirtualProbe(contentID, itemType string, versions []catalog.FileVersion, filter catalog.AccessFilter) {
	if h == nil || h.virtualSelectionProber == nil || contentID == "" || filter.SelectedFileID <= 0 {
		return
	}
	if itemType != itemTypeMovie && itemType != itemTypeEpisode {
		return
	}
	for i := range versions {
		version := &versions[i]
		if version.FileID != filter.SelectedFileID {
			continue
		}
		// Only a virtual row has provider bytes to enumerate and warm. A local
		// row leaves both entry points nothing to do.
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(version.FilePath)), virtualPlaybackPrefix) {
			return
		}
		// Only an unstamped row still needs the inventory probe; a probed row
		// skips it but still warms, because the probe says nothing about whether
		// the provider's copy is fetched. Scheduling the probe first lets a live
		// chain for an unprobed row cover the warm-up (it opens the same URL and
		// starts the same fetch), while a refused probe leaves the warm-up to
		// start the fetch on its own.
		//
		// A stamp is not an inventory: an evidence write that landed no tracks
		// (or whose tracks were invalidated afterwards) leaves a row stamped and
		// empty, and that row is exactly the one whose menu stays blank forever.
		// The probe chain already calls this "stamp_without_inventory" and
		// re-probes it, but this gate kept a selection from ever reaching it.
		if version.ProbedAt == nil || !versionCarriesProbeInventory(*version) {
			h.virtualSelectionProber.ScheduleSelectedFileProbe(contentID, version.FileID, filter)
		}
		h.virtualSelectionProber.ScheduleSelectedFileWarm(contentID, version.FileID, filter)
		return
	}
}

func (h *ItemsHandler) populateWatchMarkers(ctx context.Context, detail *catalog.WatchDetail, selectedFileID int) {
	if h.MarkerPopulation == nil || h.MarkerFileResolver == nil || len(detail.Versions) == 0 {
		return
	}
	if selectedFileID == 0 && len(detail.PlaybackVariants) > 0 {
		selectedFileID = detail.PlaybackVariants[0].DefaultFileID
	}
	if selectedFileID == 0 {
		selectedFileID = detail.Versions[0].FileID
	}
	// Only a version returned by the access-filtered detail may cause a lookup.
	var selected *catalog.FileVersion
	for i := range detail.Versions {
		if detail.Versions[i].FileID == selectedFileID {
			selected = &detail.Versions[i]
			break
		}
	}
	if selected == nil {
		return
	}
	file, err := h.MarkerFileResolver.GetByID(ctx, selectedFileID)
	if err != nil {
		slog.WarnContext(ctx, "watch marker file lookup failed", "file_id", selectedFileID, "error", err)
		return
	}
	file = populateFileMarkers(ctx, h.MarkerPopulation, file)
	if file == nil {
		return
	}
	selected.SetMarkers(file)
	if h.MarkerImageURLs != nil {
		selected.MarkerPreviews = catalog.BuildMarkerPreviews(ctx, file, h.MarkerImageURLs.ResolveURLs)
	}
	detail.Intro, detail.Credits, detail.Recap, detail.Preview = selected.Intro, selected.Credits, selected.Recap, selected.Preview
	for i := range detail.PlaybackVariants {
		for j := range detail.PlaybackVariants[i].Parts {
			for k := range detail.PlaybackVariants[i].Parts[j].Versions {
				version := &detail.PlaybackVariants[i].Parts[j].Versions[k]
				if version.FileID == selectedFileID {
					version.SetMarkers(file)
					version.MarkerPreviews = selected.MarkerPreviews
				}
			}
		}
	}
}

// SetWatchedState marks a movie, audiobook, ebook, episode, season or series
// watched (played) or unwatched for the profile; a season or series expands
// to its episodes and an ebook's read state lives in the reader-progress
// store. It is the command behind v1 POST/DELETE /watched/{id} and the v2
// operations. recordRewatch lets marking a watched movie, episode or
// audiobook that has a resume position record another play; it never applies
// to a season or series.
func (h *ItemsHandler) SetWatchedState(ctx context.Context, userID int, profileID, contentID string, played, recordRewatch bool, filter catalog.AccessFilter) (WatchedStateView, error) {
	targetType, seriesID, targets, err := h.resolveWatchedTargets(ctx, contentID, played, filter)
	if err != nil {
		if isNotFound(err) {
			return WatchedStateView{}, apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return WatchedStateView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to update watched state")
	}
	if !played && h.watchState != nil && (targetType == watchstate.ParentSeries || targetType == watchstate.ParentSeason) {
		var ids []string
		if targetType == watchstate.ParentSeason {
			// Expand by the season's durable identity, not its mutable row:
			// synthetic seasons have no row and partly linked/unlinked seasons
			// can carry watched file-less episodes outside the season_id group.
			seasonTarget, lookupErr := h.lookupWatchedTarget(ctx, contentID, filter)
			if lookupErr != nil || seasonTarget.Season == nil {
				return WatchedStateView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to update watched state")
			}
			ids, err = h.watchState.ParentUnwatchTargetsForSeason(ctx, userID, profileID, h.episodeRepo, seasonTarget.Season.SeriesID, seasonTarget.Season.SeasonNumber)
		} else {
			ids, err = h.watchState.ParentUnwatchTargets(ctx, userID, profileID, h.episodeRepo, targetType, contentID)
		}
		if err != nil {
			return WatchedStateView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to update watched state")
		}
		for _, id := range ids {
			targets = append(targets, watchedLeafTarget{ContentID: id})
		}
		// A partly linked season can return the same available episode from
		// both the resolved leaf set and the parent expansion. PostgreSQL
		// de-duplicates history deletes, but AffectedCount must count distinct
		// episodes too.
		targets = dedupeWatchedLeafTargets(targets)
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
		err = h.markLeafTargetsWatched(ctx, userID, profileID, targets,
			recordRewatch && (targetType == itemTypeMovie || targetType == itemTypeEpisode || targetType == itemTypeAudiobook))
	default:
		err = h.markLeafTargetsUnwatched(ctx, userID, profileID, targets)
	}
	if err != nil {
		if isNotFound(err) {
			return WatchedStateView{}, apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return WatchedStateView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to update watched state")
	}

	notifySignalsChanged(ctx, h.signalsNotifier, userID, profileID)
	publishUserStateEvent(ctx, h.EventsHub, userID, profileID, contentID, seriesID, "watched", userStateEventState{
		Played: boolPtr(played),
	})

	return WatchedStateView{
		ContentID:     contentID,
		Type:          targetType,
		AffectedCount: len(targets),
		Played:        played,
	}, nil
}

// ClearResumePosition clears the profile's resume position on a movie or
// episode and keeps its watched state and watch history; an item without a
// resume position is a no-op. A season, series or ebook has no single resume
// position (400 invalid_watch_target); an unknown or inaccessible item is 404,
// and so is an audiobook, which this operation does not cover.
// It is the command behind the v2 operation; the frozen v1 API has none.
func (h *ItemsHandler) ClearResumePosition(ctx context.Context, userID int, profileID, contentID string, filter catalog.AccessFilter) error {
	// Only the target's type is needed: a season or series is rejected
	// without loading its episodes.
	target, err := h.lookupWatchedTarget(ctx, contentID, filter)
	if err != nil {
		if isNotFound(err) {
			return apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to clear resume position")
	}
	if target.Type == itemTypeAudiobook {
		// The resolver finds audiobooks for mark watched; this operation
		// keeps answering them as it did before it did.
		return apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	if target.Type != itemTypeMovie && target.Type != itemTypeEpisode {
		return apiError(http.StatusBadRequest, "invalid_watch_target", "Only a movie or an episode has a resume position")
	}
	if h.watchState == nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	cleared, err := h.watchState.ClearResumePosition(ctx, userID, profileID, contentID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to clear resume position")
	}
	if !cleared {
		return nil
	}
	notifySignalsChanged(ctx, h.signalsNotifier, userID, profileID)
	publishUserStateEvent(ctx, h.EventsHub, userID, profileID, contentID, target.seriesID(), userStateChangeProgress, userStateEventState{})
	return nil
}

// markLeafTargetsWatched records a manual play for every leaf target the
// profile has not already completed and queues one outbound provider event
// for the plays it recorded. A target already completed is a no-op: no
// history row, no event, so a retried mark never double-counts a play. With
// recordRewatch, a completed target that has a resume position records
// another play instead.
func (h *ItemsHandler) markLeafTargetsWatched(ctx context.Context, userID int, profileID string, targets []watchedLeafTarget, recordRewatch bool) error {
	leafTargets := make([]watchstate.LeafWatchTarget, 0, len(targets))
	for _, target := range targets {
		leafTargets = append(leafTargets, watchstate.LeafWatchTarget{
			MediaItemID:     target.ContentID,
			DurationSeconds: target.DurationSeconds,
			RecordRewatch:   recordRewatch,
		})
	}
	result, err := h.watchState.RecordManualMarkWatchedWithResult(ctx, userID, profileID, leafTargets, time.Now().UTC())
	if err != nil {
		return err
	}
	h.dispatchLocalWatchEvent(ctx, watchsync.LocalWatchEventMarkedWatched, userID, profileID, result)
	return nil
}

// markLeafTargetsUnwatched clears the targets' watched state and progress.
// Their plays stay in watch history, here and on watch providers.
func (h *ItemsHandler) markLeafTargetsUnwatched(ctx context.Context, userID int, profileID string, targets []watchedLeafTarget) error {
	targetIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		targetIDs = append(targetIDs, target.ContentID)
	}
	return h.watchState.RecordManualMarkUnwatched(ctx, userID, profileID, targetIDs)
}

// versionCarriesProbeInventory reports whether a version row has any track
// evidence to show. A row can be stamped without it (see the selection probe
// gate): the stamp says a probe ran, not that its evidence was adopted.
func versionCarriesProbeInventory(version catalog.FileVersion) bool {
	return len(version.VideoTracks) > 0 || len(version.AudioTracks) > 0 || len(version.SubtitleTracks) > 0
}
