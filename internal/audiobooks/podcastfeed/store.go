package podcastfeed

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DBStore implements Store backed by silo's Postgres pool.
type DBStore struct {
	pool *pgxpool.Pool
}

// NewDBStore constructs a production Store.
func NewDBStore(pool *pgxpool.Pool) *DBStore {
	return &DBStore{pool: pool}
}

// ListPodcastFeeds returns all rows from podcast_feeds.
func (s *DBStore) ListPodcastFeeds(ctx context.Context) ([]PodcastFeed, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT media_item_id,
		       feed_url,
		       refresh_interval_seconds,
		       last_refreshed_at
		FROM podcast_feeds
		ORDER BY last_refreshed_at ASC NULLS FIRST
	`)
	if err != nil {
		return nil, fmt.Errorf("list podcast feeds: %w", err)
	}
	defer rows.Close()
	var out []PodcastFeed
	for rows.Next() {
		var f PodcastFeed
		if err := rows.Scan(
			&f.MediaItemID,
			&f.FeedURL,
			&f.RefreshIntervalSeconds,
			&f.LastRefreshedAt,
		); err != nil {
			return nil, fmt.Errorf("scan podcast feed: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetEpisodeIDsByGUID returns a map of podcast_guid → content_id for
// episodes in the given series that match any of the supplied GUIDs.
func (s *DBStore) GetEpisodeIDsByGUID(ctx context.Context, seriesID string, guids []string) (map[string]string, error) {
	out := make(map[string]string, len(guids))
	if seriesID == "" || len(guids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT content_id, podcast_guid
		FROM episodes
		WHERE series_id = $1
		  AND podcast_guid = ANY($2::text[])
	`, seriesID, guids)
	if err != nil {
		return nil, fmt.Errorf("guid lookup: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, guid string
		if err := rows.Scan(&id, &guid); err != nil {
			return nil, fmt.Errorf("scan episode guid: %w", err)
		}
		out[guid] = id
	}
	return out, rows.Err()
}

// UpsertPodcastEpisode inserts or updates an episode keyed by
// (series_id, podcast_guid). The season / episode number columns use 0
// for podcast episodes that lack a structured number in the feed.
//
// runtime is stored in seconds (the column name comes from TV; for
// podcasts we store raw seconds to preserve fidelity — ABS and the
// native API read it back as seconds).
func (s *DBStore) UpsertPodcastEpisode(ctx context.Context, e PodcastEpisode) error {
	if e.ContentID == "" || e.SeriesID == "" || e.GUID == "" || e.Title == "" {
		return fmt.Errorf("content_id, series_id, guid, title required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO episodes (
			content_id, series_id,
			season_number, episode_number,
			title, overview, air_date, runtime,
			still_path,
			podcast_guid, podcast_audio_url
		) VALUES (
			$1, $2,
			$3, $4,
			$5, NULLIF($6,''), $7, $8,
			NULLIF($9,''),
			$10, NULLIF($11,'')
		)
		ON CONFLICT (series_id, podcast_guid) WHERE podcast_guid IS NOT NULL
		DO UPDATE SET
			title             = EXCLUDED.title,
			overview          = EXCLUDED.overview,
			air_date          = EXCLUDED.air_date,
			runtime           = EXCLUDED.runtime,
			still_path        = EXCLUDED.still_path,
			podcast_audio_url = EXCLUDED.podcast_audio_url,
			updated_at        = NOW()
	`,
		e.ContentID,
		e.SeriesID,
		e.SeasonNumber,
		e.EpisodeNumber,
		e.Title,
		e.Overview,
		e.PublishedAt,
		e.DurationSeconds,
		e.StillPath,
		e.GUID,
		e.AudioURL,
	)
	if err != nil {
		return fmt.Errorf("upsert podcast episode: %w", err)
	}
	return nil
}

// MarkFeedRefreshed sets last_refreshed_at = now() and last_refresh_error
// on the podcast_feeds row. An empty lastError clears the error field.
func (s *DBStore) MarkFeedRefreshed(ctx context.Context, mediaItemID string, lastError string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE podcast_feeds
		SET last_refreshed_at  = now(),
		    last_refresh_error = NULLIF($2, ''),
		    updated_at         = now()
		WHERE media_item_id = $1
	`, mediaItemID, lastError)
	if err != nil {
		return fmt.Errorf("mark feed refreshed: %w", err)
	}
	return nil
}
