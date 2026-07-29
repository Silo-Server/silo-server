// internal/subtitles/pgrepo.go
package subtitles

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// PgRepository implements Repository using PostgreSQL.
type PgRepository struct {
	pool   *pgxpool.Pool
	cipher *secret.Cipher
}

// NewPgRepository creates a new PostgreSQL repository.
func NewPgRepository(pool *pgxpool.Pool, cipher *secret.Cipher) *PgRepository {
	return &PgRepository{pool: pool, cipher: cipher}
}

// providerSecretAAD binds a subtitle_provider_config secret column to its row
// (keyed by provider_name).
func (r *PgRepository) providerSecretAAD(column, providerName string) string {
	return secret.RowAAD("subtitle_provider_config", column, providerName)
}

// decryptProviderConfig applies the read-path contract to the api_key and
// password columns (username is not a secret) and (re)derives the Has* flags
// from the decrypted values.
func (r *PgRepository) decryptProviderConfig(cfg *ProviderConfig) error {
	apiKey, err := r.cipher.DecryptIfEncrypted(cfg.APIKey, r.providerSecretAAD("api_key", cfg.ProviderName))
	if err != nil {
		return fmt.Errorf("decrypt subtitle api key for %s: %w", cfg.ProviderName, err)
	}
	cfg.APIKey = apiKey
	password, err := r.cipher.DecryptIfEncrypted(cfg.Password, r.providerSecretAAD("password", cfg.ProviderName))
	if err != nil {
		return fmt.Errorf("decrypt subtitle password for %s: %w", cfg.ProviderName, err)
	}
	cfg.Password = password
	cfg.HasAPIKey = cfg.APIKey != ""
	cfg.HasCredentials = cfg.Username != "" && cfg.Password != ""
	return nil
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
		if err := r.decryptProviderConfig(&cfg); err != nil {
			return nil, err
		}
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
	if err := r.decryptProviderConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (r *PgRepository) UpsertProviderConfig(ctx context.Context, cfg *ProviderConfig) error {
	// Encrypt the secret columns; an empty result preserves the keep-existing
	// CASE (toggling enabled with blank creds leaves stored creds intact).
	apiKey, err := r.cipher.Encrypt(cfg.APIKey, r.providerSecretAAD("api_key", cfg.ProviderName))
	if err != nil {
		return fmt.Errorf("encrypt subtitle api key: %w", err)
	}
	password, err := r.cipher.Encrypt(cfg.Password, r.providerSecretAAD("password", cfg.ProviderName))
	if err != nil {
		return fmt.Errorf("encrypt subtitle password: %w", err)
	}
	// Only overwrite credentials when non-empty, so toggling enabled doesn't wipe them.
	_, err = r.pool.Exec(ctx,
		`INSERT INTO subtitle_provider_config (provider_name, enabled, api_key, username, password, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (provider_name) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			api_key = CASE WHEN EXCLUDED.api_key = '' THEN subtitle_provider_config.api_key ELSE EXCLUDED.api_key END,
			username = CASE WHEN EXCLUDED.username = '' THEN subtitle_provider_config.username ELSE EXCLUDED.username END,
			password = CASE WHEN EXCLUDED.password = '' THEN subtitle_provider_config.password ELSE EXCLUDED.password END,
			updated_at = NOW()`,
		cfg.ProviderName, cfg.Enabled, apiKey, cfg.Username, password)
	if err != nil {
		return fmt.Errorf("upsert provider config: %w", err)
	}
	return nil
}

// ClearProviderCredentials atomically disables a provider and removes every
// stored credential. The normal upsert deliberately treats blank values as
// "keep existing", so deletion must use this explicit path.
func (r *PgRepository) ClearProviderCredentials(ctx context.Context, providerName string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO subtitle_provider_config (provider_name, enabled, api_key, username, password, updated_at)
		VALUES ($1, false, '', '', '', NOW())
		ON CONFLICT (provider_name) DO UPDATE SET
			enabled = false,
			api_key = '',
			username = '',
			password = '',
			updated_at = NOW()
	`, providerName)
	if err != nil {
		return fmt.Errorf("clear subtitle provider credentials: %w", err)
	}
	return nil
}
