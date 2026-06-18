package catalog

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// The localization tables carry per-field provenance (provider | ai | manual)
// on their AI-writable fields (overview, tagline) so writes from different
// origins can never regress quality: manual beats provider beats ai. The
// precedence is enforced here, in single-statement upserts, so concurrent
// writers (a metadata refresh racing an AI translation job) cannot interleave
// a read-modify-write.

type MediaItemLocalizationRepository struct {
	pool *pgxpool.Pool
}

func NewMediaItemLocalizationRepository(pool *pgxpool.Pool) *MediaItemLocalizationRepository {
	return &MediaItemLocalizationRepository{pool: pool}
}

// Upsert writes a provider localization. Title/sort-title/artwork fields are
// taken wholesale (provider data is authoritative for them); overview and
// tagline respect provenance — a manual value is never overwritten, and an
// empty incoming value never blanks an existing one (so a provider with no
// translated overview does not erase an AI or manual translation).
func (r *MediaItemLocalizationRepository) Upsert(ctx context.Context, loc *models.MediaItemLocalization) error {
	if loc == nil || loc.ContentID == "" || loc.Language == "" {
		return fmt.Errorf("invalid media item localization")
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO media_item_localizations (
			content_id, language, title, sort_title, overview, tagline,
			poster_path, poster_source_path, poster_thumbhash,
			backdrop_path, backdrop_source_path, backdrop_thumbhash,
			logo_path, logo_source_path
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12, $13, $14
		)
		ON CONFLICT (content_id, language) DO UPDATE SET
			title = EXCLUDED.title,
			sort_title = EXCLUDED.sort_title,
			overview = CASE
				WHEN media_item_localizations.overview_source = 'manual' OR EXCLUDED.overview = ''
					THEN media_item_localizations.overview
				ELSE EXCLUDED.overview END,
			overview_source = CASE
				WHEN media_item_localizations.overview_source = 'manual' OR EXCLUDED.overview = ''
					THEN media_item_localizations.overview_source
				ELSE 'provider' END,
			tagline = CASE
				WHEN media_item_localizations.tagline_source = 'manual' OR EXCLUDED.tagline = ''
					THEN media_item_localizations.tagline
				ELSE EXCLUDED.tagline END,
			tagline_source = CASE
				WHEN media_item_localizations.tagline_source = 'manual' OR EXCLUDED.tagline = ''
					THEN media_item_localizations.tagline_source
				ELSE 'provider' END,
			poster_path = EXCLUDED.poster_path,
			poster_source_path = EXCLUDED.poster_source_path,
			poster_thumbhash = EXCLUDED.poster_thumbhash,
			backdrop_path = EXCLUDED.backdrop_path,
			backdrop_source_path = EXCLUDED.backdrop_source_path,
			backdrop_thumbhash = EXCLUDED.backdrop_thumbhash,
			logo_path = EXCLUDED.logo_path,
			logo_source_path = EXCLUDED.logo_source_path,
			updated_at = NOW()
	`, loc.ContentID, loc.Language, loc.Title, loc.SortTitle, loc.Overview, loc.Tagline,
		loc.PosterPath, loc.PosterSourcePath, loc.PosterThumbhash,
		loc.BackdropPath, loc.BackdropSourcePath, loc.BackdropThumbhash,
		loc.LogoPath, loc.LogoSourcePath)
	if err != nil {
		return fmt.Errorf("upserting media item localization: %w", err)
	}
	return nil
}

// UpsertAITranslation writes AI-translated overview/tagline values. A nil
// pointer leaves that field untouched. Per field, the write lands when the
// existing value is empty or already AI-sourced; force additionally overwrites
// provider values (the admin explicitly asked to re-translate). Manual values
// are never overwritten. Rows created here carry empty titles/artwork — the
// serving layer falls back to the base item for empty localized fields.
func (r *MediaItemLocalizationRepository) UpsertAITranslation(ctx context.Context, contentID, language string, overview, tagline *string, force bool) error {
	if contentID == "" || language == "" {
		return fmt.Errorf("invalid media item AI localization")
	}
	if overview == nil && tagline == nil {
		return nil
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO media_item_localizations (
			content_id, language, title, sort_title, overview, tagline,
			poster_path, poster_source_path, poster_thumbhash,
			backdrop_path, backdrop_source_path, backdrop_thumbhash,
			logo_path, logo_source_path,
			overview_source, tagline_source
		) VALUES (
			$1, $2, '', '', COALESCE($3, ''), COALESCE($4, ''),
			'', '', '', '', '', '', '', '',
			CASE WHEN $3::text IS NULL THEN 'provider' ELSE 'ai' END,
			CASE WHEN $4::text IS NULL THEN 'provider' ELSE 'ai' END
		)
		ON CONFLICT (content_id, language) DO UPDATE SET
			overview = CASE
				WHEN $3::text IS NULL OR media_item_localizations.overview_source = 'manual'
					THEN media_item_localizations.overview
				WHEN $5 OR media_item_localizations.overview_source = 'ai' OR media_item_localizations.overview = ''
					THEN EXCLUDED.overview
				ELSE media_item_localizations.overview END,
			overview_source = CASE
				WHEN $3::text IS NULL OR media_item_localizations.overview_source = 'manual'
					THEN media_item_localizations.overview_source
				WHEN $5 OR media_item_localizations.overview_source = 'ai' OR media_item_localizations.overview = ''
					THEN 'ai'
				ELSE media_item_localizations.overview_source END,
			tagline = CASE
				WHEN $4::text IS NULL OR media_item_localizations.tagline_source = 'manual'
					THEN media_item_localizations.tagline
				WHEN $5 OR media_item_localizations.tagline_source = 'ai' OR media_item_localizations.tagline = ''
					THEN EXCLUDED.tagline
				ELSE media_item_localizations.tagline END,
			tagline_source = CASE
				WHEN $4::text IS NULL OR media_item_localizations.tagline_source = 'manual'
					THEN media_item_localizations.tagline_source
				WHEN $5 OR media_item_localizations.tagline_source = 'ai' OR media_item_localizations.tagline = ''
					THEN 'ai'
				ELSE media_item_localizations.tagline_source END,
			updated_at = NOW()
	`, contentID, language, overview, tagline, force)
	if err != nil {
		return fmt.Errorf("upserting media item AI localization: %w", err)
	}
	return nil
}

func (r *MediaItemLocalizationRepository) Get(ctx context.Context, contentID, language string) (*models.MediaItemLocalization, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT content_id, language, title, sort_title, overview, tagline,
		       poster_path, poster_source_path, poster_thumbhash,
		       backdrop_path, backdrop_source_path, backdrop_thumbhash,
		       logo_path, logo_source_path,
		       overview_source, tagline_source,
		       created_at, updated_at
		FROM media_item_localizations
		WHERE content_id = $1 AND language = $2
	`, contentID, language)
	return scanMediaItemLocalization(row)
}

func (r *MediaItemLocalizationRepository) GetByContentIDs(ctx context.Context, contentIDs []string, language string) (map[string]*models.MediaItemLocalization, error) {
	result := make(map[string]*models.MediaItemLocalization, len(contentIDs))
	if len(contentIDs) == 0 || language == "" {
		return result, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, language, title, sort_title, overview, tagline,
		       poster_path, poster_source_path, poster_thumbhash,
		       backdrop_path, backdrop_source_path, backdrop_thumbhash,
		       logo_path, logo_source_path,
		       overview_source, tagline_source,
		       created_at, updated_at
		FROM media_item_localizations
		WHERE language = $1 AND content_id = ANY($2)
	`, language, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("querying media item localizations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		loc, scanErr := scanMediaItemLocalization(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result[loc.ContentID] = loc
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating media item localizations: %w", err)
	}
	return result, nil
}

func (r *MediaItemLocalizationRepository) UpdateArtworkIfSourceMatches(ctx context.Context, contentID, language, imageType, sourcePath, cachedPath, thumbhash string) (bool, error) {
	if r == nil || r.pool == nil {
		return false, nil
	}

	var query string
	var args []any
	switch imageType {
	case "poster":
		query = `
			UPDATE media_item_localizations
			SET poster_path = $4,
				poster_source_path = $3,
				poster_thumbhash = NULLIF($5, ''),
				updated_at = NOW()
			WHERE content_id = $1
			  AND language = $2
			  AND poster_source_path = $3`
		args = []any{contentID, language, sourcePath, cachedPath, thumbhash}
	case "backdrop":
		query = `
			UPDATE media_item_localizations
			SET backdrop_path = $4,
				backdrop_source_path = $3,
				backdrop_thumbhash = NULLIF($5, ''),
				updated_at = NOW()
			WHERE content_id = $1
			  AND language = $2
			  AND backdrop_source_path = $3`
		args = []any{contentID, language, sourcePath, cachedPath, thumbhash}
	case "logo":
		query = `
			UPDATE media_item_localizations
			SET logo_path = $4,
				logo_source_path = $3,
				updated_at = NOW()
			WHERE content_id = $1
			  AND language = $2
			  AND logo_source_path = $3`
		args = []any{contentID, language, sourcePath, cachedPath}
	default:
		return false, fmt.Errorf("unsupported localized media item artwork type %q", imageType)
	}

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("updating localized media item cached artwork: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

type SeasonLocalizationRepository struct {
	pool *pgxpool.Pool
}

func NewSeasonLocalizationRepository(pool *pgxpool.Pool) *SeasonLocalizationRepository {
	return &SeasonLocalizationRepository{pool: pool}
}

// Upsert writes a provider localization; overview respects provenance (see
// MediaItemLocalizationRepository.Upsert).
func (r *SeasonLocalizationRepository) Upsert(ctx context.Context, loc *models.SeasonLocalization) error {
	if loc == nil || loc.SeasonContentID == "" || loc.Language == "" {
		return fmt.Errorf("invalid season localization")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO season_localizations (
			season_content_id, language, title, overview, poster_path, poster_source_path, poster_thumbhash
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (season_content_id, language) DO UPDATE SET
			title = EXCLUDED.title,
			overview = CASE
				WHEN season_localizations.overview_source = 'manual' OR EXCLUDED.overview = ''
					THEN season_localizations.overview
				ELSE EXCLUDED.overview END,
			overview_source = CASE
				WHEN season_localizations.overview_source = 'manual' OR EXCLUDED.overview = ''
					THEN season_localizations.overview_source
				ELSE 'provider' END,
			poster_path = EXCLUDED.poster_path,
			poster_source_path = EXCLUDED.poster_source_path,
			poster_thumbhash = EXCLUDED.poster_thumbhash,
			updated_at = NOW()
	`, loc.SeasonContentID, loc.Language, loc.Title, loc.Overview, loc.PosterPath, loc.PosterSourcePath, loc.PosterThumbhash)
	if err != nil {
		return fmt.Errorf("upserting season localization: %w", err)
	}
	return nil
}

// UpsertAIOverview writes an AI-translated season overview (see
// MediaItemLocalizationRepository.UpsertAITranslation for the precedence).
func (r *SeasonLocalizationRepository) UpsertAIOverview(ctx context.Context, seasonContentID, language, overview string, force bool) error {
	if seasonContentID == "" || language == "" {
		return fmt.Errorf("invalid season AI localization")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO season_localizations (
			season_content_id, language, title, overview, poster_path, poster_source_path, poster_thumbhash, overview_source
		) VALUES ($1, $2, '', $3, '', '', '', 'ai')
		ON CONFLICT (season_content_id, language) DO UPDATE SET
			overview = CASE
				WHEN season_localizations.overview_source = 'manual'
					THEN season_localizations.overview
				WHEN $4 OR season_localizations.overview_source = 'ai' OR season_localizations.overview = ''
					THEN EXCLUDED.overview
				ELSE season_localizations.overview END,
			overview_source = CASE
				WHEN season_localizations.overview_source = 'manual'
					THEN season_localizations.overview_source
				WHEN $4 OR season_localizations.overview_source = 'ai' OR season_localizations.overview = ''
					THEN 'ai'
				ELSE season_localizations.overview_source END,
			updated_at = NOW()
	`, seasonContentID, language, overview, force)
	if err != nil {
		return fmt.Errorf("upserting season AI localization: %w", err)
	}
	return nil
}

func (r *SeasonLocalizationRepository) Get(ctx context.Context, seasonContentID, language string) (*models.SeasonLocalization, error) {
	row := r.pool.QueryRow(ctx, `
			SELECT season_content_id, language, title, overview, poster_path, poster_source_path, poster_thumbhash,
		       overview_source, created_at, updated_at
		FROM season_localizations
		WHERE season_content_id = $1 AND language = $2
	`, seasonContentID, language)
	return scanSeasonLocalization(row)
}

func (r *SeasonLocalizationRepository) GetBySeasonIDs(ctx context.Context, seasonIDs []string, language string) (map[string]*models.SeasonLocalization, error) {
	result := make(map[string]*models.SeasonLocalization, len(seasonIDs))
	if len(seasonIDs) == 0 || language == "" {
		return result, nil
	}
	rows, err := r.pool.Query(ctx, `
			SELECT season_content_id, language, title, overview, poster_path, poster_source_path, poster_thumbhash,
		       overview_source, created_at, updated_at
		FROM season_localizations
		WHERE language = $1 AND season_content_id = ANY($2)
	`, language, seasonIDs)
	if err != nil {
		return nil, fmt.Errorf("querying season localizations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		loc, scanErr := scanSeasonLocalization(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result[loc.SeasonContentID] = loc
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating season localizations: %w", err)
	}
	return result, nil
}

func (r *SeasonLocalizationRepository) UpdateArtworkIfSourceMatches(ctx context.Context, seasonContentID, language, sourcePath, cachedPath, thumbhash string) (bool, error) {
	if r == nil || r.pool == nil {
		return false, nil
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE season_localizations
		SET poster_path = $4,
			poster_source_path = $3,
			poster_thumbhash = NULLIF($5, ''),
			updated_at = NOW()
		WHERE season_content_id = $1
		  AND language = $2
		  AND poster_source_path = $3
	`, seasonContentID, language, sourcePath, cachedPath, thumbhash)
	if err != nil {
		return false, fmt.Errorf("updating localized season cached artwork: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

type EpisodeLocalizationRepository struct {
	pool *pgxpool.Pool
}

func NewEpisodeLocalizationRepository(pool *pgxpool.Pool) *EpisodeLocalizationRepository {
	return &EpisodeLocalizationRepository{pool: pool}
}

// Upsert writes a provider localization; overview respects provenance (see
// MediaItemLocalizationRepository.Upsert).
func (r *EpisodeLocalizationRepository) Upsert(ctx context.Context, loc *models.EpisodeLocalization) error {
	if loc == nil || loc.EpisodeContentID == "" || loc.Language == "" {
		return fmt.Errorf("invalid episode localization")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO episode_localizations (episode_content_id, language, title, overview)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (episode_content_id, language) DO UPDATE SET
			title = EXCLUDED.title,
			overview = CASE
				WHEN episode_localizations.overview_source = 'manual' OR EXCLUDED.overview = ''
					THEN episode_localizations.overview
				ELSE EXCLUDED.overview END,
			overview_source = CASE
				WHEN episode_localizations.overview_source = 'manual' OR EXCLUDED.overview = ''
					THEN episode_localizations.overview_source
				ELSE 'provider' END,
			updated_at = NOW()
	`, loc.EpisodeContentID, loc.Language, loc.Title, loc.Overview)
	if err != nil {
		return fmt.Errorf("upserting episode localization: %w", err)
	}
	return nil
}

// UpsertAIOverview writes an AI-translated episode overview (see
// MediaItemLocalizationRepository.UpsertAITranslation for the precedence).
func (r *EpisodeLocalizationRepository) UpsertAIOverview(ctx context.Context, episodeContentID, language, overview string, force bool) error {
	if episodeContentID == "" || language == "" {
		return fmt.Errorf("invalid episode AI localization")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO episode_localizations (episode_content_id, language, title, overview, overview_source)
		VALUES ($1, $2, '', $3, 'ai')
		ON CONFLICT (episode_content_id, language) DO UPDATE SET
			overview = CASE
				WHEN episode_localizations.overview_source = 'manual'
					THEN episode_localizations.overview
				WHEN $4 OR episode_localizations.overview_source = 'ai' OR episode_localizations.overview = ''
					THEN EXCLUDED.overview
				ELSE episode_localizations.overview END,
			overview_source = CASE
				WHEN episode_localizations.overview_source = 'manual'
					THEN episode_localizations.overview_source
				WHEN $4 OR episode_localizations.overview_source = 'ai' OR episode_localizations.overview = ''
					THEN 'ai'
				ELSE episode_localizations.overview_source END,
			updated_at = NOW()
	`, episodeContentID, language, overview, force)
	if err != nil {
		return fmt.Errorf("upserting episode AI localization: %w", err)
	}
	return nil
}

func (r *EpisodeLocalizationRepository) Get(ctx context.Context, episodeContentID, language string) (*models.EpisodeLocalization, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT episode_content_id, language, title, overview, overview_source, created_at, updated_at
		FROM episode_localizations
		WHERE episode_content_id = $1 AND language = $2
	`, episodeContentID, language)
	return scanEpisodeLocalization(row)
}

func (r *EpisodeLocalizationRepository) GetByEpisodeIDs(ctx context.Context, episodeIDs []string, language string) (map[string]*models.EpisodeLocalization, error) {
	result := make(map[string]*models.EpisodeLocalization, len(episodeIDs))
	if len(episodeIDs) == 0 || language == "" {
		return result, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT episode_content_id, language, title, overview, overview_source, created_at, updated_at
		FROM episode_localizations
		WHERE language = $1 AND episode_content_id = ANY($2)
	`, language, episodeIDs)
	if err != nil {
		return nil, fmt.Errorf("querying episode localizations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		loc, scanErr := scanEpisodeLocalization(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result[loc.EpisodeContentID] = loc
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating episode localizations: %w", err)
	}
	return result, nil
}

func scanMediaItemLocalization(row pgx.Row) (*models.MediaItemLocalization, error) {
	var loc models.MediaItemLocalization
	if err := row.Scan(
		&loc.ContentID,
		&loc.Language,
		&loc.Title,
		&loc.SortTitle,
		&loc.Overview,
		&loc.Tagline,
		&loc.PosterPath,
		&loc.PosterSourcePath,
		&loc.PosterThumbhash,
		&loc.BackdropPath,
		&loc.BackdropSourcePath,
		&loc.BackdropThumbhash,
		&loc.LogoPath,
		&loc.LogoSourcePath,
		&loc.OverviewSource,
		&loc.TaglineSource,
		&loc.CreatedAt,
		&loc.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning media item localization: %w", err)
	}
	return &loc, nil
}

func scanSeasonLocalization(row pgx.Row) (*models.SeasonLocalization, error) {
	var loc models.SeasonLocalization
	if err := row.Scan(
		&loc.SeasonContentID,
		&loc.Language,
		&loc.Title,
		&loc.Overview,
		&loc.PosterPath,
		&loc.PosterSourcePath,
		&loc.PosterThumbhash,
		&loc.OverviewSource,
		&loc.CreatedAt,
		&loc.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning season localization: %w", err)
	}
	return &loc, nil
}

func scanEpisodeLocalization(row pgx.Row) (*models.EpisodeLocalization, error) {
	var loc models.EpisodeLocalization
	if err := row.Scan(
		&loc.EpisodeContentID,
		&loc.Language,
		&loc.Title,
		&loc.Overview,
		&loc.OverviewSource,
		&loc.CreatedAt,
		&loc.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning episode localization: %w", err)
	}
	return &loc, nil
}
