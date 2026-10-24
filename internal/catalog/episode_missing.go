package catalog

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// airedRegularEpisode selects the metadata episodes of numbered seasons that
// have aired. Specials stay library-only: providers list many of them
// (extras, shorts, recaps) that nobody expects to collect.
const airedRegularEpisode = `episodes.season_number > 0 AND episodes.air_date <= now()`

// anyRegularEpisode also takes the regular episodes still to air, for a
// viewer whose download server can say whether they are already coming.
const anyRegularEpisode = `episodes.season_number > 0`

func missingScope(upcoming bool) string {
	if upcoming {
		return anyRegularEpisode
	}
	return airedRegularEpisode
}

// episodeOrMissingPredicate widens episodeAvailabilityPredicate to the
// regular episodes in scope, for a viewer who asked to see what the library
// lacks.
func episodeOrMissingPredicate(upcoming bool) string {
	return `((` + missingScope(upcoming) + `) OR ` + episodeAvailabilityPredicate + `)`
}

// missingEpisodePredicate selects the regular episodes in scope no library
// holds.
func missingEpisodePredicate(upcoming bool) string {
	return missingScope(upcoming) + ` AND NOT ` + episodeAvailabilityPredicate
}

// ListBySeasonWithMissing is ListBySeason including the season's aired
// episodes the library lacks, and with upcoming its episodes still to air,
// each marked with its Availability.
func (r *EpisodeRepository) ListBySeasonWithMissing(ctx context.Context, seriesID string, seasonNum int, upcoming bool) ([]*models.Episode, error) {
	query := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE series_id = $1 AND season_number = $2
		  AND ` + episodeOrMissingPredicate(upcoming) + `
		ORDER BY episode_number ASC`
	return r.listWithMissing(ctx, query, seriesID, seasonNum)
}

// ListBySeasonIDWithMissing is ListBySeasonWithMissing for a season row.
func (r *EpisodeRepository) ListBySeasonIDWithMissing(ctx context.Context, seasonID string, upcoming bool) ([]*models.Episode, error) {
	query := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE season_id = $1
		  AND ` + episodeOrMissingPredicate(upcoming) + `
		ORDER BY episode_number ASC`
	return r.listWithMissing(ctx, query, seasonID)
}

func (r *EpisodeRepository) listWithMissing(ctx context.Context, query string, args ...any) ([]*models.Episode, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing episodes with missing: %w", err)
	}
	defer rows.Close()
	episodes, err := scanEpisodes(rows)
	if err != nil {
		return nil, err
	}
	if err := r.markAvailability(ctx, episodes, time.Now()); err != nil {
		return nil, err
	}
	return episodes, nil
}

// episodeMembershipBatchSize bounds the episode ids one library membership
// read sends.
const episodeMembershipBatchSize = 500

// markAvailability sets Availability on the episodes no library holds:
// unaired while the air date is unknown or still ahead of now, missing once
// it has passed.
func (r *EpisodeRepository) markAvailability(ctx context.Context, episodes []*models.Episode, now time.Time) error {
	if len(episodes) == 0 {
		return nil
	}
	ids := make([]string, 0, len(episodes))
	for _, ep := range episodes {
		ids = append(ids, ep.ContentID)
	}
	inLibrary := make(map[string]bool, len(ids))
	for batch := range slices.Chunk(ids, episodeMembershipBatchSize) {
		if err := r.readLibraryMembership(ctx, batch, inLibrary); err != nil {
			return err
		}
	}
	for _, ep := range episodes {
		ep.Availability = episodeAvailability(inLibrary[ep.ContentID], ep.AirDate, now)
	}
	return nil
}

// episodeAvailability classifies one episode for a list that includes what
// the library lacks.
func episodeAvailability(inLibrary bool, airDate *time.Time, now time.Time) string {
	switch {
	case inLibrary:
		return ""
	case airDate == nil || airDate.After(now):
		return models.EpisodeUnaired
	default:
		return models.EpisodeMissing
	}
}

// CountMissingBySeason counts, per numbered season, the series' aired
// episodes no library holds, and with upcoming its episodes still to air.
func (r *EpisodeRepository) CountMissingBySeason(ctx context.Context, seriesID string, upcoming bool) (map[int]int, error) {
	rows, err := r.pool.Query(ctx, `SELECT season_number, COUNT(*)
		FROM episodes
		WHERE series_id = $1
		  AND `+missingEpisodePredicate(upcoming)+`
		GROUP BY season_number`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("counting missing episodes: %w", err)
	}
	defer rows.Close()
	counts := make(map[int]int)
	for rows.Next() {
		var season, count int
		if err := rows.Scan(&season, &count); err != nil {
			return nil, fmt.Errorf("scanning missing episode count: %w", err)
		}
		counts[season] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating missing episode counts: %w", err)
	}
	return counts, nil
}

// InLibrary returns the episodes a library holds, dropping the ones a
// with-missing list added.
func InLibrary(episodes []*models.Episode) []*models.Episode {
	out := make([]*models.Episode, 0, len(episodes))
	for _, ep := range episodes {
		if ep != nil && ep.Availability == "" {
			out = append(out, ep)
		}
	}
	return out
}

// readLibraryMembership marks in inLibrary the ids of batch some library
// holds.
func (r *EpisodeRepository) readLibraryMembership(ctx context.Context, batch []string, inLibrary map[string]bool) error {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT episode_id FROM episode_libraries WHERE episode_id = ANY($1)`, batch)
	if err != nil {
		return fmt.Errorf("reading episode library membership: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scanning episode library membership: %w", err)
		}
		inLibrary[id] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating episode library membership: %w", err)
	}
	return nil
}
