package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// droppedSeriesLister reads a profile's dropped series (catalog.DroppedSeriesRepo).
type droppedSeriesLister interface {
	ListDropped(ctx context.Context, userID int, profileID string, seriesIDs []string) ([]catalog.DroppedSeries, error)
}

// homeHides is what a profile removed from Home for one series. The
// continue_watching and next_up interest reasons follow those removals, so a
// series the profile removed from a surface stops notifying for that reason.
// Favorites and watchlist are unaffected.
type homeHides struct {
	// dropped is an active series drop, which hides the series from both
	// surfaces until the profile watches it again.
	dropped bool
	// continueWatching holds the profile's per-card Continue Watching
	// dismissals, each valid only for the progress stamp it captured.
	continueWatching catalog.HomeDismissalIndex
	// nextUp holds the items the profile dismissed from Next Up.
	nextUp map[string]struct{}
}

// continueWatchingVisible reports whether any in-progress episode of the
// series still shows in Continue Watching.
func (h homeHides) continueWatchingVisible(inProgress []userstore.WatchProgress) bool {
	return !h.dropped && len(h.continueWatching.FilterProgress(inProgress)) > 0
}

// dismissesNextUpIn reports whether any episode of the series was dismissed
// from Next Up, which is when the Next Up episode has to be looked up.
func (h homeHides) dismissesNextUpIn(episodeKeys map[string]int) bool {
	if len(h.nextUp) == 0 {
		return false
	}
	for id := range h.nextUp {
		if _, ok := episodeKeys[id]; ok {
			return true
		}
	}
	return false
}

// nextUpVisible reports whether the series still shows in Next Up, given the
// episode Home would show (nextUpEpisode). Like the Home fetcher
// (sections.Fetcher.filterNextUpDismissals), a per-card dismissal hides the
// card only while its episode is still the one shown; watching on, or
// un-watching an earlier episode, moves Next Up to another episode.
func (h homeHides) nextUpVisible(nextEpisodeID string) bool {
	if h.dropped {
		return false
	}
	_, dismissed := h.nextUp[nextEpisodeID]
	return !dismissed
}

// nextUpEpisode returns the episode Home's Next Up shows at the progression
// cursor: the first episode at or after next that has a present file and
// that the profile has not started. It is empty when there is none. Home
// anchors on the most recently completed episode rather than the highest, so
// the two differ only after a rewatch of an earlier episode.
func (u *InterestUpdater) nextUpEpisode(ctx context.Context, seriesID string, next int, started []string) (string, error) {
	var episodeID string
	err := u.pool.QueryRow(ctx, `
		SELECT e.content_id FROM episodes e
		WHERE e.series_id = $1
		  AND `+availabilityOrdinalGuard+`
		  AND `+availabilityKeyExpr+` >= $2
		  AND NOT (e.content_id = ANY($3))
		  AND EXISTS (
			SELECT 1 FROM media_files mf
			WHERE mf.episode_id = e.content_id AND mf.missing_since IS NULL)
		ORDER BY e.season_number, e.episode_number, e.content_id
		LIMIT 1`, seriesID, next, started).Scan(&episodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find next up episode: %w", err)
	}
	return episodeID, nil
}

// homeDismissals caches a profile's Home dismissals by surface. A nil cache
// reads the store on every call; a rebuild shares one per profile so each
// list is read once, not once per series.
type homeDismissals struct {
	continueWatching catalog.HomeDismissalIndex
	nextUp           map[string]struct{}
	loadedCW         bool
	loadedNextUp     bool
}

func (d *homeDismissals) continueWatchingIndex(ctx context.Context, store userstore.UserStore, profileID string) (catalog.HomeDismissalIndex, error) {
	if d != nil && d.loadedCW {
		return d.continueWatching, nil
	}
	dismissals, err := store.ListHomeDismissals(ctx, profileID, userstore.HomeSurfaceContinueWatching)
	if err != nil {
		return nil, fmt.Errorf("load continue watching dismissals: %w", err)
	}
	index := catalog.NewHomeDismissalIndex(dismissals)
	if d != nil {
		d.continueWatching, d.loadedCW = index, true
	}
	return index, nil
}

func (d *homeDismissals) nextUpItems(ctx context.Context, store userstore.UserStore, profileID string) (map[string]struct{}, error) {
	if d != nil && d.loadedNextUp {
		return d.nextUp, nil
	}
	dismissals, err := store.ListHomeDismissals(ctx, profileID, userstore.HomeSurfaceNextUp)
	if err != nil {
		return nil, fmt.Errorf("load next up dismissals: %w", err)
	}
	items := make(map[string]struct{}, len(dismissals))
	for _, dismissal := range dismissals {
		items[dismissal.MediaItemID] = struct{}{}
	}
	if d != nil {
		d.nextUp, d.loadedNextUp = items, true
	}
	return items, nil
}

// loadHomeHides reads the profile's Home removals that apply to the series.
// A dismissal list is read only for a surface that could show the series:
// continueWatching when an episode is in progress, nextUp when the profile
// has a progression cursor.
func (u *InterestUpdater) loadHomeHides(ctx context.Context, store userstore.UserStore, dismissals *homeDismissals, userID int, profileID, seriesID string, continueWatching, nextUp bool) (homeHides, error) {
	var hides homeHides
	if u.drops != nil {
		drops, err := u.drops.ListDropped(ctx, userID, profileID, []string{seriesID})
		if err != nil {
			return homeHides{}, fmt.Errorf("load dropped series: %w", err)
		}
		for _, drop := range drops {
			if drop.Active {
				// A drop hides both surfaces; per-card dismissals add nothing.
				return homeHides{dropped: true}, nil
			}
		}
	}

	var err error
	if continueWatching {
		if hides.continueWatching, err = dismissals.continueWatchingIndex(ctx, store, profileID); err != nil {
			return homeHides{}, err
		}
	}
	if nextUp {
		if hides.nextUp, err = dismissals.nextUpItems(ctx, store, profileID); err != nil {
			return homeHides{}, err
		}
	}
	return hides, nil
}

// queueRemoval queues a recompute for a Home removal now and once more after
// progressSessionGap. Resuming playback lifts a removal without changing any
// progress state. A resume more than progressSessionGap after the removal
// starts a new watch session, which queues its own recompute; the second
// recompute here catches a quicker one.
func (u *InterestUpdater) queueRemoval(userID int, profileID, itemID string) {
	u.QueueItemMutation(userID, profileID, itemID)
	u.QueueItemMutationAfter(userID, profileID, itemID, progressSessionGap)
}

// DroppedSeriesTracker decorates the dropped-series store so every change to
// a profile's drops recomputes its interest in that series. The Home
// dismissal handler and watch-provider sync both write drops through it.
type DroppedSeriesTracker struct {
	*catalog.DroppedSeriesRepo
	updater *InterestUpdater
}

// TrackDroppedSeries wraps repo. A nil system tracks nothing, which keeps
// wiring without notifications unchanged.
func TrackDroppedSeries(repo *catalog.DroppedSeriesRepo, system *System) *DroppedSeriesTracker {
	tracker := &DroppedSeriesTracker{DroppedSeriesRepo: repo}
	if system != nil {
		tracker.updater = system.Interest
	}
	return tracker
}

// Drop drops the series and queues an interest recompute.
func (t *DroppedSeriesTracker) Drop(ctx context.Context, userID int, profileID, seriesID string) error {
	if err := t.DroppedSeriesRepo.Drop(ctx, userID, profileID, seriesID); err != nil {
		return err
	}
	t.updater.queueRemoval(userID, profileID, seriesID)
	return nil
}

// Undrop removes the drop and queues an interest recompute.
func (t *DroppedSeriesTracker) Undrop(ctx context.Context, userID int, profileID, seriesID string) error {
	if err := t.DroppedSeriesRepo.Undrop(ctx, userID, profileID, seriesID); err != nil {
		return err
	}
	t.updater.QueueItemMutation(userID, profileID, seriesID)
	return nil
}

// ImportDrop records a provider's drop and queues an interest recompute when
// the write applied.
func (t *DroppedSeriesTracker) ImportDrop(ctx context.Context, userID int, profileID, seriesID string, droppedAt time.Time, observed *time.Time) (bool, error) {
	applied, err := t.DroppedSeriesRepo.ImportDrop(ctx, userID, profileID, seriesID, droppedAt, observed)
	if err == nil && applied {
		t.updater.queueRemoval(userID, profileID, seriesID)
	}
	return applied, err
}

// DeleteIfUnchanged removes a drop the provider no longer holds and queues an
// interest recompute when a row was removed.
func (t *DroppedSeriesTracker) DeleteIfUnchanged(ctx context.Context, userID int, profileID, seriesID string, observed time.Time) (bool, error) {
	removed, err := t.DroppedSeriesRepo.DeleteIfUnchanged(ctx, userID, profileID, seriesID, observed)
	if err == nil && removed {
		t.updater.QueueItemMutation(userID, profileID, seriesID)
	}
	return removed, err
}
