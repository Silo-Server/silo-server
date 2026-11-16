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

// homeHides is what a profile removed from Home's Next Up for one series. The
// next_up interest reason follows those removals, so a series the profile
// removed from Next Up stops notifying.
type homeHides struct {
	// dropped is an active series drop, which hides the series from Home
	// until the profile watches it again.
	dropped bool
	// nextUp holds the items the profile dismissed from Next Up.
	nextUp map[string]struct{}
}

// nextUpVisible reports whether the series still shows in Next Up, given the
// episode Home would show (nextUpEpisode). Like the Home fetcher
// (catalog.NextUpQuery.ExcludeEpisodeIDs), a per-card dismissal hides the
// card only while its episode is still the one shown; watching on, or
// un-watching an earlier episode, moves Next Up to another episode.
func (h homeHides) nextUpVisible(nextEpisodeID string) bool {
	if h.dropped {
		return false
	}
	_, dismissed := h.nextUp[nextEpisodeID]
	return !dismissed
}

// nextUpAnchor is the completion Home anchors a series' Next Up on
// (catalog's nextUpAnchorsCTE): the most recently completed episode, ties to
// the later one.
type nextUpAnchor struct {
	// key is the anchor episode's key after the special move.
	key int
	// at is the completion that picked the anchor, at full precision.
	at time.Time
	// moved is set when a special anchor moved to a regular episode because
	// the series has regular seasons.
	moved bool
	// regularAt is the completion a moved anchor stands on: the most recent
	// completed regular episode. hasRegularAt is false before there is one.
	regularAt    time.Time
	hasRegularAt bool
}

// progressTime is a progress row's update time at the store's full precision.
func progressTime(entry userstore.WatchProgress) time.Time {
	if !entry.UpdatedAtTime.IsZero() {
		return entry.UpdatedAtTime
	}
	at, _ := time.Parse(time.RFC3339, entry.UpdatedAt)
	return at
}

// partlyWatchedSinceAnchor reports whether an in-progress episode keeps the
// series off Home's Next Up: one whose progress is newer than the anchor
// completion (catalog.buildListNextUpQuery's inProgressExclusion). Once a
// special anchor has moved, a regular episode is measured against the regular
// completion the anchor moved to, and before any regular completion every
// regular episode in progress counts. Without a progress anchor (completed
// history only), any episode in progress counts. inProgress holds the
// series' progress rows that have a position and are not completed.
func partlyWatchedSinceAnchor(anchor *nextUpAnchor, inProgress []userstore.WatchProgress, episodeKeys map[string]int) bool {
	for _, entry := range inProgress {
		key, ok := episodeKeys[entry.MediaItemID]
		if !ok {
			continue
		}
		if anchor == nil {
			return true
		}
		since := anchor.at
		if anchor.moved && key >= firstRegularEpisodeKey {
			if !anchor.hasRegularAt {
				return true
			}
			since = anchor.regularAt
		}
		if progressTime(entry).After(since) {
			return true
		}
	}
	return false
}

// nextUpCursor places the Next Up cursor after anchor. Walking the episodes
// the catalog lists for the series in key order from anchor + 1, the cursor
// moves past each one the profile has started and stops at the first it has
// not, whether or not that episode's file is in the library yet. The first
// listed episode at or after the cursor is therefore the one the profile
// waits for: a listed episode whose file has not arrived holds that place, so
// a later episode landing first does not notify. When every listed episode
// after the anchor is started, the cursor sits after the last of them and the
// first episode to arrive there is the next. That is what lets a rewatcher be
// told about a new season: caught up through S05 and rewatching S01E01, the
// cursor still lands after S05.
//
// episodeKeys maps every listed episode to its key and started lists the
// episodes the profile has started. A key counts as started when any episode
// with that key is.
func nextUpCursor(anchor int, episodeKeys map[string]int, started []string) int {
	startedKeys := make(map[int]struct{}, len(started))
	for _, id := range started {
		if key, ok := episodeKeys[id]; ok && key > anchor {
			startedKeys[key] = struct{}{}
		}
	}
	// waiting is the first listed episode after the anchor the profile has
	// not started; the walk stops there.
	waiting, found := 0, false
	for _, key := range episodeKeys {
		if key <= anchor {
			continue
		}
		if _, ok := startedKeys[key]; ok {
			continue
		}
		if !found || key < waiting {
			waiting, found = key, true
		}
	}
	cursor := anchor + 1
	for key := range startedKeys {
		if !found || key < waiting {
			cursor = max(cursor, key+1)
		}
	}
	return cursor
}

// postgresStartedEpisodes returns the series' episodes the profile has a
// Postgres progress row for that is completed or has a position, including
// rows a history hide covers. That is Home's test for a started episode
// (catalog.buildListNextUpQuery's next_ep lateral), and the profile's store
// leaves hidden rows out of its own listing, so the cursor walk adds these to
// what the store reports. It is empty for a store that keeps progress outside
// Postgres.
func (u *InterestUpdater) postgresStartedEpisodes(ctx context.Context, userID int, profileID, seriesID string) ([]string, error) {
	rows, err := u.pool.Query(ctx, `
		SELECT uwp.media_item_id FROM user_watch_progress uwp
		JOIN episodes e ON e.content_id = uwp.media_item_id
		WHERE uwp.user_id = $1 AND uwp.profile_id = $2 AND e.series_id = $3
		  AND (uwp.completed = TRUE OR uwp.position_seconds > 0)`, userID, profileID, seriesID)
	if err != nil {
		return nil, fmt.Errorf("list started episodes: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scan started episodes: %w", err)
	}
	return ids, nil
}

// nextUpEpisode returns the episode Home's Next Up shows after its anchor:
// the first episode with a key of at least from that has a present file and
// that the profile has not started. "Started" is the Home query's test
// (catalog.buildListNextUpQuery's next_ep lateral: any Postgres progress row
// that is completed or has a position, including rows a history hide covers)
// joined with the started episodes the caller passes: those the profile's own
// store reports, which covers stores that keep progress outside Postgres, and
// those with a completed history entry. It is empty when there is none.
func (u *InterestUpdater) nextUpEpisode(ctx context.Context, userID int, profileID, seriesID string, from int, started []string) (string, error) {
	var episodeID string
	err := u.pool.QueryRow(ctx, `
		SELECT e.content_id FROM episodes e
		WHERE e.series_id = $3
		  AND `+availabilityOrdinalGuard+`
		  AND `+availabilityKeyExpr+` >= $4
		  AND `+presentEpisodeFileSQL+`
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
// its drop, and its Next Up dismissals of the given items. No items reads no
// dismissals.
func (u *InterestUpdater) loadHomeHides(ctx context.Context, store userstore.UserStore, userID int, profileID, seriesID string, nextUpItems []string) (homeHides, error) {
	var hides homeHides
	if u.drops != nil {
		drops, err := u.drops.ListDropped(ctx, userID, profileID, []string{seriesID})
		if err != nil {
			return homeHides{}, fmt.Errorf("load dropped series: %w", err)
		}
		for _, drop := range drops {
			if drop.Active {
				// A drop hides the series; per-card dismissals add nothing.
				return homeHides{dropped: true}, nil
			}
		}
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
