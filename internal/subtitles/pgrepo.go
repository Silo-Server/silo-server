// internal/subtitles/pgrepo.go
package subtitles

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgRepository implements Repository using PostgreSQL.
type PgRepository struct {
	pool *pgxpool.Pool
}

// NewPgRepository creates a new PostgreSQL repository.
func NewPgRepository(pool *pgxpool.Pool) *PgRepository {
	return &PgRepository{pool: pool}
}

func (r *PgRepository) InsertDownloadedSubtitle(ctx context.Context, sub *DownloadedSubtitle) error {
	return r.pool.QueryRow(ctx,
		`INSERT INTO downloaded_subtitles
			(media_file_id, provider, language, format, release_name, s3_key, score, hearing_impaired, downloaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at`,
		sub.MediaFileID, sub.Provider, sub.Language, sub.Format,
		sub.ReleaseName, sub.S3Key, sub.Score, sub.HearingImpaired, sub.DownloadedBy,
	).Scan(&sub.ID, &sub.CreatedAt)
}

func (r *PgRepository) GetDownloadedSubtitle(ctx context.Context, id int) (*DownloadedSubtitle, error) {
	var sub DownloadedSubtitle
	err := r.pool.QueryRow(ctx,
		`SELECT id, media_file_id, provider, language, format, release_name,
			s3_key, score, hearing_impaired, downloaded_by, created_at
		FROM downloaded_subtitles WHERE id = $1`, id,
	).Scan(&sub.ID, &sub.MediaFileID, &sub.Provider, &sub.Language, &sub.Format,
		&sub.ReleaseName, &sub.S3Key, &sub.Score, &sub.HearingImpaired,
		&sub.DownloadedBy, &sub.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get downloaded subtitle: %w", err)
	}
	return &sub, nil
}

func (r *PgRepository) ListDownloadedSubtitles(ctx context.Context, mediaFileID int) ([]DownloadedSubtitle, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, media_file_id, provider, language, format, release_name,
			s3_key, score, hearing_impaired, downloaded_by, created_at
		FROM downloaded_subtitles WHERE media_file_id = $1
		ORDER BY created_at`, mediaFileID)
	if err != nil {
		return nil, fmt.Errorf("list downloaded subtitles: %w", err)
	}
	defer rows.Close()

	var subs []DownloadedSubtitle
	for rows.Next() {
		var sub DownloadedSubtitle
		if err := rows.Scan(&sub.ID, &sub.MediaFileID, &sub.Provider, &sub.Language,
			&sub.Format, &sub.ReleaseName, &sub.S3Key, &sub.Score,
			&sub.HearingImpaired, &sub.DownloadedBy, &sub.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan downloaded subtitle: %w", err)
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

func (r *PgRepository) UpdateDownloadedSubtitle(ctx context.Context, id int, update SubtitleMetadataUpdate) (*DownloadedSubtitle, error) {
	var sub DownloadedSubtitle
	err := r.pool.QueryRow(ctx,
		`UPDATE downloaded_subtitles
		SET language = $1, release_name = $2, hearing_impaired = $3, s3_key = $4
		WHERE id = $5
		RETURNING id, media_file_id, provider, language, format, release_name,
			s3_key, score, hearing_impaired, downloaded_by, created_at`,
		update.Language, update.ReleaseName, update.HearingImpaired, update.S3Key, id,
	).Scan(&sub.ID, &sub.MediaFileID, &sub.Provider, &sub.Language, &sub.Format,
		&sub.ReleaseName, &sub.S3Key, &sub.Score, &sub.HearingImpaired,
		&sub.DownloadedBy, &sub.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update downloaded subtitle: %w", err)
	}
	return &sub, nil
}

func (r *PgRepository) DeleteDownloadedSubtitle(ctx context.Context, id int) (*DownloadedSubtitle, error) {
	var sub DownloadedSubtitle
	err := r.pool.QueryRow(ctx,
		`DELETE FROM downloaded_subtitles WHERE id = $1
		RETURNING id, media_file_id, provider, language, format, release_name,
			s3_key, score, hearing_impaired, downloaded_by, created_at`, id,
	).Scan(&sub.ID, &sub.MediaFileID, &sub.Provider, &sub.Language, &sub.Format,
		&sub.ReleaseName, &sub.S3Key, &sub.Score, &sub.HearingImpaired,
		&sub.DownloadedBy, &sub.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("delete downloaded subtitle: %w", err)
	}
	return &sub, nil
}

func (r *PgRepository) GetDownloadedSubtitleByS3Key(ctx context.Context, s3Key string) (*DownloadedSubtitle, error) {
	var sub DownloadedSubtitle
	err := r.pool.QueryRow(ctx,
		`SELECT id, media_file_id, provider, language, format, release_name,
			s3_key, score, hearing_impaired, downloaded_by, created_at
		FROM downloaded_subtitles WHERE s3_key = $1`, s3Key,
	).Scan(&sub.ID, &sub.MediaFileID, &sub.Provider, &sub.Language, &sub.Format,
		&sub.ReleaseName, &sub.S3Key, &sub.Score, &sub.HearingImpaired,
		&sub.DownloadedBy, &sub.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subtitle by s3 key: %w", err)
	}
	return &sub, nil
}

func (r *PgRepository) ListProviderConfigs(ctx context.Context) ([]ProviderConfig, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT provider_name, enabled, api_key, username, password, updated_at
		FROM subtitle_provider_config ORDER BY provider_name`)
	if err != nil {
		return nil, fmt.Errorf("list provider configs: %w", err)
	}
	defer rows.Close()

	var configs []ProviderConfig
	for rows.Next() {
		var cfg ProviderConfig
		if err := rows.Scan(&cfg.ProviderName, &cfg.Enabled, &cfg.APIKey,
			&cfg.Username, &cfg.Password, &cfg.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan provider config: %w", err)
		}
		cfg.HasAPIKey = cfg.APIKey != ""
		cfg.HasCredentials = cfg.Username != "" && cfg.Password != ""
		configs = append(configs, cfg)
	}
	return configs, rows.Err()
}

func (r *PgRepository) GetProviderConfig(ctx context.Context, providerName string) (*ProviderConfig, error) {
	var cfg ProviderConfig
	err := r.pool.QueryRow(ctx,
		`SELECT provider_name, enabled, api_key, username, password, updated_at
		FROM subtitle_provider_config WHERE provider_name = $1`, providerName,
	).Scan(&cfg.ProviderName, &cfg.Enabled, &cfg.APIKey,
		&cfg.Username, &cfg.Password, &cfg.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get provider config: %w", err)
	}
	cfg.HasAPIKey = cfg.APIKey != ""
	cfg.HasCredentials = cfg.Username != "" && cfg.Password != ""
	return &cfg, nil
}

func (r *PgRepository) UpsertProviderConfig(ctx context.Context, cfg *ProviderConfig) error {
	// Only overwrite credentials when non-empty, so toggling enabled doesn't wipe them.
	_, err := r.pool.Exec(ctx,
		`INSERT INTO subtitle_provider_config (provider_name, enabled, api_key, username, password, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (provider_name) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			api_key = CASE WHEN EXCLUDED.api_key = '' THEN subtitle_provider_config.api_key ELSE EXCLUDED.api_key END,
			username = CASE WHEN EXCLUDED.username = '' THEN subtitle_provider_config.username ELSE EXCLUDED.username END,
			password = CASE WHEN EXCLUDED.password = '' THEN subtitle_provider_config.password ELSE EXCLUDED.password END,
			updated_at = NOW()`,
		cfg.ProviderName, cfg.Enabled, cfg.APIKey, cfg.Username, cfg.Password)
	if err != nil {
		return fmt.Errorf("upsert provider config: %w", err)
	}
	return nil
}
