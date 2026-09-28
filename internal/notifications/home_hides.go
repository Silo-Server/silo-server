package notifications

import (
	"context"
	"fmt"
	"time"

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
	// nextUp holds the series episodes the profile dismissed from Next Up.
	nextUp map[string]struct{}
}

// continueWatchingVisible reports whether any in-progress episode of the
// series still shows in Continue Watching.
func (h homeHides) continueWatchingVisible(inProgress []userstore.WatchProgress) bool {
	return !h.dropped && len(h.continueWatching.FilterProgress(inProgress)) > 0
}

// nextUpVisible reports whether the series still shows in Next Up, given the
// episode that is next for the profile. Like the Home fetcher
// (sections.Fetcher.filterNextUpDismissals), a per-card dismissal hides the
// card only while the dismissed episode is still next; completing it, or
// un-watching an earlier one, moves Next Up on.
func (h homeHides) nextUpVisible(nextEpisodeID string) bool {
	if h.dropped {
		return false
	}
	_, dismissed := h.nextUp[nextEpisodeID]
	return !dismissed
}

// nextEpisodeAt returns the series episode at the progression cursor: the
// lowest episode key at or after next, ties broken by content ID. It is empty
// when there is no cursor or the profile is caught up.
func nextEpisodeAt(episodeKeys map[string]int, next *int) string {
	if next == nil {
		return ""
	}
	nextID, nextKey := "", 0
	for id, key := range episodeKeys {
		if key < *next {
			continue
		}
		if nextID == "" || key < nextKey || (key == nextKey && id < nextID) {
			nextID, nextKey = id, key
		}
	}
	return nextID
}

// loadHomeHides reads the profile's Home removals that apply to the series.
// Dismissal lists are read only for a surface that could show the series:
// continueWatching when an episode is in progress, nextUp when the profile
// has a progression cursor.
func (u *InterestUpdater) loadHomeHides(ctx context.Context, store userstore.UserStore, userID int, profileID, seriesID string, episodeKeys map[string]int, continueWatching, nextUp bool) (homeHides, error) {
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

	if continueWatching {
		dismissals, err := store.ListHomeDismissals(ctx, profileID, userstore.HomeSurfaceContinueWatching)
		if err != nil {
			return homeHides{}, fmt.Errorf("load continue watching dismissals: %w", err)
		}
		hides.continueWatching = catalog.NewHomeDismissalIndex(dismissals)
	}
	if nextUp {
		dismissals, err := store.ListHomeDismissals(ctx, profileID, userstore.HomeSurfaceNextUp)
		if err != nil {
			return homeHides{}, fmt.Errorf("load next up dismissals: %w", err)
		}
		for _, dismissal := range dismissals {
			// Next Up cards are episodes; Home matches a dismissal by the
			// card's content ID.
			if _, ok := episodeKeys[dismissal.MediaItemID]; !ok {
				continue
			}
			if hides.nextUp == nil {
				hides.nextUp = make(map[string]struct{})
			}
			hides.nextUp[dismissal.MediaItemID] = struct{}{}
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
