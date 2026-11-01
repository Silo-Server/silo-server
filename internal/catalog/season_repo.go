package catalog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ParseSyntheticSeasonID recognizes the season IDs derived from episode groups
// when a series has no stored season metadata. Callers must verify existence.
func ParseSyntheticSeasonID(contentID string) (string, int, bool) {
	seriesID, seasonPart, ok := strings.Cut(contentID, "-S")
	if !ok || seriesID == "" || seasonPart == "" {
		return "", 0, false
	}
	seasonNum, err := strconv.Atoi(seasonPart)
	if err != nil || !FitsPostgresInteger(seasonNum) {
		return "", 0, false
	}
	return seriesID, seasonNum, true
}

// Sentinel errors for season repository operations.
var (
	ErrSeasonNotFound = errors.New("season not found")
)

// FitsPostgresInteger reports whether value fits a PostgreSQL integer (int4) column.
func FitsPostgresInteger(value int) bool {
	return value >= -1<<31 && value <= 1<<31-1
}

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
	created_at, updated_at,
	backdrop_path, backdrop_source_path, backdrop_thumbhash`

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
		&s.BackdropPath,
		&s.BackdropSourcePath,
		&s.BackdropThumbhash,
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
			&s.BackdropPath,
			&s.BackdropSourcePath,
			&s.BackdropThumbhash,
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

// seasonArtworkAssignments keeps a season poster an admin picked after the
// writer read the season; see lockedArtworkAssignments.
var seasonArtworkAssignments = lockedArtworkAssignments("seasons", seriesImagesLockedSQL("seasons"),
	"poster_path", "poster_source_path", "poster_thumbhash")

// writeSeasonBackdrops sets the backdrop of each season marked WriteBackdrop,
// in the upsert's transaction after the row is written and locked. It writes
// the slot from the current row rather than the writer's earlier read, so
// unmarked writers keep whatever is stored, a marked one replaces or clears
// it, and one marked BackdropFillOnly only fills a backdrop that is still
// empty. While the series has Images locked, a stored backdrop stays, as
// lockedArtworkAssignments keeps a poster.
func writeSeasonBackdrops(ctx context.Context, tx pgx.Tx, seasons []*models.Season) error {
	var seriesIDs, paths, sources, thumbhashes []string
	var numbers []int32
	var fillOnly []bool
	for _, s := range seasons {
		if !s.WriteBackdrop {
			continue
		}
		if s.SeasonNumber < math.MinInt32 || s.SeasonNumber > math.MaxInt32 {
			return fmt.Errorf("writing season backdrops: season number %d is outside PostgreSQL integer range", s.SeasonNumber)
		}
		seriesIDs = append(seriesIDs, s.SeriesID)
		numbers = append(numbers, int32(s.SeasonNumber))
		paths = append(paths, s.BackdropPath)
		sources = append(sources, s.BackdropSourcePath)
		thumbhashes = append(thumbhashes, s.BackdropThumbhash)
		fillOnly = append(fillOnly, s.BackdropFillOnly)
	}
	if len(seriesIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE seasons
		SET backdrop_path = target.path,
			backdrop_source_path = target.source_path,
			backdrop_thumbhash = target.thumbhash,
			updated_at = NOW()
		FROM UNNEST($1::text[], $2::int[], $3::text[], $4::text[], $5::text[], $6::bool[])
			AS target(series_id, season_number, path, source_path, thumbhash, fill_only)
		WHERE seasons.series_id = target.series_id
		  AND seasons.season_number = target.season_number
		  AND (seasons.backdrop_path, seasons.backdrop_source_path, seasons.backdrop_thumbhash)
			IS DISTINCT FROM (target.path, target.source_path, target.thumbhash)
		  AND NOT (target.fill_only AND seasons.backdrop_path <> '')
		  AND NOT (`+seriesImagesLockedSQL("seasons")+` AND seasons.backdrop_path <> '')`,
		seriesIDs, numbers, paths, sources, thumbhashes, fillOnly)
	if err != nil {
		return fmt.Errorf("writing season backdrops: %w", err)
	}
	return nil
}

// ClearBackdropsExcept clears the backdrop of every season of seriesID whose
// number is not in keep, unless the series has Images locked. Identify uses it
// for seasons the corrected match no longer names, which may show the
// previous match's art. It locks the rows first and updates only those, so a
// pick published meanwhile is either seen with its images lock or waits.
func (r *SeasonRepository) ClearBackdropsExcept(ctx context.Context, seriesID string, keep []int32) error {
	if keep == nil {
		keep = []int32{}
	}
	err := pgx.BeginTxFunc(ctx, r.pool, artworkUpsertTxOptions, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT content_id FROM seasons
			WHERE series_id = $1 AND NOT (season_number = ANY($2::int[])) AND backdrop_path <> ''
			ORDER BY content_id
			FOR NO KEY UPDATE`, seriesID, keep)
		if err != nil {
			return err
		}
		contentIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(contentIDs) == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE seasons
			SET backdrop_path = '', backdrop_source_path = '', backdrop_thumbhash = '', updated_at = NOW()
			WHERE content_id = ANY($1::text[])
			  AND NOT `+seriesImagesLockedSQL("seasons"), contentIDs)
		return err
	})
	if err != nil {
		return fmt.Errorf("clearing season backdrops: %w", err)
	}
	return nil
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
			default_metadata_language = COALESCE(NULLIF(EXCLUDED.default_metadata_language, ''), seasons.default_metadata_language),
			overview = EXCLUDED.overview,
			air_date = EXCLUDED.air_date,
			` + seasonArtworkAssignments + `,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			metadata_source = EXCLUDED.metadata_source,
			updated_at = NOW()
		RETURNING content_id, xmax = 0`

	var storedContentID string
	err := retryArtworkUpsert(func() error {
		return pgx.BeginTxFunc(ctx, r.pool, artworkUpsertTxOptions, func(tx pgx.Tx) error {
			locked, err := lockArtworkRowsForUpsert(ctx, tx, `
			SELECT content_id FROM seasons WHERE series_id = $1 AND season_number = $2 FOR NO KEY UPDATE`,
				s.SeriesID, s.SeasonNumber)
			if err != nil {
				return err
			}
			var inserted bool
			if err := tx.QueryRow(ctx, query,
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
			).Scan(&storedContentID, &inserted); err != nil {
				return err
			}
			if err := checkArtworkRowLocked(locked, storedContentID, inserted); err != nil {
				return err
			}
			return writeSeasonBackdrops(ctx, tx, []*models.Season{s})
		})
	})
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
	for i, season := range seasons {
		if season == nil {
			return fmt.Errorf("bulk upserting seasons: season %d is nil", i)
		}
		if !FitsPostgresInteger(season.SeasonNumber) {
			return fmt.Errorf("bulk upserting seasons: season %d number %d is outside PostgreSQL integer range", i, season.SeasonNumber)
		}
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
			default_metadata_language = COALESCE(NULLIF(EXCLUDED.default_metadata_language, ''), seasons.default_metadata_language),
			overview = EXCLUDED.overview,
			air_date = EXCLUDED.air_date,
			` + seasonArtworkAssignments + `,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			metadata_source = EXCLUDED.metadata_source,
			updated_at = NOW()
		RETURNING content_id, season_number, xmax = 0`

	// Build a map to write back content IDs by season number.
	returnedIDs := make(map[int32]string, len(seasons))
	err := retryArtworkUpsert(func() error {
		return pgx.BeginTxFunc(ctx, r.pool, artworkUpsertTxOptions, func(tx pgx.Tx) error {
			locked, err := lockArtworkRowsForUpsert(ctx, tx, `
			SELECT seasons.content_id FROM seasons
			JOIN UNNEST($1::text[], $2::int[]) AS target(series_id, season_number) USING (series_id, season_number)
			ORDER BY seasons.content_id
			FOR NO KEY UPDATE OF seasons`, seriesIDs, seasonNums)
			if err != nil {
				return err
			}
			rows, err := tx.Query(ctx, query,
				contentIDs, seriesIDs, seasonNums, titles, defaultMetadataLanguages, overviews,
				airDates, posterPaths, posterSourcePaths, posterThumbs,
				metaS3Paths, metaEtags, metaSources,
			)
			if err != nil {
				return fmt.Errorf("bulk upserting seasons: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var cid string
				var sn int32
				var inserted bool
				if err := rows.Scan(&cid, &sn, &inserted); err != nil {
					return fmt.Errorf("scanning bulk upsert season result: %w", err)
				}
				if err := checkArtworkRowLocked(locked, cid, inserted); err != nil {
					return err
				}
				returnedIDs[sn] = cid
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating bulk upsert season results: %w", err)
			}
			return writeSeasonBackdrops(ctx, tx, seasons)
		})
	})
	if err != nil {
		return err
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

// ListBySeriesAndNumbers returns the requested season rows for one series in a
// single query. Missing season numbers are omitted.
func (r *SeasonRepository) ListBySeriesAndNumbers(ctx context.Context, seriesID string, seasonNumbers []int32) ([]*models.Season, error) {
	if len(seasonNumbers) == 0 {
		return nil, nil
	}
	query := `SELECT ` + seasonColumns + `
		FROM seasons
		WHERE series_id = $1 AND season_number = ANY($2::int[])
		ORDER BY season_number ASC`

	rows, err := r.pool.Query(ctx, query, seriesID, seasonNumbers)
	if err != nil {
		return nil, fmt.Errorf("listing seasons by series and numbers: %w", err)
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
// setting only the non-nil fields in upd and NULLing the ones it clears.
// Always bumps updated_at.
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
	setClauses = append(setClauses, upd.nullClauses(seasonClearableColumns...)...)

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

// UpdateArtworkIfSourceMatches stores the cached copy of a season's poster or
// backdrop, named by imageType, while the row still points at sourcePath.
func (r *SeasonRepository) UpdateArtworkIfSourceMatches(ctx context.Context, contentID, imageType, sourcePath, cachedPath, thumbhash string) (bool, error) {
	// The image type names the column family: poster_* or backdrop_*.
	if imageType != artworkImagePoster && imageType != artworkImageBackdrop {
		return false, fmt.Errorf("updating season cached artwork: seasons do not hold %q images", imageType)
	}
	tag, err := r.pool.Exec(ctx, fmt.Sprintf(`
		UPDATE seasons
		SET %[1]s_path = $3,
			%[1]s_source_path = $2,
			%[1]s_thumbhash = $4,
			updated_at = NOW()
		WHERE content_id = $1
		  AND %[1]s_source_path = $2
	`, imageType), contentID, sourcePath, cachedPath, thumbhash)
	if err != nil {
		return false, fmt.Errorf("updating season cached artwork: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
