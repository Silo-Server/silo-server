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

// Sentinel errors for episode repository operations.
var (
	ErrEpisodeNotFound = errors.New("episode not found")
)

// EpisodeRepository provides CRUD operations for the episodes table.
type EpisodeRepository struct {
	pool *pgxpool.Pool
}

// NewEpisodeRepository creates a new EpisodeRepository backed by the given pool.
func NewEpisodeRepository(pool *pgxpool.Pool) *EpisodeRepository {
	return &EpisodeRepository{pool: pool}
}

// episodeColumns is the list of columns returned by all SELECT queries on episodes.
const episodeColumns = `content_id, series_id, season_id, season_number, episode_number,
	title, default_metadata_language, overview, air_date, runtime,
	rating_imdb, rating_tmdb,
	imdb_id, tmdb_id, tvdb_id,
	still_path, still_source_path, still_thumbhash,
	metadata_s3_path, metadata_etag, metadata_source,
	created_at, updated_at`

// updateSeriesLastAirDateSQL maintains the denormalized
// media_items.last_air_date_at column for a single series after an Upsert.
// Audit 2026-05-01 §2.1 hot path #1: replaces a per-row correlated
// subquery with a column read.
//
// MAINTENANCE INVARIANT: This column is currently kept fresh only on
// Upsert/BulkUpsert. The repo has no episode-delete path, and
// TestNoEpisodeDeletePath_ProtectsLastAirDateDenorm pins that absence.
// If you add a delete path, also recompute last_air_date_at for the
// parent series (or add a DB trigger).
const updateSeriesLastAirDateSQL = `
	UPDATE media_items mi
	SET last_air_date_at = sub.last_aired
	FROM (
		SELECT MAX(e.air_date) AS last_aired
		FROM episodes e
		WHERE e.series_id = $1 AND e.air_date IS NOT NULL AND e.air_date <= CURRENT_DATE
	) sub
	WHERE mi.content_id = $1 AND mi.type = 'series'
	  AND (mi.last_air_date_at IS DISTINCT FROM sub.last_aired)`

// batchUpdateSeriesLastAirDateSQL is the multi-series form used by
// BulkUpsert. Single round-trip regardless of episode count.
//
// Drive the subquery from UNNEST($1) with a LEFT JOIN so every input
// series_id produces exactly one row in `sub` — including series whose
// entire episode set has air_date NULL or in the future, where MAX is
// NULL. Without this, GROUP BY over a filtered `episodes` scan would
// drop those series and leave a stale media_items.last_air_date_at
// from an earlier sync (e.g., a series whose air dates were corrected
// to NULL would never reset). The IS DISTINCT FROM guard handles NULL
// vs non-NULL comparison correctly.
const batchUpdateSeriesLastAirDateSQL = `
	UPDATE media_items mi
	SET last_air_date_at = sub.last_aired
	FROM (
		SELECT s.series_id, MAX(e.air_date) AS last_aired
		FROM unnest($1::text[]) AS s(series_id)
		LEFT JOIN episodes e
			ON e.series_id = s.series_id
		   AND e.air_date IS NOT NULL
		   AND e.air_date <= CURRENT_DATE
		GROUP BY s.series_id
	) sub
	WHERE mi.content_id = sub.series_id
	  AND mi.type = 'series'
	  AND (mi.last_air_date_at IS DISTINCT FROM sub.last_aired)`

// buildUpdateSeriesLastAirDateSQL exposes the single-series maintenance
// SQL for unit-test inspection.
func (r *EpisodeRepository) buildUpdateSeriesLastAirDateSQL() string {
	return updateSeriesLastAirDateSQL
}

// buildBatchUpdateLastAirDateSQL exposes the bulk maintenance SQL for
// unit-test inspection.
func (r *EpisodeRepository) buildBatchUpdateLastAirDateSQL() string {
	return batchUpdateSeriesLastAirDateSQL
}

const episodeAvailabilityPredicate = `EXISTS (
		SELECT 1
		FROM episode_libraries el
		WHERE el.episode_id = episodes.content_id
	)`

// scanEpisode scans a single row into a *models.Episode.
func scanEpisode(row pgx.Row) (*models.Episode, error) {
	var ep models.Episode
	var seasonID *string
	var runtime *int
	var overview *string
	var imdbID *string
	var tmdbID *string
	var tvdbID *string
	var stillPath *string
	var stillSourcePath *string
	var stillThumbhash *string
	var metadataS3Path *string
	var metadataEtag *string
	err := row.Scan(
		&ep.ContentID,
		&ep.SeriesID,
		&seasonID,
		&ep.SeasonNumber,
		&ep.EpisodeNumber,
		&ep.Title,
		&ep.DefaultMetadataLanguage,
		&overview,
		&ep.AirDate,
		&runtime,
		&ep.RatingIMDB,
		&ep.RatingTMDB,
		&imdbID,
		&tmdbID,
		&tvdbID,
		&stillPath,
		&stillSourcePath,
		&stillThumbhash,
		&metadataS3Path,
		&metadataEtag,
		&ep.MetadataSource,
		&ep.CreatedAt,
		&ep.UpdatedAt,
	)
	if seasonID != nil {
		ep.SeasonID = *seasonID
	}
	if runtime != nil {
		ep.Runtime = *runtime
	}
	if overview != nil {
		ep.Overview = *overview
	}
	if imdbID != nil {
		ep.ImdbID = *imdbID
	}
	if tmdbID != nil {
		ep.TmdbID = *tmdbID
	}
	if tvdbID != nil {
		ep.TvdbID = *tvdbID
	}
	if stillPath != nil {
		ep.StillPath = *stillPath
	}
	if stillSourcePath != nil {
		ep.StillSourcePath = *stillSourcePath
	}
	if stillThumbhash != nil {
		ep.StillThumbhash = *stillThumbhash
	}
	if metadataS3Path != nil {
		ep.MetadataS3Path = *metadataS3Path
	}
	if metadataEtag != nil {
		ep.MetadataEtag = *metadataEtag
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEpisodeNotFound
		}
		return nil, fmt.Errorf("scanning episode: %w", err)
	}
	return &ep, nil
}

// scanEpisodes scans multiple rows into a []*models.Episode slice.
func scanEpisodes(rows pgx.Rows) ([]*models.Episode, error) {
	var episodes []*models.Episode
	for rows.Next() {
		var ep models.Episode
		var seasonID *string
		var runtime *int
		var overview *string
		var imdbID *string
		var tmdbID *string
		var tvdbID *string
		var stillPath *string
		var stillSourcePath *string
		var stillThumbhash *string
		var metadataS3Path *string
		var metadataEtag *string
		err := rows.Scan(
			&ep.ContentID,
			&ep.SeriesID,
			&seasonID,
			&ep.SeasonNumber,
			&ep.EpisodeNumber,
			&ep.Title,
			&ep.DefaultMetadataLanguage,
			&overview,
			&ep.AirDate,
			&runtime,
			&ep.RatingIMDB,
			&ep.RatingTMDB,
			&imdbID,
			&tmdbID,
			&tvdbID,
			&stillPath,
			&stillSourcePath,
			&stillThumbhash,
			&metadataS3Path,
			&metadataEtag,
			&ep.MetadataSource,
			&ep.CreatedAt,
			&ep.UpdatedAt,
		)
		if seasonID != nil {
			ep.SeasonID = *seasonID
		}
		if runtime != nil {
			ep.Runtime = *runtime
		}
		if overview != nil {
			ep.Overview = *overview
		}
		if imdbID != nil {
			ep.ImdbID = *imdbID
		}
		if tmdbID != nil {
			ep.TmdbID = *tmdbID
		}
		if tvdbID != nil {
			ep.TvdbID = *tvdbID
		}
		if stillPath != nil {
			ep.StillPath = *stillPath
		}
		if stillSourcePath != nil {
			ep.StillSourcePath = *stillSourcePath
		}
		if stillThumbhash != nil {
			ep.StillThumbhash = *stillThumbhash
		}
		if metadataS3Path != nil {
			ep.MetadataS3Path = *metadataS3Path
		}
		if metadataEtag != nil {
			ep.MetadataEtag = *metadataEtag
		}
		if err != nil {
			return nil, fmt.Errorf("scanning episode row: %w", err)
		}
		episodes = append(episodes, &ep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating episode rows: %w", err)
	}
	return episodes, nil
}

// Upsert inserts a new episode or updates all mutable fields if the
// (series_id, season_number, episode_number) natural key already exists. The
// stored content ID is preserved on update and written back to ep.ContentID.
func (r *EpisodeRepository) Upsert(ctx context.Context, ep *models.Episode) error {
	var seasonID *string
	if ep.SeasonID != "" {
		seasonID = &ep.SeasonID
	}

	// Clear stale external IDs from other episodes in the same series as a
	// separate statement. This MUST run before the INSERT because PostgreSQL
	// data-modifying CTEs share the same snapshot as the main query, so an
	// inline CTE's UPDATE is invisible to the INSERT's unique-constraint check.
	clearQuery := `
		UPDATE episodes SET
			imdb_id = CASE WHEN $2 <> '' AND imdb_id = $2 THEN '' ELSE imdb_id END,
			tmdb_id = CASE WHEN $3 <> '' AND tmdb_id = $3 THEN '' ELSE tmdb_id END,
			tvdb_id = CASE WHEN $4 <> '' AND tvdb_id = $4 THEN '' ELSE tvdb_id END
		WHERE series_id = $1
		  AND (season_number, episode_number) <> ($5, $6)
		  AND (($2 <> '' AND imdb_id = $2) OR ($3 <> '' AND tmdb_id = $3) OR ($4 <> '' AND tvdb_id = $4))`
	if _, err := r.pool.Exec(ctx, clearQuery,
		ep.SeriesID,
		ep.ImdbID,
		ep.TmdbID,
		ep.TvdbID,
		ep.SeasonNumber,
		ep.EpisodeNumber,
	); err != nil {
		return fmt.Errorf("clearing stale episode external IDs: %w", err)
	}

	query := `
		INSERT INTO episodes (
			content_id, series_id, season_id, season_number, episode_number,
			title, default_metadata_language, overview, air_date, runtime,
			rating_imdb, rating_tmdb,
			imdb_id, tmdb_id, tvdb_id,
			still_path, still_source_path, still_thumbhash,
			metadata_s3_path, metadata_etag, metadata_source
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12,
			$13, $14, $15,
			$16, $17, $18,
			$19, $20, $21
		)
		ON CONFLICT (series_id, season_number, episode_number) DO UPDATE SET
			season_id = COALESCE(EXCLUDED.season_id, episodes.season_id),
			title = EXCLUDED.title,
			default_metadata_language = COALESCE(NULLIF(episodes.default_metadata_language, ''), EXCLUDED.default_metadata_language),
			overview = EXCLUDED.overview,
			air_date = EXCLUDED.air_date,
			runtime = EXCLUDED.runtime,
			rating_imdb = EXCLUDED.rating_imdb,
			rating_tmdb = EXCLUDED.rating_tmdb,
			imdb_id = COALESCE(NULLIF(EXCLUDED.imdb_id, ''), episodes.imdb_id),
			tmdb_id = COALESCE(NULLIF(EXCLUDED.tmdb_id, ''), episodes.tmdb_id),
			tvdb_id = COALESCE(NULLIF(EXCLUDED.tvdb_id, ''), episodes.tvdb_id),
			still_path = EXCLUDED.still_path,
			still_source_path = EXCLUDED.still_source_path,
			still_thumbhash = EXCLUDED.still_thumbhash,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			metadata_source = EXCLUDED.metadata_source,
			updated_at = NOW()
		RETURNING content_id`

	var storedContentID string
	err := r.pool.QueryRow(ctx, query,
		ep.ContentID,
		ep.SeriesID,
		seasonID,
		ep.SeasonNumber,
		ep.EpisodeNumber,
		ep.Title,
		ep.DefaultMetadataLanguage,
		ep.Overview,
		ep.AirDate,
		ep.Runtime,
		ep.RatingIMDB,
		ep.RatingTMDB,
		ep.ImdbID,
		ep.TmdbID,
		ep.TvdbID,
		ep.StillPath,
		ep.StillSourcePath,
		ep.StillThumbhash,
		ep.MetadataS3Path,
		ep.MetadataEtag,
		ep.MetadataSource,
	).Scan(&storedContentID)
	if err != nil {
		return fmt.Errorf("upserting episode: %w", err)
	}
	ep.ContentID = storedContentID

	// Maintain denormalized media_items.last_air_date_at for the parent
	// series (audit 2026-05-01 §2.1 hot path #1).
	if _, err := r.pool.Exec(ctx, updateSeriesLastAirDateSQL, ep.SeriesID); err != nil {
		return fmt.Errorf("update series last_air_date_at: %w", err)
	}

	return nil
}

// BulkUpsert inserts or updates multiple episodes for a single series in two
// round-trips: one to clear stale external IDs, one for the multi-row upsert.
// Stored content IDs are written back to each Episode struct.
func (r *EpisodeRepository) BulkUpsert(ctx context.Context, seriesID string, episodes []*models.Episode) error {
	if len(episodes) == 0 {
		return nil
	}

	// Collect external IDs and their owning (season_number, episode_number)
	// pairs for the batch stale-ID clear.
	imdbIDs := make([]string, 0, len(episodes))
	tmdbIDs := make([]string, 0, len(episodes))
	tvdbIDs := make([]string, 0, len(episodes))
	ownerSNs := make([]int32, 0, len(episodes))
	ownerENs := make([]int32, 0, len(episodes))
	for _, ep := range episodes {
		if ep.ImdbID != "" || ep.TmdbID != "" || ep.TvdbID != "" {
			imdbIDs = append(imdbIDs, ep.ImdbID)
			tmdbIDs = append(tmdbIDs, ep.TmdbID)
			tvdbIDs = append(tvdbIDs, ep.TvdbID)
			ownerSNs = append(ownerSNs, int32(ep.SeasonNumber))
			ownerENs = append(ownerENs, int32(ep.EpisodeNumber))
		}
	}

	// Step 1: Clear stale external IDs. This MUST run as a separate statement
	// before the INSERT (PostgreSQL data-modifying CTEs share the same snapshot).
	if len(imdbIDs) > 0 {
		clearQuery := `
			UPDATE episodes SET
				imdb_id = CASE WHEN imdb_id <> '' AND imdb_id = ANY($2::text[])
					AND NOT EXISTS (
						SELECT 1 FROM UNNEST($5::int[], $6::int[], $2::text[]) AS t(sn, en, eid)
						WHERE t.sn = episodes.season_number AND t.en = episodes.episode_number AND t.eid = episodes.imdb_id
					) THEN '' ELSE imdb_id END,
				tmdb_id = CASE WHEN tmdb_id <> '' AND tmdb_id = ANY($3::text[])
					AND NOT EXISTS (
						SELECT 1 FROM UNNEST($5::int[], $6::int[], $3::text[]) AS t(sn, en, eid)
						WHERE t.sn = episodes.season_number AND t.en = episodes.episode_number AND t.eid = episodes.tmdb_id
					) THEN '' ELSE tmdb_id END,
				tvdb_id = CASE WHEN tvdb_id <> '' AND tvdb_id = ANY($4::text[])
					AND NOT EXISTS (
						SELECT 1 FROM UNNEST($5::int[], $6::int[], $4::text[]) AS t(sn, en, eid)
						WHERE t.sn = episodes.season_number AND t.en = episodes.episode_number AND t.eid = episodes.tvdb_id
					) THEN '' ELSE tvdb_id END
			WHERE series_id = $1
			  AND (
				(imdb_id <> '' AND imdb_id = ANY($2::text[]))
				OR (tmdb_id <> '' AND tmdb_id = ANY($3::text[]))
				OR (tvdb_id <> '' AND tvdb_id = ANY($4::text[]))
			  )`
		if _, err := r.pool.Exec(ctx, clearQuery,
			seriesID, imdbIDs, tmdbIDs, tvdbIDs, ownerSNs, ownerENs,
		); err != nil {
			return fmt.Errorf("bulk clearing stale episode external IDs: %w", err)
		}
	}

	// Step 2: Multi-row upsert.
	contentIDs := make([]string, len(episodes))
	epSeriesIDs := make([]string, len(episodes))
	seasonIDs := make([]*string, len(episodes))
	seasonNums := make([]int32, len(episodes))
	episodeNums := make([]int32, len(episodes))
	titles := make([]string, len(episodes))
	defaultMetadataLanguages := make([]string, len(episodes))
	overviews := make([]string, len(episodes))
	airDates := make([]*time.Time, len(episodes))
	runtimes := make([]int32, len(episodes))
	ratingsIMDB := make([]*float64, len(episodes))
	ratingsTMDB := make([]*float64, len(episodes))
	epImdbIDs := make([]string, len(episodes))
	epTmdbIDs := make([]string, len(episodes))
	epTvdbIDs := make([]string, len(episodes))
	stillPaths := make([]string, len(episodes))
	stillSourcePaths := make([]string, len(episodes))
	stillThumbs := make([]string, len(episodes))
	metaS3Paths := make([]string, len(episodes))
	metaEtags := make([]string, len(episodes))
	metaSources := make([]string, len(episodes))

	for i, ep := range episodes {
		contentIDs[i] = ep.ContentID
		epSeriesIDs[i] = ep.SeriesID
		if ep.SeasonID != "" {
			seasonIDs[i] = &ep.SeasonID
		}
		seasonNums[i] = int32(ep.SeasonNumber)
		episodeNums[i] = int32(ep.EpisodeNumber)
		titles[i] = ep.Title
		defaultMetadataLanguages[i] = ep.DefaultMetadataLanguage
		overviews[i] = ep.Overview
		airDates[i] = ep.AirDate
		runtimes[i] = int32(ep.Runtime)
		ratingsIMDB[i] = ep.RatingIMDB
		ratingsTMDB[i] = ep.RatingTMDB
		epImdbIDs[i] = ep.ImdbID
		epTmdbIDs[i] = ep.TmdbID
		epTvdbIDs[i] = ep.TvdbID
		stillPaths[i] = ep.StillPath
		stillSourcePaths[i] = ep.StillSourcePath
		stillThumbs[i] = ep.StillThumbhash
		metaS3Paths[i] = ep.MetadataS3Path
		metaEtags[i] = ep.MetadataEtag
		metaSources[i] = ep.MetadataSource
	}

	query := `
		INSERT INTO episodes (
			content_id, series_id, season_id, season_number, episode_number,
			title, default_metadata_language, overview, air_date, runtime,
			rating_imdb, rating_tmdb,
			imdb_id, tmdb_id, tvdb_id,
			still_path, still_source_path, still_thumbhash,
			metadata_s3_path, metadata_etag, metadata_source
		)
		SELECT * FROM UNNEST(
			$1::text[], $2::text[], $3::text[], $4::int[], $5::int[],
			$6::text[], $7::text[], $8::text[], $9::date[], $10::int[],
			$11::float8[], $12::float8[],
			$13::text[], $14::text[], $15::text[],
			$16::text[], $17::text[], $18::text[],
			$19::text[], $20::text[], $21::text[]
		)
		ON CONFLICT (series_id, season_number, episode_number) DO UPDATE SET
			season_id = COALESCE(EXCLUDED.season_id, episodes.season_id),
			title = EXCLUDED.title,
			default_metadata_language = COALESCE(NULLIF(episodes.default_metadata_language, ''), EXCLUDED.default_metadata_language),
			overview = EXCLUDED.overview,
			air_date = EXCLUDED.air_date,
			runtime = EXCLUDED.runtime,
			rating_imdb = EXCLUDED.rating_imdb,
			rating_tmdb = EXCLUDED.rating_tmdb,
			imdb_id = COALESCE(NULLIF(EXCLUDED.imdb_id, ''), episodes.imdb_id),
			tmdb_id = COALESCE(NULLIF(EXCLUDED.tmdb_id, ''), episodes.tmdb_id),
			tvdb_id = COALESCE(NULLIF(EXCLUDED.tvdb_id, ''), episodes.tvdb_id),
			still_path = EXCLUDED.still_path,
			still_source_path = EXCLUDED.still_source_path,
			still_thumbhash = EXCLUDED.still_thumbhash,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			metadata_source = EXCLUDED.metadata_source,
			updated_at = NOW()
		RETURNING content_id, season_number, episode_number`

	rows, err := r.pool.Query(ctx, query,
		contentIDs, epSeriesIDs, seasonIDs, seasonNums, episodeNums,
		titles, defaultMetadataLanguages, overviews, airDates, runtimes,
		ratingsIMDB, ratingsTMDB,
		epImdbIDs, epTmdbIDs, epTvdbIDs,
		stillPaths, stillSourcePaths, stillThumbs,
		metaS3Paths, metaEtags, metaSources,
	)
	if err != nil {
		return fmt.Errorf("bulk upserting episodes: %w", err)
	}
	defer rows.Close()

	// Build a map to write back content IDs by (season_number, episode_number).
	type epKey struct {
		sn, en int32
	}
	returnedIDs := make(map[epKey]string, len(episodes))
	for rows.Next() {
		var cid string
		var sn, en int32
		if err := rows.Scan(&cid, &sn, &en); err != nil {
			return fmt.Errorf("scanning bulk upsert episode result: %w", err)
		}
		returnedIDs[epKey{sn, en}] = cid
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating bulk upsert episode results: %w", err)
	}

	for _, ep := range episodes {
		if cid, ok := returnedIDs[epKey{int32(ep.SeasonNumber), int32(ep.EpisodeNumber)}]; ok {
			ep.ContentID = cid
		}
	}

	// Maintain denormalized media_items.last_air_date_at for the parent
	// series (audit 2026-05-01 §2.1 hot path #1). BulkUpsert is called for
	// a single series, but the SQL uses ANY($1::text[]) to be future-proof
	// for multi-series scanner batches.
	if _, err := r.pool.Exec(ctx, batchUpdateSeriesLastAirDateSQL, []string{seriesID}); err != nil {
		return fmt.Errorf("batch update last_air_date_at: %w", err)
	}

	return nil
}

// GetBySeriesAndNumber retrieves a specific episode by series ID, season, and
// episode number.
func (r *EpisodeRepository) GetBySeriesAndNumber(ctx context.Context, seriesID string, season, episode int) (*models.Episode, error) {
	query := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE series_id = $1 AND season_number = $2 AND episode_number = $3`
	return scanEpisode(r.pool.QueryRow(ctx, query, seriesID, season, episode))
}

// ListBySeriesAndAirDates retrieves provider episode rows for a series keyed by
// air date. It intentionally does not use episodeAvailabilityPredicate because
// callers use it to link files before episode_libraries rows exist.
func (r *EpisodeRepository) ListBySeriesAndAirDates(ctx context.Context, seriesID string, airDates []string) (map[string][]*models.Episode, error) {
	result := make(map[string][]*models.Episode, len(airDates))
	if len(airDates) == 0 {
		return result, nil
	}

	seen := make(map[string]struct{}, len(airDates))
	args := []any{seriesID}
	placeholders := make([]string, 0, len(airDates))
	for _, airDate := range airDates {
		airDate = strings.TrimSpace(airDate)
		if airDate == "" {
			continue
		}
		if _, ok := seen[airDate]; ok {
			continue
		}
		seen[airDate] = struct{}{}
		args = append(args, airDate)
		placeholders = append(placeholders, fmt.Sprintf("$%d::date", len(args)))
	}
	if len(placeholders) == 0 {
		return result, nil
	}

	query := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE series_id = $1
		  AND air_date IN (` + strings.Join(placeholders, ", ") + `)
		ORDER BY air_date ASC, season_number ASC, episode_number ASC`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing episodes by series and air dates: %w", err)
	}
	defer rows.Close()

	episodes, err := scanEpisodes(rows)
	if err != nil {
		return nil, err
	}
	for _, ep := range episodes {
		if ep.AirDate == nil {
			continue
		}
		key := ep.AirDate.Format("2006-01-02")
		result[key] = append(result[key], ep)
	}
	return result, nil
}

// GetByID retrieves a specific episode by its content ID.
func (r *EpisodeRepository) GetByID(ctx context.Context, contentID string) (*models.Episode, error) {
	query := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE content_id = $1`
	return scanEpisode(r.pool.QueryRow(ctx, query, contentID))
}

// GetByIDs retrieves multiple episodes by their content IDs.
func (r *EpisodeRepository) GetByIDs(ctx context.Context, contentIDs []string) ([]*models.Episode, error) {
	if len(contentIDs) == 0 {
		return nil, nil
	}
	query := `SELECT ` + episodeColumns + ` FROM episodes WHERE content_id = ANY($1)`
	rows, err := r.pool.Query(ctx, query, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("fetching episodes by IDs: %w", err)
	}
	defer rows.Close()
	return scanEpisodes(rows)
}

// HasFilesByIDs reports which of the given episodes are backed by at least one
// live (non-missing) media file. Episodes absent from the result map have no
// file — e.g. provider-metadata-only entries for unaired episodes.
func (r *EpisodeRepository) HasFilesByIDs(ctx context.Context, contentIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(contentIDs))
	if len(contentIDs) == 0 {
		return result, nil
	}
	query := `SELECT episode_id FROM media_files
		WHERE episode_id = ANY($1) AND missing_since IS NULL
		GROUP BY episode_id`
	rows, err := r.pool.Query(ctx, query, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("checking episode file presence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var episodeID string
		if err := rows.Scan(&episodeID); err != nil {
			return nil, fmt.Errorf("scanning episode file presence: %w", err)
		}
		result[episodeID] = true
	}
	return result, rows.Err()
}

// ListBySeries returns all episodes for a given series, ordered by season and
// episode number.
func (r *EpisodeRepository) ListBySeries(ctx context.Context, seriesID string) ([]*models.Episode, error) {
	query := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE series_id = $1
		  AND ` + episodeAvailabilityPredicate + `
		ORDER BY season_number ASC, episode_number ASC`

	rows, err := r.pool.Query(ctx, query, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing episodes by series: %w", err)
	}
	defer rows.Close()

	return scanEpisodes(rows)
}

func (r *EpisodeRepository) ListBySeriesIDs(ctx context.Context, seriesIDs []string) (map[string][]*models.Episode, error) {
	result := make(map[string][]*models.Episode, len(seriesIDs))
	if len(seriesIDs) == 0 {
		return result, nil
	}

	query := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE series_id = ANY($1)
		  AND ` + episodeAvailabilityPredicate + `
		ORDER BY series_id ASC, season_number ASC, episode_number ASC`

	rows, err := r.pool.Query(ctx, query, seriesIDs)
	if err != nil {
		return nil, fmt.Errorf("listing episodes by series ids: %w", err)
	}
	defer rows.Close()

	episodes, err := scanEpisodes(rows)
	if err != nil {
		return nil, err
	}
	for _, episode := range episodes {
		result[episode.SeriesID] = append(result[episode.SeriesID], episode)
	}
	return result, nil
}

// buildListBySeriesGroupedBySeasonQuery returns the SQL and bound args used by
// ListBySeriesGroupedBySeason. Extracted so tests can assert SQL shape without
// a live Postgres pool.
func (r *EpisodeRepository) buildListBySeriesGroupedBySeasonQuery(seriesID string) (string, []any) {
	sql := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE series_id = $1
		  AND ` + episodeAvailabilityPredicate + `
		ORDER BY season_number ASC, episode_number ASC`
	return sql, []any{seriesID}
}

// ListBySeriesGroupedBySeason returns all available episodes for a series
// grouped by season number. Replaces N+1 per-season ListBySeason calls
// (audit 2026-05-01 §2.2).
func (r *EpisodeRepository) ListBySeriesGroupedBySeason(ctx context.Context, seriesID string) (map[int][]*models.Episode, error) {
	query, args := r.buildListBySeriesGroupedBySeasonQuery(seriesID)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing episodes grouped by season: %w", err)
	}
	defer rows.Close()

	episodes, err := scanEpisodes(rows)
	if err != nil {
		return nil, err
	}
	grouped := make(map[int][]*models.Episode)
	for _, ep := range episodes {
		grouped[ep.SeasonNumber] = append(grouped[ep.SeasonNumber], ep)
	}
	return grouped, nil
}

// SeasonSummary contains summary info about a season.
type SeasonSummary struct {
	SeasonNumber int `json:"season_number"`
	EpisodeCount int `json:"episode_count"`
}

// ListSeasons returns a summary of all seasons for a given series.
func (r *EpisodeRepository) ListSeasons(ctx context.Context, seriesID string) ([]SeasonSummary, error) {
	query := `SELECT season_number, COUNT(*) AS episode_count
		FROM episodes
		WHERE series_id = $1
		  AND ` + episodeAvailabilityPredicate + `
		GROUP BY season_number
		ORDER BY season_number ASC`

	rows, err := r.pool.Query(ctx, query, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing seasons: %w", err)
	}
	defer rows.Close()

	var seasons []SeasonSummary
	for rows.Next() {
		var s SeasonSummary
		if err := rows.Scan(&s.SeasonNumber, &s.EpisodeCount); err != nil {
			return nil, fmt.Errorf("scanning season summary: %w", err)
		}
		seasons = append(seasons, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating season rows: %w", err)
	}
	return seasons, nil
}

// CountBySeries returns the number of episodes for a given series.
func (r *EpisodeRepository) CountBySeries(ctx context.Context, seriesID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*)
		FROM episodes
		WHERE series_id = $1
		  AND `+episodeAvailabilityPredicate, seriesID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting episodes by series: %w", err)
	}
	return count, nil
}

// ListBySeason returns all episodes for a specific season of a series, ordered
// by episode number.
func (r *EpisodeRepository) ListBySeason(ctx context.Context, seriesID string, seasonNum int) ([]*models.Episode, error) {
	query := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE series_id = $1 AND season_number = $2
		  AND ` + episodeAvailabilityPredicate + `
		ORDER BY episode_number ASC`

	rows, err := r.pool.Query(ctx, query, seriesID, seasonNum)
	if err != nil {
		return nil, fmt.Errorf("listing episodes by season: %w", err)
	}
	defer rows.Close()

	return scanEpisodes(rows)
}

// ListBySeasonID returns all episodes for a specific season row, ordered by
// episode number.
func (r *EpisodeRepository) ListBySeasonID(ctx context.Context, seasonID string) ([]*models.Episode, error) {
	query := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE season_id = $1
		  AND ` + episodeAvailabilityPredicate + `
		ORDER BY episode_number ASC`

	rows, err := r.pool.Query(ctx, query, seasonID)
	if err != nil {
		return nil, fmt.Errorf("listing episodes by season_id: %w", err)
	}
	defer rows.Close()

	return scanEpisodes(rows)
}

func (r *EpisodeRepository) ListBySeasonIDs(ctx context.Context, seasonIDs []string) (map[string][]*models.Episode, error) {
	result := make(map[string][]*models.Episode, len(seasonIDs))
	if len(seasonIDs) == 0 {
		return result, nil
	}

	query := `SELECT ` + episodeColumns + `
		FROM episodes
		WHERE season_id = ANY($1)
		  AND ` + episodeAvailabilityPredicate + `
		ORDER BY season_id ASC, season_number ASC, episode_number ASC`

	rows, err := r.pool.Query(ctx, query, seasonIDs)
	if err != nil {
		return nil, fmt.Errorf("listing episodes by season ids: %w", err)
	}
	defer rows.Close()

	episodes, err := scanEpisodes(rows)
	if err != nil {
		return nil, err
	}
	for _, episode := range episodes {
		result[episode.SeasonID] = append(result[episode.SeasonID], episode)
	}
	return result, nil
}

// UpdateMetadata builds a dynamic UPDATE query for the episodes table,
// setting only the non-nil fields in upd. Always bumps updated_at.
// Returns ErrEpisodeNotFound if no row matches contentID.
func (r *EpisodeRepository) UpdateMetadata(ctx context.Context, contentID string, upd *MetadataUpdate) error {
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
	addFloat := func(col string, val *float64) {
		if val != nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, *val)
			argIdx++
		}
	}

	addString("title", upd.Title)
	addString("overview", upd.Overview)
	addInt("episode_number", upd.EpisodeNumber)
	addInt("season_number", upd.SeasonNumber)
	addString("air_date", upd.AirDate)
	addInt("runtime", upd.Runtime)
	addFloat("rating_imdb", upd.RatingIMDB)
	addFloat("rating_tmdb", upd.RatingTMDB)
	addString("imdb_id", upd.ImdbID)
	addString("tmdb_id", upd.TmdbID)
	addString("tvdb_id", upd.TvdbID)
	addString("still_path", upd.StillPath)
	if upd.StillPath != nil && upd.StillSourcePath == nil {
		setClauses = append(setClauses, "still_source_path = ''")
	}
	addString("still_source_path", upd.StillSourcePath)
	addString("still_thumbhash", upd.StillThumbhash)

	setClauses = append(setClauses, "updated_at = NOW()")

	query := fmt.Sprintf("UPDATE episodes SET %s WHERE content_id = $%d",
		strings.Join(setClauses, ", "), argIdx)
	args = append(args, contentID)

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating episode metadata: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrEpisodeNotFound
	}
	return nil
}

func (r *EpisodeRepository) UpdateStillIfSourceMatches(ctx context.Context, contentID, sourcePath, cachedPath, thumbhash string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE episodes
		SET still_path = $3,
			still_source_path = $2,
			still_thumbhash = $4,
			updated_at = NOW()
		WHERE content_id = $1
		  AND still_source_path = $2
	`, contentID, sourcePath, cachedPath, thumbhash)
	if err != nil {
		return false, fmt.Errorf("updating episode cached still: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
