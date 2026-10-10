package watchsync

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"
)

// UpdateConnectionTokens replaces credentials only while the original
// connection and sign-in still exist. A disconnect or reconnect wins over an
// in-flight refresh; unrelated settings and sync state are preserved.
func (r *PostgresRepository) UpdateConnectionTokens(ctx context.Context, expected, updated Connection) (Connection, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Connection{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := r.scanConnection(tx.QueryRow(ctx, `SELECT `+connectionColumns+` FROM watch_provider_connections WHERE id=$1::uuid FOR UPDATE`, expected.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrConnectionNotFound
	}
	if err != nil {
		return Connection{}, err
	}
	if !connectionCredentialsMatch(current, expected) {
		return Connection{}, ErrStaleConnection
	}
	current = connectionWithTokens(current, storedTokens(updated))
	accessToken, err := r.cipher.Encrypt(current.AccessToken, TokenAAD("access_token", current.Provider, current.UserID, current.ProfileID))
	if err != nil {
		return Connection{}, fmt.Errorf("encrypt refreshed watch access token: %w", err)
	}
	refreshToken, err := r.cipher.Encrypt(current.RefreshToken, TokenAAD("refresh_token", current.Provider, current.UserID, current.ProfileID))
	if err != nil {
		return Connection{}, fmt.Errorf("encrypt refreshed watch refresh token: %w", err)
	}
	pluginCredentials, err := r.encodePluginCredentials(current)
	if err != nil {
		return Connection{}, err
	}
	saved, err := r.scanConnection(tx.QueryRow(ctx, `
		UPDATE watch_provider_connections
		SET access_token=$2, refresh_token=$3, token_expires_at=$4,
		    plugin_credentials=$5, last_error=$6,
		    updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond')
		WHERE id=$1::uuid
		RETURNING `+connectionColumns,
		current.ID, accessToken, refreshToken, current.TokenExpiresAt, pluginCredentials, updated.LastError))
	if err != nil {
		return Connection{}, fmt.Errorf("update watch provider tokens: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Connection{}, err
	}
	return saved, nil
}

func connectionCredentialsMatch(current, expected Connection) bool {
	expiryMatches := current.TokenExpiresAt == nil && expected.TokenExpiresAt == nil ||
		current.TokenExpiresAt != nil && expected.TokenExpiresAt != nil && current.TokenExpiresAt.Equal(*expected.TokenExpiresAt)
	return current.Provider == expected.Provider && current.UserID == expected.UserID && current.ProfileID == expected.ProfileID &&
		current.ProviderAccountID == expected.ProviderAccountID && current.AccessToken == expected.AccessToken && current.RefreshToken == expected.RefreshToken &&
		current.TokenType == expected.TokenType && expiryMatches && slices.Equal(current.Scopes, expected.Scopes) && maps.Equal(current.SecretAttributes, expected.SecretAttributes)
}
