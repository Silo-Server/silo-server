package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Sentinel errors for season repository operations.
var (
	ErrSeasonNotFound = errors.New("season not found")
)

// SeasonRepository provides CRUD operations for the seasons table.
type SeasonRepository struct {
	pool *pgxpool.Pool
}

// NewSeasonRepository creates a new SeasonRepository backed by the given pool.
func NewSeasonRepository(pool *pgxpool.Pool) *SeasonRepository {
	return &SeasonRepository{pool: pool}
}

// seasonColumns is the list of columns returned by all SELECT queries on seasons.
const seasonColumns = `content_id, series_id, season_number, title, default_metadata_language, overview,
	air_date, poster_path, poster_source_path, poster_thumbhash,
	metadata_s3_path, metadata_etag, metadata_source,
	created_at, updated_at`

// scanSeason scans a single row into a *models.Season.
func scanSeason(row pgx.Row) (*models.Season, error) {
	var s models.Season
	err := row.Scan(
		&s.ContentID,
		&s.SeriesID,
		&s.SeasonNumber,
		&s.Title,
		&s.DefaultMetadataLanguage,
		&s.Overview,
		&s.AirDate,
		&s.PosterPath,
		&s.PosterSourcePath,
		&s.PosterThumbhash,
		&s.MetadataS3Path,
		&s.MetadataEtag,
		&s.MetadataSource,
		&s.CreatedAt,
		&s.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSeasonNotFound
		}
		return nil, fmt.Errorf("scanning season: %w", err)
	}
	return &s, nil
}

// scanSeasons scans multiple rows into a []*models.Season slice.
func scanSeasons(rows pgx.Rows) ([]*models.Season, error) {
	var seasons []*models.Season
	for rows.Next() {
		var s models.Season
		err := rows.Scan(
			&s.ContentID,
			&s.SeriesID,
			&s.SeasonNumber,
			&s.Title,
			&s.DefaultMetadataLanguage,
			&s.Overview,
			&s.AirDate,
			&s.PosterPath,
			&s.PosterSourcePath,
			&s.PosterThumbhash,
			&s.MetadataS3Path,
			&s.MetadataEtag,
			&s.MetadataSource,
			&s.CreatedAt,
			&s.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning season row: %w", err)
		}
		seasons = append(seasons, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating season rows: %w", err)
	}
	return seasons, nil
}

// Upsert inserts a new season or updates all mutable fields if the
// (series_id, season_number) natural key already exists. The stored content ID
// is preserved on update and written back to s.ContentID.
func (r *SeasonRepository) Upsert(ctx context.Context, s *models.Season) error {
	query := `
		INSERT INTO seasons (
			content_id, series_id, season_number, title, default_metadata_language, overview,
			air_date, poster_path, poster_source_path, poster_thumbhash,
			metadata_s3_path, metadata_etag, metadata_source
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13
		)
		ON CONFLICT (series_id, season_number) DO UPDATE SET
			title = EXCLUDED.title,
			default_metadata_language = COALESCE(NULLIF(seasons.default_metadata_language, ''), EXCLUDED.default_metadata_language),
			overview = EXCLUDED.overview,
			air_date = EXCLUDED.air_date,
			poster_path = EXCLUDED.poster_path,
			poster_source_path = EXCLUDED.poster_source_path,
			poster_thumbhash = EXCLUDED.poster_thumbhash,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			metadata_source = EXCLUDED.metadata_source,
			updated_at = NOW()
		RETURNING content_id`

	var storedContentID string
	err := r.pool.QueryRow(ctx, query,
		s.ContentID,
		s.SeriesID,
		s.SeasonNumber,
		s.Title,
		s.DefaultMetadataLanguage,
		s.Overview,
		s.AirDate,
		s.PosterPath,
		s.PosterSourcePath,
		s.PosterThumbhash,
		s.MetadataS3Path,
		s.MetadataEtag,
		s.MetadataSource,
	).Scan(&storedContentID)
	if err != nil {
		return fmt.Errorf("upserting season: %w", err)
	}
	s.ContentID = storedContentID
	return nil
}

// BulkUpsert inserts or updates multiple seasons in a single round-trip using
// UNNEST-based multi-row INSERT ON CONFLICT. Stored content IDs are written
// back to each Season struct.
func (r *SeasonRepository) BulkUpsert(ctx context.Context, seasons []*models.Season) error {
	if len(seasons) == 0 {
		return nil
	}

	contentIDs := make([]string, len(seasons))
	seriesIDs := make([]string, len(seasons))
	seasonNums := make([]int32, len(seasons))
	titles := make([]string, len(seasons))
	defaultMetadataLanguages := make([]string, len(seasons))
	overviews := make([]string, len(seasons))
	airDates := make([]*time.Time, len(seasons))
	posterPaths := make([]string, len(seasons))
	posterSourcePaths := make([]string, len(seasons))
	posterThumbs := make([]string, len(seasons))
	metaS3Paths := make([]string, len(seasons))
	metaEtags := make([]string, len(seasons))
	metaSources := make([]string, len(seasons))

	for i, s := range seasons {
		contentIDs[i] = s.ContentID
		seriesIDs[i] = s.SeriesID
		seasonNums[i] = int32(s.SeasonNumber)
		titles[i] = s.Title
		defaultMetadataLanguages[i] = s.DefaultMetadataLanguage
		overviews[i] = s.Overview
		airDates[i] = s.AirDate
		posterPaths[i] = s.PosterPath
		posterSourcePaths[i] = s.PosterSourcePath
		posterThumbs[i] = s.PosterThumbhash
		metaS3Paths[i] = s.MetadataS3Path
		metaEtags[i] = s.MetadataEtag
		metaSources[i] = s.MetadataSource
	}

	query := `
		INSERT INTO seasons (
			content_id, series_id, season_number, title, default_metadata_language, overview,
			air_date, poster_path, poster_source_path, poster_thumbhash,
			metadata_s3_path, metadata_etag, metadata_source
		)
		SELECT * FROM UNNEST(
			$1::text[], $2::text[], $3::int[], $4::text[], $5::text[], $6::text[],
			$7::date[], $8::text[], $9::text[], $10::text[],
			$11::text[], $12::text[], $13::text[]
		)
		ON CONFLICT (series_id, season_number) DO UPDATE SET
			title = EXCLUDED.title,
			default_metadata_language = COALESCE(NULLIF(seasons.default_metadata_language, ''), EXCLUDED.default_metadata_language),
			overview = EXCLUDED.overview,
			air_date = EXCLUDED.air_date,
			poster_path = EXCLUDED.poster_path,
			poster_source_path = EXCLUDED.poster_source_path,
			poster_thumbhash = EXCLUDED.poster_thumbhash,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			metadata_source = EXCLUDED.metadata_source,
			updated_at = NOW()
		RETURNING content_id, season_number`

	rows, err := r.pool.Query(ctx, query,
		contentIDs, seriesIDs, seasonNums, titles, defaultMetadataLanguages, overviews,
		airDates, posterPaths, posterSourcePaths, posterThumbs,
		metaS3Paths, metaEtags, metaSources,
	)
	if err != nil {
		return fmt.Errorf("bulk upserting seasons: %w", err)
	}
	defer rows.Close()

	// Build a map to write back content IDs by season number.
	returnedIDs := make(map[int32]string, len(seasons))
	for rows.Next() {
		var cid string
		var sn int32
		if err := rows.Scan(&cid, &sn); err != nil {
			return fmt.Errorf("scanning bulk upsert season result: %w", err)
		}
		returnedIDs[sn] = cid
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating bulk upsert season results: %w", err)
	}

	for _, s := range seasons {
		if cid, ok := returnedIDs[int32(s.SeasonNumber)]; ok {
			s.ContentID = cid
		}
	}

	return nil
}

// GetBySeriesAndNumber retrieves a specific season by series ID and season number.
func (r *SeasonRepository) GetBySeriesAndNumber(ctx context.Context, seriesID string, seasonNum int) (*models.Season, error) {
	query := `SELECT ` + seasonColumns + `
		FROM seasons
		WHERE series_id = $1 AND season_number = $2`
	return scanSeason(r.pool.QueryRow(ctx, query, seriesID, seasonNum))
}

// GetByID retrieves a specific season by its content ID.
func (r *SeasonRepository) GetByID(ctx context.Context, contentID string) (*models.Season, error) {
	query := `SELECT ` + seasonColumns + `
		FROM seasons
		WHERE content_id = $1`
	return scanSeason(r.pool.QueryRow(ctx, query, contentID))
}

// ListBySeries returns all seasons for a given series, ordered by season number.
func (r *SeasonRepository) ListBySeries(ctx context.Context, seriesID string) ([]*models.Season, error) {
	query := `SELECT ` + seasonColumns + `
		FROM seasons
		WHERE series_id = $1
		ORDER BY season_number ASC`

	rows, err := r.pool.Query(ctx, query, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing seasons by series: %w", err)
	}
	defer rows.Close()

	return scanSeasons(rows)
}

// CountBySeries returns the number of seasons for a given series.
func (r *SeasonRepository) CountBySeries(ctx context.Context, seriesID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM seasons WHERE series_id = $1", seriesID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting seasons by series: %w", err)
	}
	return count, nil
}

// DeleteBySeries deletes all seasons for a given series.
func (r *SeasonRepository) DeleteBySeries(ctx context.Context, seriesID string) error {
	_, err := r.pool.Exec(ctx,
		"DELETE FROM seasons WHERE series_id = $1", seriesID)
	if err != nil {
		return fmt.Errorf("deleting seasons by series: %w", err)
	}
	return nil
}

// UpdateMetadata builds a dynamic UPDATE query for the seasons table,
// setting only the non-nil fields in upd. Always bumps updated_at.
// Returns ErrSeasonNotFound if no row matches contentID.
func (r *SeasonRepository) UpdateMetadata(ctx context.Context, contentID string, upd *MetadataUpdate) error {
	var setClauses []string
	var args []any
	argIdx := 1

	addString := func(col string, val *string) {
		if val != nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, *val)
			argIdx++
		}
	}
	addInt := func(col string, val *int) {
		if val != nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, *val)
			argIdx++
		}
	}

	addString("title", upd.Title)
	addString("overview", upd.Overview)
	addInt("season_number", upd.SeasonNumber)
	addString("air_date", upd.AirDate)
	addString("poster_path", upd.PosterPath)
	if upd.PosterPath != nil && upd.PosterSourcePath == nil {
		setClauses = append(setClauses, "poster_source_path = ''")
	}
	addString("poster_source_path", upd.PosterSourcePath)
	addString("poster_thumbhash", upd.PosterThumbhash)

	setClauses = append(setClauses, "updated_at = NOW()")

	query := fmt.Sprintf("UPDATE seasons SET %s WHERE content_id = $%d",
		strings.Join(setClauses, ", "), argIdx)
	args = append(args, contentID)

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating season metadata: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSeasonNotFound
	}
	return nil
}

func (r *SeasonRepository) UpdateArtworkIfSourceMatches(ctx context.Context, contentID, sourcePath, cachedPath, thumbhash string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE seasons
		SET poster_path = $3,
			poster_source_path = $2,
			poster_thumbhash = $4,
			updated_at = NOW()
		WHERE content_id = $1
		  AND poster_source_path = $2
	`, contentID, sourcePath, cachedPath, thumbhash)
	if err != nil {
		return false, fmt.Errorf("updating season cached artwork: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
