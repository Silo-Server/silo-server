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

// completedEpisode is a completed progress row of the series.
type completedEpisode struct {
	key int
	at  time.Time
}

// supersededInProgress returns the in-progress episodes Home's Continue
// Watching hides because a later episode of the series was completed more
// recently: the viewer moved past them. It applies the rule of
// catalog.ContinueWatchingProgressFilter to the series' progress the
// recompute already holds.
func supersededInProgress(inProgress []userstore.WatchProgress, completed []completedEpisode, episodeKeys map[string]int) map[string]struct{} {
	superseded := make(map[string]struct{})
	for _, entry := range inProgress {
		key, ok := episodeKeys[entry.MediaItemID]
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339, entry.UpdatedAt)
		if err != nil {
			continue
		}
		for _, done := range completed {
			if done.key > key && done.at.After(at) {
				superseded[entry.MediaItemID] = struct{}{}
				break
			}
		}
	}
	return superseded
}

// nextUpEpisode returns the episode Home's Next Up shows after its anchor:
// the first episode with a key of at least from that has a present file and
// that the profile has not started. "Started" is the Home query's test
// (catalog.buildListNextUpQuery's next_ep lateral: any Postgres progress row
// that is completed or has a position, including rows a history hide covers)
// joined with the started episodes the profile's own store reports, which
// covers stores that keep progress outside Postgres. It is empty when there
// is none.
func (u *InterestUpdater) nextUpEpisode(ctx context.Context, userID int, profileID, seriesID string, from int, started []string) (string, error) {
	var episodeID string
	err := u.pool.QueryRow(ctx, `
		SELECT e.content_id FROM episodes e
		WHERE e.series_id = $3
		  AND `+availabilityOrdinalGuard+`
		  AND `+availabilityKeyExpr+` >= $4
		  AND EXISTS (
			SELECT 1 FROM media_files mf
			WHERE mf.episode_id = e.content_id AND mf.missing_since IS NULL)
		  AND NOT (e.content_id = ANY($5))
		  AND NOT EXISTS (
			SELECT 1 FROM user_watch_progress uwp
			WHERE uwp.user_id = $1 AND uwp.profile_id = $2
			  AND uwp.media_item_id = e.content_id
			  AND (uwp.completed = TRUE OR uwp.position_seconds > 0))
		ORDER BY e.season_number, e.episode_number, e.content_id
		LIMIT 1`, userID, profileID, seriesID, from, started).Scan(&episodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find next up episode: %w", err)
	}
	return episodeID, nil
}

// loadHomeHides reads the profile's Home removals that apply to the series:
// its drop, and its per-card dismissals of the given items on each surface.
// A surface with no items is not read.
func (u *InterestUpdater) loadHomeHides(ctx context.Context, store userstore.UserStore, userID int, profileID, seriesID string, continueWatchingItems, nextUpItems []string) (homeHides, error) {
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

	if len(continueWatchingItems) > 0 {
		dismissals, err := listHomeDismissals(ctx, store, profileID, userstore.HomeSurfaceContinueWatching, continueWatchingItems)
		if err != nil {
			return homeHides{}, err
		}
		hides.continueWatching = catalog.NewHomeDismissalIndex(dismissals)
	}
	if len(nextUpItems) > 0 {
		dismissals, err := listHomeDismissals(ctx, store, profileID, userstore.HomeSurfaceNextUp, nextUpItems)
		if err != nil {
			return homeHides{}, err
		}
		for _, dismissal := range dismissals {
			if hides.nextUp == nil {
				hides.nextUp = make(map[string]struct{}, len(dismissals))
			}
			hides.nextUp[dismissal.MediaItemID] = struct{}{}
		}
	}
	return hides, nil
}

// listHomeDismissals returns the profile's dismissals of itemIDs on a surface,
// reading only those items when the store supports it.
func listHomeDismissals(ctx context.Context, store userstore.UserStore, profileID, surface string, itemIDs []string) ([]userstore.HomeItemDismissal, error) {
	if reader, ok := store.(userstore.HomeDismissalItemReader); ok {
		dismissals, err := reader.ListHomeDismissalsForItems(ctx, profileID, surface, itemIDs)
		if err != nil {
			return nil, fmt.Errorf("load %s dismissals: %w", surface, err)
		}
		return dismissals, nil
	}
	all, err := store.ListHomeDismissals(ctx, profileID, surface)
	if err != nil {
		return nil, fmt.Errorf("load %s dismissals: %w", surface, err)
	}
	wanted := make(map[string]struct{}, len(itemIDs))
	for _, id := range itemIDs {
		wanted[id] = struct{}{}
	}
	dismissals := make([]userstore.HomeItemDismissal, 0, len(all))
	for _, dismissal := range all {
		if _, ok := wanted[dismissal.MediaItemID]; ok {
			dismissals = append(dismissals, dismissal)
		}
	}
	return dismissals, nil
}

// queueHomeChange queues a recompute for a Home removal or restore now and
// once more after progressSessionGap. Resuming playback lifts a removal
// without changing any progress state; a resume more than progressSessionGap
// after the removal starts a new watch session, which queues its own
// recompute, and the second recompute here catches a quicker one. For a
// restore it repairs a concurrent recompute, such as the interest rebuild,
// that read the state from before the restore and wrote after it.
func (u *InterestUpdater) queueHomeChange(userID int, profileID, itemID string) {
	u.noteHomeChange(userID, profileID, time.Now())
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
	t.updater.queueHomeChange(userID, profileID, seriesID)
	return nil
}

// Undrop removes the drop and queues an interest recompute.
func (t *DroppedSeriesTracker) Undrop(ctx context.Context, userID int, profileID, seriesID string) error {
	if err := t.DroppedSeriesRepo.Undrop(ctx, userID, profileID, seriesID); err != nil {
		return err
	}
	t.updater.queueHomeChange(userID, profileID, seriesID)
	return nil
}

// ImportDrop records a provider's drop and queues an interest recompute when
// the write applied.
func (t *DroppedSeriesTracker) ImportDrop(ctx context.Context, userID int, profileID, seriesID string, droppedAt time.Time, observed *time.Time) (bool, error) {
	applied, err := t.DroppedSeriesRepo.ImportDrop(ctx, userID, profileID, seriesID, droppedAt, observed)
	if err == nil && applied {
		t.updater.queueHomeChange(userID, profileID, seriesID)
	}
	return applied, err
}

// DeleteIfUnchanged removes a drop the provider no longer holds and queues an
// interest recompute when a row was removed.
func (t *DroppedSeriesTracker) DeleteIfUnchanged(ctx context.Context, userID int, profileID, seriesID string, observed time.Time) (bool, error) {
	removed, err := t.DroppedSeriesRepo.DeleteIfUnchanged(ctx, userID, profileID, seriesID, observed)
	if err == nil && removed {
		t.updater.queueHomeChange(userID, profileID, seriesID)
	}
	return removed, err
}
