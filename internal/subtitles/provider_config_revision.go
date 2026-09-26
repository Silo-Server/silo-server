package subtitles

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidProviderConfigRevision       = errors.New("invalid provider configuration revision")
	ErrProviderConfigEncryptionUnavailable = errors.New("provider configuration encryption unavailable")
)

// VersionedProviderConfig is an internal canonical read, not a wire projection.
// Config retains its secret fields for provider construction only.
type VersionedProviderConfig struct {
	Config   ProviderConfig
	Revision int64
}

// ProviderConfigChange preserves blank credential fields unless ClearCredentials
// is explicit. Clearing ignores the other fields and disables the provider.
type ProviderConfigChange struct {
	Enabled          bool
	APIKey           string
	Username         string
	Password         string
	ClearCredentials bool
}

// ProviderConfigRevisionConflict never carries credentials. A zero current
// revision means that the row was absent when the conflict was inspected.
type ProviderConfigRevisionConflict struct{ CurrentRevision int64 }

func (*ProviderConfigRevisionConflict) Error() string { return "provider configuration changed" }

// ProviderConfigRevisionRepository is opt-in; legacy Repository callers remain
// compatible. The revision migration must precede use of these methods.
type ProviderConfigRevisionRepository interface {
	GetProviderConfigWithRevision(context.Context, string) (*VersionedProviderConfig, error)
	SaveProviderConfigWithRevision(context.Context, string, ProviderConfigChange, *int64) (int64, error)
}

func (r *PgRepository) GetProviderConfigWithRevision(ctx context.Context, provider string) (*VersionedProviderConfig, error) {
	if r.cipher == nil {
		return nil, ErrProviderConfigEncryptionUnavailable
	}
	var result VersionedProviderConfig
	cfg := &result.Config
	err := r.pool.QueryRow(ctx, `SELECT provider_name, enabled, api_key, username, password, updated_at, revision
 FROM subtitle_provider_config WHERE provider_name=$1`, provider).Scan(&cfg.ProviderName, &cfg.Enabled, &cfg.APIKey, &cfg.Username, &cfg.Password, &cfg.UpdatedAt, &result.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read provider configuration revision: %w", err)
	}
	if err = r.decryptProviderConfig(cfg); err != nil {
		return nil, err
	}
	return &result, nil
}

// SaveProviderConfigWithRevision guards the actual SQL write. Revision 0 means
// create-if-absent; positive means update that exact row; nil explicitly means
// update an existing row without a revision match, never create. Success returns
// the new durable revision. Errors may be uncertain and must not trigger retries
// or live-provider reload. This method does not validate provider credentials by
// contacting a provider and does not apply configuration to any running process.
func (r *PgRepository) SaveProviderConfigWithRevision(ctx context.Context, provider string, change ProviderConfigChange, expected *int64) (int64, error) {
	if r.cipher == nil {
		return 0, ErrProviderConfigEncryptionUnavailable
	}
	if provider == "" || (expected != nil && *expected < 0) {
		return 0, ErrInvalidProviderConfigRevision
	}
	if change.ClearCredentials {
		change = ProviderConfigChange{ClearCredentials: true}
	}
	apiKey, err := r.cipher.Encrypt(change.APIKey, r.providerSecretAAD("api_key", provider))
	if err != nil {
		return 0, fmt.Errorf("encrypt provider API key: %w", err)
	}
	password, err := r.cipher.Encrypt(change.Password, r.providerSecretAAD("password", provider))
	if err != nil {
		return 0, fmt.Errorf("encrypt provider password: %w", err)
	}
	var revision int64
	if expected != nil && *expected == 0 {
		err = r.pool.QueryRow(ctx, `INSERT INTO subtitle_provider_config(provider_name,enabled,api_key,username,password,updated_at)
  VALUES($1,$2,$3,$4,$5,NOW()) ON CONFLICT(provider_name) DO NOTHING RETURNING revision`, provider, change.Enabled, apiKey, change.Username, password).Scan(&revision)
	} else {
		err = r.pool.QueryRow(ctx, `UPDATE subtitle_provider_config SET enabled=$2,
  api_key=CASE WHEN $6 THEN '' WHEN $3='' THEN api_key ELSE $3 END,
  username=CASE WHEN $6 THEN '' WHEN $4='' THEN username ELSE $4 END,
  password=CASE WHEN $6 THEN '' WHEN $5='' THEN password ELSE $5 END,
  updated_at=NOW()
  WHERE provider_name=$1 AND ($7::bigint IS NULL OR revision=$7)
  RETURNING revision`, provider, change.Enabled, apiKey, change.Username, password, change.ClearCredentials, expected).Scan(&revision)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		var current int64
		lookupErr := r.pool.QueryRow(ctx, `SELECT revision FROM subtitle_provider_config WHERE provider_name=$1`, provider).Scan(&current)
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return 0, fmt.Errorf("read provider conflict revision: %w", lookupErr)
		}
		return 0, &ProviderConfigRevisionConflict{CurrentRevision: current}
	}
	if err != nil {
		return 0, fmt.Errorf("save provider configuration revision: %w", err)
	}
	return revision, nil
}
