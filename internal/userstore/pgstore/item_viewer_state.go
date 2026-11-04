package pgstore

import (
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

var _ userstore.ItemViewerStateQueuer = (*PostgresUserStore)(nil)

// completedHistoryLatestSQL is ListCompletedHistoryItems for one item ($3)
// and no source filter: the item's latest visible completed watch, NULL when
// it has none. A batch cannot plan per execution as profilePlanArgs does, so
// the item is compared by equality: the generic plan a cached statement
// switches to keeps probing the item index, where the array form's
// cardinality test would read the profile's whole history.
const completedHistoryLatestSQL = `
		SELECT MAX(h.watched_at)
		FROM user_watch_history h
		WHERE h.user_id = $1
		  AND h.profile_id = $2
		  AND h.completed = true
		  AND h.media_item_id = $3
		` + completedHistoryVisibleSQL

// QueueItemViewerState queues the reads for the parts of one item's viewer
// state q asks for. Each is the statement its single-item method runs, except
// the completed-history read (see completedHistoryLatestSQL); closing the
// sent batch's results without error fills state.
func (s *PostgresUserStore) QueueItemViewerState(b *pgx.Batch, q userstore.ItemViewerStateQuery, state *userstore.ItemViewerState) bool {
	*state = userstore.ItemViewerState{}
	// GetProgressWithCompletedHistory reads nothing for a blank profile or
	// item, and its ID is trimmed; the other reads take the ID as given.
	progressID := strings.TrimSpace(q.MediaItemID)
	if q.Progress && q.ProfileID != "" && progressID != "" {
		b.Queue(progressSQL, s.userID, q.ProfileID, progressID).QueryRow(func(row pgx.Row) error {
			progress, err := scanProgressRow(row)
			state.Progress = progress
			return err
		})
		b.Queue(completedHistoryLatestSQL, s.userID, q.ProfileID, progressID).QueryRow(func(row pgx.Row) error {
			var watchedAt *time.Time
			if err := row.Scan(&watchedAt); err != nil {
				return err
			}
			var completed userstore.CompletedHistoryItem
			if watchedAt != nil {
				completed = userstore.CompletedHistoryItem{MediaItemID: progressID, WatchedAt: timeToString(*watchedAt)}
			}
			state.Progress = userstore.WithCompletedHistory(state.Progress, completed, q.ProfileID, progressID)
			return nil
		})
	}
	if q.VersionChoice {
		b.Queue(seriesPlaybackPreferenceSQL, s.userID, q.ProfileID, q.MediaItemID).QueryRow(func(row pgx.Row) error {
			choice, err := scanSeriesPlaybackPreference(row)
			state.VersionChoice = choice
			return err
		})
	}
	if q.Marks {
		queueCount := func(sql string, dst *bool) {
			b.Queue(sql, s.userID, q.ProfileID, q.MediaItemID).QueryRow(func(row pgx.Row) error {
				var count int
				err := row.Scan(&count)
				*dst = count > 0
				return err
			})
		}
		queueCount(isFavoriteSQL, &state.IsFavorite)
		queueCount(inWatchlistSQL, &state.InWatchlist)
		b.Queue(notInterestedSQL, s.userID, q.ProfileID, q.MediaItemID).QueryRow(func(row pgx.Row) error {
			mark, err := scanNotInterested(row)
			state.NotInterested = mark != nil
			return err
		})
	}
	if q.PlaybackPreferences {
		b.Queue(audioPreferenceSQL, s.userID, q.ProfileID, q.MediaItemID).QueryRow(func(row pgx.Row) error {
			pref, err := scanOptionalAudioPreference(row)
			state.AudioPreference = pref
			return err
		})
		b.Queue(subtitlePreferenceSQL, s.userID, q.ProfileID, q.MediaItemID).QueryRow(func(row pgx.Row) error {
			pref, err := scanSubtitlePreference(row)
			state.SubtitlePreference = pref
			return err
		})
	}
	return true
}
