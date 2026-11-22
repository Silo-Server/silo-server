package catalog

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EpisodeLabel is how a card labels a date-labeled series or one of its
// episodes (see models.EpisodeLabelsAirDate).
type EpisodeLabel struct {
	Labels  string
	AirDate string // the episode's air date, YYYY-MM-DD; empty for a series or an undated episode
}

// episodeLabelsQuery finds, among the given content IDs, the series that label
// their episodes by something other than numbers, and the episodes of such
// series with their air dates. Numbered series have no row.
const episodeLabelsQuery = `
	SELECT e.content_id, s.episode_labels, COALESCE(e.air_date::text, '')
	FROM episodes e
	JOIN media_items s ON s.content_id = e.series_id
	WHERE e.content_id = ANY($1) AND s.episode_labels <> ''
	UNION ALL
	SELECT s.content_id, s.episode_labels, ''
	FROM media_items s
	WHERE s.content_id = ANY($1) AND s.type = 'series' AND s.episode_labels <> ''
`

// EpisodeLabelReader loads the episode labels cards show.
type EpisodeLabelReader struct {
	pool *pgxpool.Pool
}

// NewEpisodeLabelReader returns a reader over the catalog database.
func NewEpisodeLabelReader(pool *pgxpool.Pool) *EpisodeLabelReader {
	return &EpisodeLabelReader{pool: pool}
}

// EpisodeLabels returns the episode labels of date-labeled series and of their
// episodes, keyed by content ID. IDs with no entry show numbers.
func (r *EpisodeLabelReader) EpisodeLabels(ctx context.Context, ids []string) (map[string]EpisodeLabel, error) {
	labels := make(map[string]EpisodeLabel)
	if r == nil || r.pool == nil || len(ids) == 0 {
		return labels, nil
	}
	rows, err := r.pool.Query(ctx, episodeLabelsQuery, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var label EpisodeLabel
		if err := rows.Scan(&id, &label.Labels, &label.AirDate); err != nil {
			return nil, err
		}
		labels[id] = label
	}
	return labels, rows.Err()
}
