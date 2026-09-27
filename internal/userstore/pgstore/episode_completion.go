package pgstore

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

var _ userstore.EpisodeParentCompletionStore = (*PostgresUserStore)(nil)

func (s *PostgresUserStore) SeriesCompletion(ctx context.Context, profileID string, seriesIDs []string) (map[string]bool, error) {
	return s.episodeParentCompletion(ctx, profileID, "series_id", seriesIDs)
}

func (s *PostgresUserStore) SeasonCompletion(ctx context.Context, profileID string, seasonIDs []string) (map[string]bool, error) {
	return s.episodeParentCompletion(ctx, profileID, "season_id", seasonIDs)
}

func (s *PostgresUserStore) episodeParentCompletion(ctx context.Context, profileID, parentColumn string, parentIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(parentIDs))
	if len(parentIDs) == 0 {
		return result, nil
	}
	// parentColumn is supplied only by the two fixed-column wrappers above.
	// Separate EXISTS checks distinguish empty parents from completed parents
	// while allowing the incomplete-episode search to stop after one match.
	query := fmt.Sprintf(`SELECT parent_id,
		EXISTS (
			SELECT 1 FROM episodes e WHERE e.%[1]s = parent_id
			AND EXISTS (SELECT 1 FROM episode_libraries el WHERE el.episode_id = e.content_id)
		) AND NOT EXISTS (
			SELECT 1 FROM episodes e WHERE e.%[1]s = parent_id
			AND EXISTS (SELECT 1 FROM episode_libraries el WHERE el.episode_id = e.content_id)
			AND NOT EXISTS (
				SELECT 1 FROM user_watch_progress wp
				WHERE wp.user_id = $1 AND wp.profile_id = $2
				  AND wp.media_item_id = e.content_id AND wp.completed
				  AND NOT EXISTS (
					SELECT 1 FROM user_history_hidden_items hh
					WHERE hh.user_id = wp.user_id AND hh.profile_id = wp.profile_id
					  AND hh.media_item_id = wp.media_item_id AND wp.updated_at <= hh.hidden_before
				  )
			) AND NOT EXISTS (
				SELECT 1 FROM user_watch_history h
				WHERE h.user_id = $1 AND h.profile_id = $2
				  AND h.media_item_id = e.content_id AND h.completed
				`+completedHistoryVisibleSQL+`
			)
		) AS completed
		FROM unnest($3::text[]) AS parents(parent_id)`, parentColumn)
	rows, err := s.pool.Query(ctx, query, s.userID, profileID, parentIDs)
	if err != nil {
		return nil, fmt.Errorf("loading episode parent completion: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var parentID string
		var completed bool
		if err := rows.Scan(&parentID, &completed); err != nil {
			return nil, fmt.Errorf("scanning episode parent completion: %w", err)
		}
		result[parentID] = completed
	}
	return result, rows.Err()
}
