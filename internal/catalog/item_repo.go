package catalog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pathscope"
)

// Sentinel errors for item repository operations.
var (
	ErrItemNotFound = errors.New("media item not found")
)

// ItemRepository provides CRUD operations for the media_items table.
type ItemRepository struct {
	pool *pgxpool.Pool
}

type itemExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// NewItemRepository creates a new ItemRepository backed by the given pool.
func NewItemRepository(pool *pgxpool.Pool) *ItemRepository {
	return &ItemRepository{pool: pool}
}

// itemColumns is the list of columns returned by all SELECT queries on media_items.
const itemColumns = `content_id, type, title, sort_title, default_metadata_language, original_title, year, genres,
	content_rating, runtime, overview, tagline,
	rating_imdb, rating_tmdb, rating_rt_critic, rating_rt_audience,
	imdb_id, tmdb_id, tvdb_id,
	poster_path, poster_thumbhash, backdrop_path, backdrop_thumbhash, logo_path,
	metadata_s3_path, metadata_etag, season_count,
	studios, networks, countries, keywords, original_language, release_date::text, first_air_date, last_air_date, air_time,
	show_status,
	matched_at, last_refreshed, refresh_failures,
	episode_metadata_incomplete, episode_metadata_last_checked_at, locked_fields, status, created_at, updated_at`

func qualifiedItemColumns(alias string) string {
	cols := []string{
		"content_id", "type", "title", "sort_title", "default_metadata_language", "original_title", "year", "genres",
		"content_rating", "runtime", "overview", "tagline",
		"rating_imdb", "rating_tmdb", "rating_rt_critic", "rating_rt_audience",
		"imdb_id", "tmdb_id", "tvdb_id",
		"poster_path", "poster_thumbhash", "backdrop_path", "backdrop_thumbhash", "logo_path",
		"metadata_s3_path", "metadata_etag", "season_count",
		"studios", "networks", "countries", "keywords", "original_language", "release_date::text", "first_air_date", "last_air_date", "air_time",
		"show_status",
		"matched_at", "last_refreshed", "refresh_failures",
		"episode_metadata_incomplete", "episode_metadata_last_checked_at", "locked_fields", "status", "created_at", "updated_at",
	}
	prefixed := make([]string, len(cols))
	for i, col := range cols {
		prefixed[i] = alias + "." + col
	}
	return strings.Join(prefixed, ", ")
}

func qualifiedListItemColumns(alias string) string {
	cols := []string{
		"content_id", "type", "title", "sort_title", "default_metadata_language", "original_title", "year", "genres",
		"content_rating", "runtime", "overview", "tagline",
		"rating_imdb", "rating_tmdb", "rating_rt_critic", "rating_rt_audience",
		"imdb_id", "tmdb_id", "tvdb_id",
		"poster_path", "poster_thumbhash", "backdrop_path", "backdrop_thumbhash", "logo_path",
		"metadata_s3_path", "metadata_etag", "season_count",
		"studios", "networks", "countries", "keywords", "original_language", "release_date::text", "first_air_date", "last_air_date", "air_time",
		"show_status",
		"matched_at", "last_refreshed", "refresh_failures",
		"episode_metadata_incomplete", "episode_metadata_last_checked_at", "locked_fields", "status", "created_at", "updated_at",
	}
	prefixed := make([]string, len(cols))
	for i, col := range cols {
		if col == "last_air_date" {
			prefixed[i] = effectiveLastAirDateExpr(alias)
			continue
		}
		prefixed[i] = alias + "." + col
	}
	return strings.Join(prefixed, ", ")
}

// scanItem scans a single row into a *models.MediaItem.
func scanItem(row pgx.Row) (*models.MediaItem, error) {
	var item models.MediaItem
	err := row.Scan(
		&item.ContentID,
		&item.Type,
		&item.Title,
		&item.SortTitle,
		&item.DefaultMetadataLanguage,
		&item.OriginalTitle,
		&item.Year,
		&item.Genres,
		&item.ContentRating,
		&item.Runtime,
		&item.Overview,
		&item.Tagline,
		&item.RatingIMDB,
		&item.RatingTMDB,
		&item.RatingRTCritic,
		&item.RatingRTAudience,
		&item.ImdbID,
		&item.TmdbID,
		&item.TvdbID,
		&item.PosterPath,
		&item.PosterThumbhash,
		&item.BackdropPath,
		&item.BackdropThumbhash,
		&item.LogoPath,
		&item.MetadataS3Path,
		&item.MetadataEtag,
		&item.SeasonCount,
		&item.Studios,
		&item.Networks,
		&item.Countries,
		&item.Keywords,
		&item.OriginalLanguage,
		&item.ReleaseDate,
		&item.FirstAirDate,
		&item.LastAirDate,
		&item.AirTime,
		&item.ShowStatus,
		&item.MatchedAt,
		&item.LastRefreshed,
		&item.RefreshFailures,
		&item.EpisodeMetadataIncomplete,
		&item.EpisodeMetadataLastCheckedAt,
		&item.LockedFields,
		&item.Status,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrItemNotFound
		}
		return nil, fmt.Errorf("scanning media item: %w", err)
	}
	return &item, nil
}

// scanItems scans multiple rows into a []*models.MediaItem slice.
func scanItems(rows pgx.Rows) ([]*models.MediaItem, error) {
	var items []*models.MediaItem
	for rows.Next() {
		var item models.MediaItem
		err := rows.Scan(
			&item.ContentID,
			&item.Type,
			&item.Title,
			&item.SortTitle,
			&item.DefaultMetadataLanguage,
			&item.OriginalTitle,
			&item.Year,
			&item.Genres,
			&item.ContentRating,
			&item.Runtime,
			&item.Overview,
			&item.Tagline,
			&item.RatingIMDB,
			&item.RatingTMDB,
			&item.RatingRTCritic,
			&item.RatingRTAudience,
			&item.ImdbID,
			&item.TmdbID,
			&item.TvdbID,
			&item.PosterPath,
			&item.PosterThumbhash,
			&item.BackdropPath,
			&item.BackdropThumbhash,
			&item.LogoPath,
			&item.MetadataS3Path,
			&item.MetadataEtag,
			&item.SeasonCount,
			&item.Studios,
			&item.Networks,
			&item.Countries,
			&item.Keywords,
			&item.OriginalLanguage,
			&item.ReleaseDate,
			&item.FirstAirDate,
			&item.LastAirDate,
			&item.AirTime,
			&item.ShowStatus,
			&item.MatchedAt,
			&item.LastRefreshed,
			&item.RefreshFailures,
			&item.EpisodeMetadataIncomplete,
			&item.EpisodeMetadataLastCheckedAt,
			&item.LockedFields,
			&item.Status,
			&item.CreatedAt,
			&item.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning media item row: %w", err)
		}
		items = append(items, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating media item rows: %w", err)
	}
	return items, nil
}

// scanItemsWithTotal scans rows that include a trailing total_count column
// emitted by COUNT(*) OVER (). The total is identical for every row in the
// result set; we read it from the first row (or leave it zero when the result
// is empty). Used by paginated paths that previously fired a separate
// SELECT COUNT(*) before the data query.
func scanItemsWithTotal(rows pgx.Rows) ([]*models.MediaItem, int, error) {
	var (
		items []*models.MediaItem
		total int
	)
	for rows.Next() {
		var item models.MediaItem
		var rowTotal int
		err := rows.Scan(
			&item.ContentID,
			&item.Type,
			&item.Title,
			&item.SortTitle,
			&item.DefaultMetadataLanguage,
			&item.OriginalTitle,
			&item.Year,
			&item.Genres,
			&item.ContentRating,
			&item.Runtime,
			&item.Overview,
			&item.Tagline,
			&item.RatingIMDB,
			&item.RatingTMDB,
			&item.RatingRTCritic,
			&item.RatingRTAudience,
			&item.ImdbID,
			&item.TmdbID,
			&item.TvdbID,
			&item.PosterPath,
			&item.PosterThumbhash,
			&item.BackdropPath,
			&item.BackdropThumbhash,
			&item.LogoPath,
			&item.MetadataS3Path,
			&item.MetadataEtag,
			&item.SeasonCount,
			&item.Studios,
			&item.Networks,
			&item.Countries,
			&item.Keywords,
			&item.OriginalLanguage,
			&item.ReleaseDate,
			&item.FirstAirDate,
			&item.LastAirDate,
			&item.AirTime,
			&item.ShowStatus,
			&item.MatchedAt,
			&item.LastRefreshed,
			&item.RefreshFailures,
			&item.EpisodeMetadataIncomplete,
			&item.EpisodeMetadataLastCheckedAt,
			&item.LockedFields,
			&item.Status,
			&item.CreatedAt,
			&item.UpdatedAt,
			&rowTotal,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("scanning media item row with total: %w", err)
		}
		items = append(items, &item)
		total = rowTotal
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating media item rows: %w", err)
	}
	return items, total, nil
}

// Upsert inserts a new media item or updates all mutable fields if the
// content_id already exists. The created_at timestamp is preserved on update.
func (r *ItemRepository) Upsert(ctx context.Context, item *models.MediaItem) error {
	return r.upsert(ctx, r.pool, item)
}

// UpsertTx inserts or updates a media item using the caller's transaction.
func (r *ItemRepository) UpsertTx(ctx context.Context, tx pgx.Tx, item *models.MediaItem) error {
	return r.upsert(ctx, tx, item)
}

func (r *ItemRepository) upsert(ctx context.Context, execer itemExecer, item *models.MediaItem) error {
	if item.ContentID == "" {
		return fmt.Errorf("refusing to upsert media item with empty content_id")
	}
	studios := nonNilStringSlice(item.Studios)
	networks := nonNilStringSlice(item.Networks)
	countries := nonNilStringSlice(item.Countries)
	keywords := nonNilStringSlice(item.Keywords)
	query := `
		INSERT INTO media_items (
			content_id, type, title, sort_title, default_metadata_language, original_title, year, genres,
			content_rating, runtime, overview, tagline,
			rating_imdb, rating_tmdb, rating_rt_critic, rating_rt_audience,
			imdb_id, tmdb_id, tvdb_id,
			poster_path, poster_thumbhash, backdrop_path, backdrop_thumbhash, logo_path,
			metadata_s3_path, metadata_etag, season_count,
			studios, networks, countries, keywords, original_language, release_date, first_air_date, last_air_date, air_time,
			show_status,
			matched_at, last_refreshed, refresh_failures,
			episode_metadata_incomplete, episode_metadata_last_checked_at, status
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12,
			$13, $14, $15, $16,
			$17, $18, $19,
			$20, $21, $22, $23, $24,
			$25, $26, $27,
			$28, $29, $30, $31, $32, $33, $34, $35, $36,
			$37,
			$38, $39, $40,
			$41, $42, $43
		)
		ON CONFLICT (content_id) DO UPDATE SET
			type = EXCLUDED.type,
			title = EXCLUDED.title,
			sort_title = EXCLUDED.sort_title,
			default_metadata_language = COALESCE(NULLIF(media_items.default_metadata_language, ''), EXCLUDED.default_metadata_language),
			original_title = EXCLUDED.original_title,
			year = EXCLUDED.year,
			genres = EXCLUDED.genres,
			content_rating = EXCLUDED.content_rating,
			runtime = EXCLUDED.runtime,
			overview = EXCLUDED.overview,
			tagline = EXCLUDED.tagline,
			rating_imdb = EXCLUDED.rating_imdb,
			rating_tmdb = EXCLUDED.rating_tmdb,
			rating_rt_critic = EXCLUDED.rating_rt_critic,
			rating_rt_audience = EXCLUDED.rating_rt_audience,
			imdb_id = EXCLUDED.imdb_id,
			tmdb_id = EXCLUDED.tmdb_id,
			tvdb_id = EXCLUDED.tvdb_id,
			poster_path = EXCLUDED.poster_path,
			poster_thumbhash = EXCLUDED.poster_thumbhash,
			backdrop_path = EXCLUDED.backdrop_path,
			backdrop_thumbhash = EXCLUDED.backdrop_thumbhash,
			logo_path = EXCLUDED.logo_path,
			metadata_s3_path = EXCLUDED.metadata_s3_path,
			metadata_etag = EXCLUDED.metadata_etag,
			season_count = EXCLUDED.season_count,
			studios = EXCLUDED.studios,
			networks = EXCLUDED.networks,
			countries = EXCLUDED.countries,
			keywords = EXCLUDED.keywords,
			original_language = EXCLUDED.original_language,
			release_date = EXCLUDED.release_date,
			first_air_date = EXCLUDED.first_air_date,
			last_air_date = EXCLUDED.last_air_date,
			air_time = EXCLUDED.air_time,
			show_status = EXCLUDED.show_status,
			matched_at = EXCLUDED.matched_at,
			last_refreshed = EXCLUDED.last_refreshed,
			refresh_failures = EXCLUDED.refresh_failures,
			episode_metadata_incomplete = EXCLUDED.episode_metadata_incomplete,
			episode_metadata_last_checked_at = EXCLUDED.episode_metadata_last_checked_at,
			status = EXCLUDED.status,
			updated_at = NOW()`

	_, err := execer.Exec(ctx, query,
		item.ContentID,
		item.Type,
		item.Title,
		item.SortTitle,
		item.DefaultMetadataLanguage,
		item.OriginalTitle,
		item.Year,
		item.Genres,
		item.ContentRating,
		item.Runtime,
		item.Overview,
		item.Tagline,
		item.RatingIMDB,
		item.RatingTMDB,
		item.RatingRTCritic,
		item.RatingRTAudience,
		item.ImdbID,
		item.TmdbID,
		item.TvdbID,
		item.PosterPath,
		item.PosterThumbhash,
		item.BackdropPath,
		item.BackdropThumbhash,
		item.LogoPath,
		item.MetadataS3Path,
		item.MetadataEtag,
		item.SeasonCount,
		studios,
		networks,
		countries,
		keywords,
		item.OriginalLanguage,
		item.ReleaseDate,
		item.FirstAirDate,
		item.LastAirDate,
		item.AirTime,
		item.ShowStatus,
		item.MatchedAt,
		item.LastRefreshed,
		item.RefreshFailures,
		item.EpisodeMetadataIncomplete,
		item.EpisodeMetadataLastCheckedAt,
		item.Status,
	)
	if err != nil {
		return fmt.Errorf("upserting media item: %w", err)
	}

	return nil
}

func nonNilStringSlice(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// GetByID retrieves a media item by its content ID.
func (r *ItemRepository) GetByID(ctx context.Context, contentID string) (*models.MediaItem, error) {
	query := `SELECT ` + itemColumns + ` FROM media_items WHERE content_id = $1`
	return scanItem(r.pool.QueryRow(ctx, query, contentID))
}

// GetByIDs retrieves multiple media items by their content IDs.
// Items not found are silently omitted from the result.
func (r *ItemRepository) GetByIDs(ctx context.Context, contentIDs []string) ([]*models.MediaItem, error) {
	if len(contentIDs) == 0 {
		return []*models.MediaItem{}, nil
	}

	query := `SELECT ` + itemColumns + ` FROM media_items WHERE content_id = ANY($1) ORDER BY content_id ASC`
	rows, err := r.pool.Query(ctx, query, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("fetching media items by IDs: %w", err)
	}
	defer rows.Close()

	return scanItems(rows)
}

// GetByIDsWithAccess fetches multiple media items by content_id, filtered by
// the access policy in a single query. Returns only items the viewer is
// allowed to see — replaces a per-item EnsureAccessible loop alongside the
// existing batch GetByIDs (audit 2026-05-01 §3.3).
//
// Library-access semantics differ intentionally from EnsureAccessible. That
// method joins media_item_libraries once and applies allowed/disabled
// predicates against the same row, so an item linked to BOTH an allowed and
// a separate disabled library can satisfy the join via the allowed row and
// leak through. GetByIDsWithAccess uses independent EXISTS / NOT EXISTS
// subqueries: the item must be in some allowed library AND not in any
// disabled library. This is per-link rather than per-row and is strictly
// stricter (no leakage when an item spans both an allowed and a disabled
// library). Callers that specifically need the older single-row JOIN form
// should call EnsureAccessible directly; no current caller does.
func (r *ItemRepository) GetByIDsWithAccess(ctx context.Context, contentIDs []string, access AccessFilter) ([]*models.MediaItem, error) {
	if len(contentIDs) == 0 {
		return []*models.MediaItem{}, nil
	}
	if access.AllowedLibraryIDs != nil && len(access.AllowedLibraryIDs) == 0 {
		return []*models.MediaItem{}, nil
	}
	sql, args := r.buildGetByIDsWithAccessSQL(contentIDs, access)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("fetching media items by IDs with access: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

func (r *ItemRepository) buildGetByIDsWithAccessSQL(contentIDs []string, access AccessFilter) (string, []any) {
	sql := `SELECT ` + itemColumns + `
            FROM media_items mi
            WHERE mi.content_id = ANY($1)`
	args := []any{contentIDs}

	argIdx := 2
	if access.AllowedLibraryIDs != nil {
		sql += fmt.Sprintf(`
            AND EXISTS (
                SELECT 1 FROM media_item_libraries mil
                WHERE mil.content_id = mi.content_id
                  AND mil.media_folder_id = ANY($%d)
            )`, argIdx)
		args = append(args, access.AllowedLibraryIDs)
		argIdx++
	}
	if len(access.DisabledLibraryIDs) > 0 {
		// When DisabledLibraryIDs is active without an AllowedLibraryIDs
		// allowlist, also require positive library membership. Otherwise
		// orphan items (rows in media_items with no media_item_libraries
		// link — e.g. mid-scan, stale rows from a removed library, or
		// metadata-refresh inserts not yet linked) would pass the NOT EXISTS
		// (which is true over an empty subquery set) and become visible to
		// users whose access policy is restricted by DisabledLibraryIDs.
		// EnsureAccessible's prior INNER JOIN on media_item_libraries
		// implicitly enforced this membership; the EXISTS pair here makes
		// it explicit. When AllowedLibraryIDs is non-nil, the EXISTS-by-
		// allowed-list above already provides positive membership.
		if access.AllowedLibraryIDs == nil {
			sql += `
            AND EXISTS (
                SELECT 1 FROM media_item_libraries mil
                WHERE mil.content_id = mi.content_id
            )`
		}
		sql += fmt.Sprintf(`
            AND NOT EXISTS (
                SELECT 1 FROM media_item_libraries mil
                WHERE mil.content_id = mi.content_id
                  AND mil.media_folder_id = ANY($%d)
            )`, argIdx)
		args = append(args, access.DisabledLibraryIDs)
		argIdx++
	}

	// Apply MaxContentRating like applyAccessFilter does.
	var ratingConditions []string
	applyAccessFilter("mi", AccessFilter{MaxContentRating: access.MaxContentRating}, &ratingConditions, &args, &argIdx)
	for _, c := range ratingConditions {
		sql += " AND " + c
	}

	sql += " ORDER BY mi.content_id ASC"
	return sql, args
}

// GetOriginalLanguage returns the original_language for a media item by content ID.
// Returns empty string if the item is not found or has no original language.
func (r *ItemRepository) GetOriginalLanguage(ctx context.Context, contentID string) (string, error) {
	var lang string
	err := r.pool.QueryRow(ctx,
		`SELECT original_language FROM media_items WHERE content_id = $1`,
		contentID,
	).Scan(&lang)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("fetching original language for %s: %w", contentID, err)
	}
	return lang, nil
}

// GetByExternalID finds a media item matching any of the given external IDs
// and the specified item type. Checks TMDB ID first, then IMDB, then TVDB.
// Returns ErrItemNotFound if no match is found.
func (r *ItemRepository) GetByExternalID(ctx context.Context, tmdbID, imdbID, tvdbID, itemType string) (*models.MediaItem, error) {
	for _, check := range []struct{ col, val string }{
		{"tmdb_id", tmdbID},
		{"imdb_id", imdbID},
		{"tvdb_id", tvdbID},
	} {
		if check.val == "" {
			continue
		}
		query := fmt.Sprintf("SELECT %s FROM media_items WHERE %s = $1", itemColumns, check.col)
		args := []any{check.val}
		if itemType != "" {
			query += " AND type = $2"
			args = append(args, itemType)
		}
		query += `
			ORDER BY
				CASE lower(trim(status))
					WHEN 'matched' THEN 0
					WHEN 'pending' THEN 1
					WHEN 'unmatched' THEN 2
					ELSE 3
				END,
				updated_at DESC,
				content_id ASC
			LIMIT 1`
		item, err := scanItem(r.pool.QueryRow(ctx, query, args...))
		if err == nil {
			return item, nil
		}
		if !errors.Is(err, ErrItemNotFound) {
			return nil, err
		}
	}
	return nil, ErrItemNotFound
}

// ExternalIDBatch holds the external IDs to look up in a single batched
// query. Each slice may be nil/empty independently; the caller does not need
// to pre-pad with placeholders.
type ExternalIDBatch struct {
	TMDBIDs []string
	IMDbIDs []string
	TVDBIDs []string
}

// ExternalIDLookup maps from each external ID to its content_id, allowing the
// caller to dedup and choose a priority order (e.g. TMDB > IMDb > TVDB).
type ExternalIDLookup struct {
	ByTMDB map[string]string
	ByIMDb map[string]string
	ByTVDB map[string]string
}

// GetByExternalIDs fetches media items matching any of the given external IDs
// of the specified type, in a single query. Replaces N×3 GetByExternalID
// calls in MDBList collection sync (audit 2026-05-01 §3.7).
func (r *ItemRepository) GetByExternalIDs(ctx context.Context, batch ExternalIDBatch, itemType string) (*ExternalIDLookup, error) {
	if len(batch.TMDBIDs) == 0 && len(batch.IMDbIDs) == 0 && len(batch.TVDBIDs) == 0 {
		return &ExternalIDLookup{
			ByTMDB: map[string]string{},
			ByIMDb: map[string]string{},
			ByTVDB: map[string]string{},
		}, nil
	}
	sql, args := r.buildGetByExternalIDsSQL(batch, itemType)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("fetching media items by external IDs: %w", err)
	}
	defer rows.Close()

	out := &ExternalIDLookup{
		ByTMDB: map[string]string{},
		ByIMDb: map[string]string{},
		ByTVDB: map[string]string{},
	}
	for rows.Next() {
		var contentID, tmdb, imdb, tvdb string
		if err := rows.Scan(&contentID, &tmdb, &imdb, &tvdb); err != nil {
			return nil, fmt.Errorf("scanning external-ID row: %w", err)
		}
		if tmdb != "" {
			out.ByTMDB[tmdb] = contentID
		}
		if imdb != "" {
			out.ByIMDb[imdb] = contentID
		}
		if tvdb != "" {
			out.ByTVDB[tvdb] = contentID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating external-ID rows: %w", err)
	}
	return out, nil
}

// buildGetByExternalIDsSQL pins the exact SQL shape used by GetByExternalIDs:
// a single statement that ORs across all three external-ID arrays plus a
// type filter. The COALESCE wraps make scanning into string safe even when
// the column is NULL (imdb_id/tmdb_id/tvdb_id are nullable text on
// media_items per migration 001).
func (r *ItemRepository) buildGetByExternalIDsSQL(batch ExternalIDBatch, itemType string) (string, []any) {
	sql := `SELECT content_id, COALESCE(tmdb_id, ''), COALESCE(imdb_id, ''), COALESCE(tvdb_id, '')
            FROM media_items
            WHERE (tmdb_id = ANY($1) OR imdb_id = ANY($2) OR tvdb_id = ANY($3))
              AND type = $4`
	return sql, []any{batch.TMDBIDs, batch.IMDbIDs, batch.TVDBIDs, itemType}
}

// GetByTitleYearType finds a media item by exact title, year, and type match.
// Used for dedup when external IDs are not available.
// Returns ErrItemNotFound if no match.
func (r *ItemRepository) GetByTitleYearType(ctx context.Context, title string, year int, itemType string) (*models.MediaItem, error) {
	query := `SELECT ` + itemColumns + ` FROM media_items WHERE title = $1 AND year = $2 AND type = $3 LIMIT 1`
	return scanItem(r.pool.QueryRow(ctx, query, title, year, itemType))
}

// Delete removes a media item by its content ID and returns S3 image
// directory paths that should be cleaned up by the caller.
func (r *ItemRepository) Delete(ctx context.Context, contentID string) ([]string, error) {
	// Collect image paths before deletion.
	imgRows, err := r.pool.Query(ctx, `
		SELECT poster_path, backdrop_path, logo_path FROM media_items WHERE content_id = $1
		UNION ALL
		SELECT poster_path, '', '' FROM seasons WHERE series_id = $1
		UNION ALL
		SELECT still_path, '', '' FROM episodes WHERE series_id = $1
	`, contentID)
	if err != nil {
		return nil, fmt.Errorf("collecting image paths before delete: %w", err)
	}
	var imageDirs []string
	for imgRows.Next() {
		var p1, p2, p3 string
		if err := imgRows.Scan(&p1, &p2, &p3); err != nil {
			imgRows.Close()
			return nil, fmt.Errorf("scanning image path: %w", err)
		}
		for _, p := range []string{p1, p2, p3} {
			if p != "" && !strings.Contains(p, "://") {
				if dir := pathDir(p); dir != "" {
					imageDirs = append(imageDirs, dir)
				}
			}
		}
	}
	imgRows.Close()

	tag, err := r.pool.Exec(ctx, "DELETE FROM media_items WHERE content_id = $1", contentID)
	if err != nil {
		return nil, fmt.Errorf("deleting media item: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return nil, ErrItemNotFound
	}

	return imageDirs, nil
}

// Search performs a full-text search on media items using the GIN tsvector
// index. It returns the matching items, total count of results, and any error.
//
// The data and total count are computed in a single round-trip via a
// COUNT(*) OVER () window function in the final SELECT off the scored CTE
// (audit 2026-05-01 §3.11). This replaces a prior two-query path that
// re-evaluated the tsvector predicates for the count.
func (r *ItemRepository) Search(ctx context.Context, query string, itemTypes []string, limit, offset int, filter AccessFilter) ([]*models.MediaItem, int, error) {
	if filter.AllowedLibraryIDs != nil && len(filter.AllowedLibraryIDs) == 0 {
		return []*models.MediaItem{}, 0, nil
	}
	sql, countSQL, args := r.buildSearchSQL(query, itemTypes, limit, offset, filter)
	if sql == "" {
		return []*models.MediaItem{}, 0, nil
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("searching media items: %w", err)
	}
	defer rows.Close()

	items, total, err := scanItemsWithTotal(rows)
	if err != nil {
		return nil, 0, err
	}
	// COUNT(*) OVER () emits no rows when the data SELECT is empty, so total
	// stays 0 even when the broader result set has matching rows (e.g. OFFSET
	// past the last page). Re-query the count to give callers the real total.
	// Skip when offset == 0 because in that case an empty page genuinely means
	// total = 0.
	if len(items) == 0 && offset > 0 {
		// Drop the trailing limit/offset args from the data query.
		countArgs := args[:len(args)-2]
		if err := r.pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count fallback for empty search page: %w", err)
		}
	}
	return items, total, nil
}

// buildSearchSQL assembles the unified search query, returning the SQL string
// and bound args (or empty string when the input parses to no searchable text).
//
// The query always uses a single `WITH scored AS (...)` CTE that aggregates
// per-content_id ranking signals. The final SELECT off the CTE selects
// itemColumns plus COUNT(*) OVER () AS total_count, ordered by relevance and
// paged via LIMIT/OFFSET. When the parsed text spans multiple words, a strict
// title-match path is enabled: a `stats` CTE is computed off `scored`, and the
// final SELECT cross-joins it with a WHERE clause that suppresses non-matching
// rows when at least one row has a strong title match. Because the window
// count runs in the final SELECT (after the cross-joined WHERE), the total
// reflects the post-filter count automatically.
//
// Argument order is intentionally fixed across both paths:
//
//	$1               searchText (always)
//	itemType placeholders, allowed/disabled libraries, MaxContentRating
//	parsed.ExactTitleHint
//	parsed.Year (or NULL)
//	parsed.Phrase
//	limit, offset
//
// The single ExactTitleHint binding is reused by both the strict-only
// contiguous_title_match LIKE filter inside the CTE and the exact_title_match
// equality predicate used for ranking.
func (r *ItemRepository) buildSearchSQL(query string, itemTypes []string, limit, offset int, filter AccessFilter) (dataSQL, countSQL string, args []any) {
	parsed := parseSearchQuery(query)
	searchText := parsed.Text
	if searchText == "" {
		searchText = collapseSearchWhitespace(strings.ReplaceAll(strings.TrimSpace(query), "\"", " "))
	}
	if searchText == "" {
		return "", "", nil
	}
	normalizedSearchText := normalizeTitleForComparison(searchText)
	strictTitleFilter := len(strings.Fields(normalizedSearchText)) > 1

	var conditions []string
	args = []any{searchText}
	argIdx := 2

	// All title-side text on both sides of @@ flows through
	// public.normalize_search_text() (migrations 127 / 138), which strips
	// non-alphanumeric chars, drops standalone "and" tokens, and normalizes
	// common title numbers. The expression must match
	// idx_media_items_search_title_fields exactly for the GIN index to be used.
	//
	// Overview uses the 'english' config which natively treats "and" as a
	// stop word, so it does not need explicit normalization.
	titleVector := `(
		setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.title, ''))), 'A') ||
		setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.original_title, ''))), 'A') ||
		setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.sort_title, ''))), 'B')
	)`
	overviewVector := `to_tsvector('english', COALESCE(mi.overview, ''))`
	normalizedTitleExpr := `public.normalize_search_text(%s)`
	titleQuery := `websearch_to_tsquery('simple', public.normalize_search_text($1))`
	titleMatch := fmt.Sprintf("%s @@ %s", titleVector, titleQuery)
	overviewMatch := fmt.Sprintf("%s @@ websearch_to_tsquery('english', $1)", overviewVector)

	// Keep the base match condition index-friendly; exact-title logic is used as
	// a ranking boost later, not as an additional scan predicate.
	conditions = append(conditions, fmt.Sprintf("(%s OR %s)", titleMatch, overviewMatch))

	if len(itemTypes) > 0 {
		placeholders := make([]string, 0, len(itemTypes))
		for _, itemType := range itemTypes {
			if strings.TrimSpace(itemType) == "" {
				continue
			}
			placeholders = append(placeholders, fmt.Sprintf("$%d", argIdx))
			args = append(args, strings.ToLower(strings.TrimSpace(itemType)))
			argIdx++
		}
		if len(placeholders) > 0 {
			conditions = append(conditions, fmt.Sprintf("mi.type IN (%s)", strings.Join(placeholders, ", ")))
		}
	}

	needsLibJoin := filter.AllowedLibraryIDs != nil || len(filter.DisabledLibraryIDs) > 0
	fromClause := "media_items mi"
	if filter.AllowedLibraryIDs != nil {
		// Caller (Search) is expected to short-circuit when len == 0; we still
		// guard here so the builder is safe to invoke from tests.
		if len(filter.AllowedLibraryIDs) > 0 {
			conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = ANY($%d)", argIdx))
			args = append(args, filter.AllowedLibraryIDs)
			argIdx++
		}
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("NOT (mil.media_folder_id = ANY($%d))", argIdx))
		args = append(args, filter.DisabledLibraryIDs)
		argIdx++
	}
	if needsLibJoin {
		fromClause = "media_items mi JOIN media_item_libraries mil ON mi.content_id = mil.content_id"
	}
	applyAccessFilter("mi", AccessFilter{MaxContentRating: filter.MaxContentRating}, &conditions, &args, &argIdx)

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	// Bind ExactTitleHint exactly once. The same arg index is referenced by
	// both contiguous_title_match (used as a ranking signal in all paths and
	// as the strict-title CROSS JOIN filter when strictTitleFilter is true)
	// and exact_title_match (used as a ranking signal in all paths).
	exactIdx := argIdx
	args = append(args, parsed.ExactTitleHint)
	argIdx++

	// mi.title uses the title_normalized stored generated column (migrations
	// 105 / 127), so the LIKE '%pattern%' arm hits the gin_trgm_ops index
	// instead of recomputing normalization per row. original_title and
	// sort_title do not have a trigram index of their own, so their LIKE
	// arms call public.normalize_search_text() per row. The tsvector @@
	// path covers all three via idx_media_items_search_title_fields.
	contiguousTitleMatch := fmt.Sprintf(`(
		$%d <> '' AND (
			%s LIKE '%%' || $%d || '%%' OR
			%s LIKE '%%' || $%d || '%%' OR
			%s LIKE '%%' || $%d || '%%'
		)
	)`,
		exactIdx,
		"mi.title_normalized", exactIdx,
		fmt.Sprintf(normalizedTitleExpr, "mi.original_title"), exactIdx,
		fmt.Sprintf(normalizedTitleExpr, "mi.sort_title"), exactIdx,
	)

	var yearArg any
	if parsed.Year != nil {
		yearArg = *parsed.Year
	}
	yearIdx := argIdx
	args = append(args, yearArg)
	argIdx++
	phraseIdx := argIdx
	args = append(args, parsed.Phrase)
	argIdx++

	exactTitleMatch := fmt.Sprintf(`(
		$%d <> '' AND (
			%s = $%d OR
			%s = $%d OR
			%s = $%d
		)
	)`,
		exactIdx,
		"mi.title_normalized", exactIdx,
		fmt.Sprintf(normalizedTitleExpr, "mi.original_title"), exactIdx,
		fmt.Sprintf(normalizedTitleExpr, "mi.sort_title"), exactIdx,
	)

	// Use qualified column names inside the CTE to avoid ambiguity when
	// the FROM clause includes a JOIN to media_item_libraries.
	qualifiedCols := qualifiedItemColumns("mi")
	scoredCTE := fmt.Sprintf(`
		WITH scored AS (
			SELECT
				%s,
				MAX(CASE
					WHEN %s THEN 1
					ELSE 0
				END) AS exact_title_match,
				MAX(CASE
					WHEN %s THEN 1
					ELSE 0
				END) AS contiguous_title_match,
				MAX(CASE
					WHEN $%d::int IS NOT NULL AND mi.year = $%d::int THEN 1
					ELSE 0
				END) AS year_match,
				MAX(ts_rank_cd(%s, %s)) AS title_rank,
				MAX(ts_rank_cd(%s, websearch_to_tsquery('english', $1))) AS overview_rank,
				MAX(CASE
					WHEN $%d <> '' THEN ts_rank_cd(%s, phraseto_tsquery('simple', public.normalize_search_text($%d)))
					ELSE 0
				END) AS phrase_rank
			FROM %s
			%s
			GROUP BY %s
		)
	`, qualifiedCols, exactTitleMatch, contiguousTitleMatch, yearIdx, yearIdx, titleVector, titleQuery, overviewVector, phraseIdx, titleVector, phraseIdx, fromClause, whereClause, qualifiedCols)

	// COUNT(*) OVER () runs after the GROUP BY in the scored CTE collapses
	// duplicates from the library JOIN, so the window count preserves the
	// COUNT(DISTINCT mi.content_id) semantics of the prior 2-query path.
	// In strict-title mode, placing the window count in the final SELECT
	// (after the cross-joined WHERE filter) means the total reflects the
	// post-filter row count automatically.
	//
	// countSQL is a count-only sibling that omits LIMIT/OFFSET/ORDER BY. It is
	// invoked only as a fallback when the data SELECT returns an empty page
	// past offset 0 — COUNT(*) OVER () emits no rows in that case so total
	// would otherwise default to 0.
	if strictTitleFilter {
		statsCTE := `
			, stats AS (
				SELECT MAX(contiguous_title_match) AS has_strong_title_match
				FROM scored
			)`
		dataSQL = scoredCTE + statsCTE + fmt.Sprintf(`
			SELECT %s, COUNT(*) OVER () AS total_count
			FROM scored
			CROSS JOIN stats
			WHERE COALESCE(stats.has_strong_title_match, 0) = 0 OR scored.contiguous_title_match = 1
			ORDER BY exact_title_match DESC, contiguous_title_match DESC, year_match DESC, phrase_rank DESC, title_rank DESC, overview_rank DESC, LOWER(title) ASC, content_id ASC
			LIMIT $%d OFFSET $%d`, itemColumns, argIdx, argIdx+1)
		countSQL = scoredCTE + statsCTE + `
			SELECT COUNT(*) FROM scored
			CROSS JOIN stats
			WHERE COALESCE(stats.has_strong_title_match, 0) = 0 OR scored.contiguous_title_match = 1`
	} else {
		dataSQL = scoredCTE + fmt.Sprintf(`
			SELECT %s, COUNT(*) OVER () AS total_count
			FROM scored
			ORDER BY exact_title_match DESC, contiguous_title_match DESC, year_match DESC, phrase_rank DESC, title_rank DESC, overview_rank DESC, LOWER(title) ASC, content_id ASC
			LIMIT $%d OFFSET $%d`, itemColumns, argIdx, argIdx+1)
		countSQL = scoredCTE + `SELECT COUNT(*) FROM scored`
	}
	args = append(args, limit, offset)
	return dataSQL, countSQL, args
}

// ListUnmatchedByFolderAndPathPrefix returns content IDs for unmatched-style
// items that are linked to at least one present file within the given folder
// subtree. This intentionally includes ambiguous items so a library scan can
// revisit legacy scanner ambiguities after inference heuristics improve.
func (r *ItemRepository) ListUnmatchedByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, limit int) ([]string, error) {
	query := `
		SELECT mi.content_id
		FROM media_items mi
		JOIN media_item_libraries mil
			ON mil.content_id = mi.content_id
		JOIN media_folders folders
			ON folders.id = mil.media_folder_id
		JOIN media_files mf
			ON mf.content_id = mi.content_id
		   AND mf.media_folder_id = mil.media_folder_id
		WHERE mil.media_folder_id = $1
		  AND folders.enabled = true
		  AND mi.status IN ('unmatched', 'pending', 'ambiguous')
		  AND mf.missing_since IS NULL
		  AND (mf.file_path = $2 OR mf.file_path LIKE $3 ESCAPE '\')
		GROUP BY mi.content_id
		ORDER BY MIN(mf.id) ASC, mi.content_id ASC`

	args := []any{folderID, pathPrefix, pathPrefixLike(pathPrefix)}
	if limit > 0 {
		query += ` LIMIT $4`
		args = append(args, limit)
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing unmatched items by folder and path prefix: %w", err)
	}
	defer rows.Close()

	var contentIDs []string
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning unmatched item content_id: %w", err)
		}
		contentIDs = append(contentIDs, contentID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating unmatched item rows: %w", err)
	}

	return contentIDs, nil
}

// EnsureAccessible returns ErrItemNotFound when the item falls outside the effective scope.
func (r *ItemRepository) EnsureAccessible(ctx context.Context, contentID string, filter AccessFilter) error {
	var conditions []string
	var args []any
	argIdx := 1

	fromClause := "media_items mi"
	conditions = append(conditions, fmt.Sprintf("mi.content_id = $%d", argIdx))
	args = append(args, contentID)
	argIdx++

	needsLibJoin := filter.AllowedLibraryIDs != nil || len(filter.DisabledLibraryIDs) > 0
	if filter.AllowedLibraryIDs != nil {
		if len(filter.AllowedLibraryIDs) == 0 {
			return ErrItemNotFound
		}
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = ANY($%d)", argIdx))
		args = append(args, filter.AllowedLibraryIDs)
		argIdx++
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("NOT (mil.media_folder_id = ANY($%d))", argIdx))
		args = append(args, filter.DisabledLibraryIDs)
		argIdx++
	}
	if needsLibJoin {
		fromClause = "media_items mi JOIN media_item_libraries mil ON mi.content_id = mil.content_id"
	}

	applyAccessFilter("mi", AccessFilter{MaxContentRating: filter.MaxContentRating}, &conditions, &args, &argIdx)

	query := fmt.Sprintf("SELECT 1 FROM %s WHERE %s LIMIT 1", fromClause, strings.Join(conditions, " AND "))
	var found int
	if err := r.pool.QueryRow(ctx, query, args...).Scan(&found); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrItemNotFound
		}
		return fmt.Errorf("checking item access: %w", err)
	}
	return nil
}

// GetItemsInLibrary returns a membership map for the provided content IDs within
// a single library. Episode content IDs are matched directly against
// episode_libraries so compat callers can filter mixed item/episode batches.
func (r *ItemRepository) GetItemsInLibrary(ctx context.Context, contentIDs []string, libraryID int) (map[string]bool, error) {
	result := make(map[string]bool, len(contentIDs))
	if len(contentIDs) == 0 {
		return result, nil
	}

	rows, err := r.pool.Query(ctx,
		`SELECT req.content_id
		FROM unnest($2::text[]) AS req(content_id)
		WHERE EXISTS (
			SELECT 1
			FROM media_item_libraries mil
			WHERE mil.media_folder_id = $1
			  AND mil.content_id = req.content_id
		)
		OR EXISTS (
			SELECT 1
			FROM episode_libraries el
			WHERE el.media_folder_id = $1
			  AND el.episode_id = req.content_id
		)`,
		libraryID, contentIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("getting library membership for items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning library membership row: %w", err)
		}
		result[contentID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating library membership rows: %w", err)
	}

	return result, nil
}

// ReplacePeople deletes all item_people rows for the given content_id and inserts new ones.
func (r *ItemRepository) ReplacePeople(ctx context.Context, contentID string, people []models.ItemPerson) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "DELETE FROM item_people WHERE content_id = $1", contentID); err != nil {
		return fmt.Errorf("delete existing people: %w", err)
	}

	if len(people) == 0 {
		return tx.Commit(ctx)
	}

	// Deduplicate by (person_id, kind, character) — PostgreSQL's ON CONFLICT
	// cannot handle the same row appearing twice in a single INSERT.
	type dedupKey struct {
		PersonID  int64
		Kind      models.PersonKind
		Character string
	}
	seen := make(map[dedupKey]struct{}, len(people))
	deduped := make([]models.ItemPerson, 0, len(people))
	for _, p := range people {
		key := dedupKey{p.Person.ID, p.Kind, p.Character}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, p)
	}

	var sb strings.Builder
	sb.WriteString("INSERT INTO item_people (id, content_id, person_id, kind, character, sort_order) VALUES ")
	args := make([]interface{}, 0, len(deduped)*6)
	for i, p := range deduped {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * 6
		fmt.Fprintf(&sb, "($%d, $%d, $%d, $%d, $%d, $%d)", base+1, base+2, base+3, base+4, base+5, base+6)

		rowIDStr, err := idgen.NextID()
		if err != nil {
			return fmt.Errorf("generate content-person id: %w", err)
		}
		rowID, _ := strconv.ParseInt(rowIDStr, 10, 64)
		args = append(args, rowID, contentID, p.Person.ID, p.Kind, p.Character, p.SortOrder)
	}
	sb.WriteString(" ON CONFLICT (content_id, person_id, kind, character) DO UPDATE SET sort_order = EXCLUDED.sort_order")

	if _, err := tx.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("insert people: %w", err)
	}

	return tx.Commit(ctx)
}

// GetPeople returns all people credited on a media item via the item_people + people JOIN.
func (r *ItemRepository) GetPeople(ctx context.Context, contentID string) ([]models.ItemPerson, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.name, p.sort_name, p.bio, p.birth_date, p.death_date, p.birthplace, p.homepage,
			p.photo_path, p.photo_thumbhash, p.tmdb_id, p.imdb_id, p.tvdb_id, p.plex_guid,
			p.created_at, p.updated_at,
			ip.kind, ip.character, ip.sort_order
		FROM item_people ip
		JOIN people p ON p.id = ip.person_id
		WHERE ip.content_id = $1
		ORDER BY ip.kind, ip.sort_order`, contentID,
	)
	if err != nil {
		return nil, fmt.Errorf("get people for item %s: %w", contentID, err)
	}
	defer rows.Close()

	return scanItemPeople(rows)
}

// UpdateMetadata builds a dynamic UPDATE query for the media_items table,
// setting only the non-nil fields in upd. Always bumps updated_at.
// Returns ErrItemNotFound if no row matches contentID.
func (r *ItemRepository) UpdateMetadata(ctx context.Context, contentID string, upd *MetadataUpdate) error {
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
	addStringArray := func(col string, val *[]string) {
		if val != nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, *val)
			argIdx++
		}
	}
	addIntArray := func(col string, val *[]int) {
		if val != nil {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, *val)
			argIdx++
		}
	}

	addString("title", upd.Title)
	addString("sort_title", upd.SortTitle)
	addString("original_title", upd.OriginalTitle)
	addString("overview", upd.Overview)
	addString("tagline", upd.Tagline)
	addString("content_rating", upd.ContentRating)
	addInt("year", upd.Year)
	addInt("runtime", upd.Runtime)
	addFloat("rating_imdb", upd.RatingIMDB)
	addFloat("rating_tmdb", upd.RatingTMDB)
	addInt("rating_rt_critic", upd.RatingRTCritic)
	addInt("rating_rt_audience", upd.RatingRTAudience)
	addStringArray("genres", upd.Genres)
	addStringArray("studios", upd.Studios)
	addStringArray("networks", upd.Networks)
	addStringArray("countries", upd.Countries)
	addString("release_date", upd.ReleaseDate)
	addString("first_air_date", upd.FirstAirDate)
	addString("last_air_date", upd.LastAirDate)
	addString("air_time", upd.AirTime)
	addString("status", upd.Status)
	addString("show_status", upd.ShowStatus)
	addString("imdb_id", upd.ImdbID)
	addString("tmdb_id", upd.TmdbID)
	addString("tvdb_id", upd.TvdbID)
	addIntArray("locked_fields", upd.LockedFields)
	addString("poster_path", upd.PosterPath)
	addString("poster_thumbhash", upd.PosterThumbhash)
	addString("backdrop_path", upd.BackdropPath)
	addString("backdrop_thumbhash", upd.BackdropThumbhash)
	addString("logo_path", upd.LogoPath)

	setClauses = append(setClauses, "updated_at = NOW()")

	query := fmt.Sprintf("UPDATE media_items SET %s WHERE content_id = $%d",
		strings.Join(setClauses, ", "), argIdx)
	args = append(args, contentID)

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating media item metadata: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

// IncrementRefreshFailure records a failed metadata refresh attempt for an
// existing media item.
func (r *ItemRepository) IncrementRefreshFailure(ctx context.Context, contentID string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_items
		SET refresh_failures = refresh_failures + 1
		WHERE content_id = $1`,
		contentID,
	)
	if err != nil {
		return fmt.Errorf("incrementing refresh failure: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

// MediaTMDBRow is a single result row from LookupTMDBIDs, containing the
// fields needed by the pluginhost CatalogPresence adapter.
type MediaTMDBRow struct {
	MediaID   string
	TMDBID    string
	LibraryID string
	Title     string
}

type ExternalIDLookupCandidate struct {
	TMDBID string
	TVDBID string
	IMDbID string
}

type ExternalIDMatchRow struct {
	QueryTMDBID     string
	MediaID         string
	MatchedProvider string
	LibraryID       string
	Title           string
}

// LookupTMDBIDs returns one row per matching media item that has its tmdb_id
// in the supplied list and is linked to at least one enabled library.
// mediaType is "movie" or "series" (silo's internal naming).
// When a media item is linked to multiple libraries the first matched row is
// returned (ordered by library id for determinism).
// The pluginhost.CatalogPresence adapter calls this to answer
// CheckMediaPresence requests from plugins.
func (r *ItemRepository) LookupTMDBIDs(ctx context.Context, mediaType string, tmdbIDs []string) ([]MediaTMDBRow, error) {
	if len(tmdbIDs) == 0 {
		return nil, nil
	}
	candidates := make([]ExternalIDLookupCandidate, 0, len(tmdbIDs))
	for _, id := range tmdbIDs {
		if strings.TrimSpace(id) != "" {
			candidates = append(candidates, ExternalIDLookupCandidate{TMDBID: id})
		}
	}
	rows, err := r.LookupExternalIDs(ctx, mediaType, candidates)
	if err != nil {
		return nil, err
	}
	out := make([]MediaTMDBRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, MediaTMDBRow{
			MediaID:   row.MediaID,
			TMDBID:    row.QueryTMDBID,
			LibraryID: row.LibraryID,
			Title:     row.Title,
		})
	}
	return out, nil
}

func lookupExternalIDsSQL() string {
	return `
		WITH requested(query_tmdb_id, provider, provider_id, ord) AS (
			SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::int[])
		),
		direct_matches AS (
			SELECT r.query_tmdb_id, mi.content_id, r.provider, mil.media_folder_id::text, mi.title, r.ord,
			       CASE r.provider WHEN 'tmdb' THEN 0 WHEN 'tvdb' THEN 1 WHEN 'imdb' THEN 2 ELSE 3 END AS provider_rank
			FROM requested r
			JOIN media_items mi
			  ON mi.type = $5
			 AND (
				(r.provider = 'tmdb' AND mi.tmdb_id <> '' AND mi.tmdb_id = r.provider_id)
				OR (r.provider = 'tvdb' AND mi.tvdb_id <> '' AND mi.tvdb_id = r.provider_id)
				OR (r.provider = 'imdb' AND mi.imdb_id <> '' AND mi.imdb_id = r.provider_id)
			 )
			JOIN media_item_libraries mil ON mil.content_id = mi.content_id
			JOIN media_folders mf ON mf.id = mil.media_folder_id
			WHERE mf.enabled = true
		),
		provider_matches AS (
			SELECT r.query_tmdb_id, mi.content_id, r.provider, mil.media_folder_id::text, mi.title, r.ord,
			       CASE r.provider WHEN 'tmdb' THEN 0 WHEN 'tvdb' THEN 1 WHEN 'imdb' THEN 2 ELSE 3 END AS provider_rank
			FROM requested r
			JOIN media_item_provider_ids mip
			  ON mip.provider = r.provider
			 AND mip.provider_id = r.provider_id
			 AND mip.item_type = $5
			JOIN media_items mi ON mi.content_id = mip.content_id AND mi.type = $5
			JOIN media_item_libraries mil ON mil.content_id = mi.content_id
			JOIN media_folders mf ON mf.id = mil.media_folder_id
			WHERE mf.enabled = true
		)
		SELECT DISTINCT ON (query_tmdb_id)
		       query_tmdb_id, content_id, provider, media_folder_id, title
		FROM (
			SELECT * FROM direct_matches
			UNION ALL
			SELECT * FROM provider_matches
		) matches
		ORDER BY query_tmdb_id, provider_rank ASC, ord ASC, content_id ASC, media_folder_id ASC`
}

func (r *ItemRepository) LookupExternalIDs(
	ctx context.Context,
	mediaType string,
	candidates []ExternalIDLookupCandidate,
) ([]ExternalIDMatchRow, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	queryTMDBIDs := make([]string, 0, len(candidates)*3)
	providers := make([]string, 0, len(candidates)*3)
	providerIDs := make([]string, 0, len(candidates)*3)
	ordinals := make([]int32, 0, len(candidates)*3)

	appendID := func(candidate ExternalIDLookupCandidate, provider, providerID string, ordinal int) {
		providerID = strings.TrimSpace(providerID)
		if providerID == "" {
			return
		}
		queryTMDBIDs = append(queryTMDBIDs, strings.TrimSpace(candidate.TMDBID))
		providers = append(providers, provider)
		providerIDs = append(providerIDs, providerID)
		ordinals = append(ordinals, int32(ordinal))
	}

	for i, candidate := range candidates {
		appendID(candidate, "tmdb", candidate.TMDBID, i)
		appendID(candidate, "tvdb", candidate.TVDBID, i)
		appendID(candidate, "imdb", candidate.IMDbID, i)
	}
	if len(providerIDs) == 0 {
		return nil, nil
	}

	rows, err := r.pool.Query(ctx, lookupExternalIDsSQL(), queryTMDBIDs, providers, providerIDs, ordinals, mediaType)
	if err != nil {
		return nil, fmt.Errorf("lookup external ids: %w", err)
	}
	defer rows.Close()

	out := make([]ExternalIDMatchRow, 0)
	for rows.Next() {
		var row ExternalIDMatchRow
		if err := rows.Scan(&row.QueryTMDBID, &row.MediaID, &row.MatchedProvider, &row.LibraryID, &row.Title); err != nil {
			return nil, fmt.Errorf("scanning external id lookup row: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating external id lookup rows: %w", err)
	}
	return out, nil
}

func pathPrefixLike(pathPrefix string) string {
	return pathscope.PrefixLike(pathPrefix)
}
