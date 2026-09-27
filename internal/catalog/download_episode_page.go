package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// EpisodePagePosition identifies the last returned episode in natural order.
// This is an internal keyset position; native clients receive scoped opaque
// cursors from their owning transport instead of constructing these values.
type EpisodePagePosition struct {
	SeasonNumber  int
	EpisodeNumber int
	ContentID     string
}

// ListDownloadEpisodesPage bounds available episode selection before download
// metadata resolution. A nil season selects all seasons, including specials (0).
// A nil position starts the first page. The boolean reports another row beyond
// this page; callers advance using the last returned episode's tuple.
// Like the bridge resolvers, availability means episode library membership.
// This is a live catalog traversal, not a snapshot across concurrent rescans.
func (r *EpisodeRepository) ListDownloadEpisodesPage(ctx context.Context, seriesID string, seasonNumber *int, after *EpisodePagePosition, limit int) ([]*models.Episode, bool, error) {
	if strings.TrimSpace(seriesID) == "" {
		return nil, false, fmt.Errorf("series ID is required")
	}
	if limit < 1 || limit > 200 {
		return nil, false, fmt.Errorf("episode page limit must be between 1 and 200")
	}
	if seasonNumber != nil && *seasonNumber < 0 {
		return nil, false, fmt.Errorf("season number must not be negative")
	}
	if after != nil && (after.SeasonNumber < 0 || after.EpisodeNumber < 0 || after.ContentID == "" || (seasonNumber != nil && after.SeasonNumber != *seasonNumber)) {
		return nil, false, fmt.Errorf("invalid episode page position")
	}
	args := []any{seriesID}
	query := `SELECT ` + episodeColumns + ` FROM episodes WHERE series_id=$1 AND ` + episodeAvailabilityPredicate
	if seasonNumber != nil {
		args = append(args, *seasonNumber)
		query += fmt.Sprintf(" AND season_number=$%d", len(args))
	}
	if after != nil {
		n := len(args)
		args = append(args, after.SeasonNumber, after.EpisodeNumber, after.ContentID)
		query += fmt.Sprintf(" AND (season_number,episode_number,content_id)>($%d,$%d,$%d)", n+1, n+2, n+3)
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(" ORDER BY season_number ASC,episode_number ASC,content_id ASC LIMIT $%d", len(args))
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("listing download episode page: %w", err)
	}
	defer rows.Close()
	episodes, err := scanEpisodes(rows)
	if err != nil {
		return nil, false, err
	}
	more := len(episodes) > limit
	if more {
		episodes = episodes[:limit]
	}
	if episodes == nil {
		episodes = []*models.Episode{}
	}
	return episodes, more, nil
}
