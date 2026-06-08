package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/watchsync"
	watchtrakt "github.com/Silo-Server/silo-server/internal/watchsync/providers/trakt"
)

type traktCollectionTokenResolver struct {
	pool     *pgxpool.Pool
	settings catalog.SettingsStore // encrypting decorator: decrypts trakt client_secret
	cipher   *secret.Cipher        // decrypts watch_provider_connections tokens read via raw SQL
	provider *watchtrakt.Provider
}

func (r *traktCollectionTokenResolver) ResolveTraktAccessToken(ctx context.Context, profileID string) (string, error) {
	if r == nil || r.pool == nil || r.settings == nil || r.provider == nil {
		return "", errors.New("trakt token resolver is not configured")
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return "", errors.New("profile id is required")
	}

	conn, err := r.loadConnection(ctx, profileID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(conn.AccessToken) == "" {
		return "", errors.New("trakt connection is missing an access token")
	}
	if conn.TokenExpiresAt == nil || conn.TokenExpiresAt.After(time.Now().UTC().Add(time.Minute)) {
		return conn.AccessToken, nil
	}
	if strings.TrimSpace(conn.RefreshToken) == "" {
		return "", errors.New("trakt connection is expired and missing a refresh token")
	}

	cfg, err := r.serverConfig(ctx)
	if err != nil {
		return "", err
	}
	tokens, err := r.provider.RefreshToken(ctx, cfg, conn)
	if err != nil {
		return "", fmt.Errorf("refresh trakt token: %w", err)
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return "", errors.New("trakt refresh returned an empty access token")
	}
	conn.AccessToken = tokens.AccessToken
	if strings.TrimSpace(tokens.RefreshToken) != "" {
		conn.RefreshToken = tokens.RefreshToken
	}
	if tokens.TokenExpiresAt != nil {
		conn.TokenExpiresAt = tokens.TokenExpiresAt
	}
	if err := r.updateTokens(ctx, conn); err != nil {
		return "", err
	}
	return conn.AccessToken, nil
}

func (r *traktCollectionTokenResolver) serverConfig(ctx context.Context) (watchsync.ServerConfig, error) {
	clientID, err := r.settings.Get(ctx, "watchsync.trakt.client_id")
	if err != nil {
		return watchsync.ServerConfig{}, err
	}
	clientSecret, err := r.settings.Get(ctx, "watchsync.trakt.client_secret")
	if err != nil {
		return watchsync.ServerConfig{}, err
	}
	cfg := watchsync.ServerConfig{ClientID: clientID, ClientSecret: clientSecret}
	if !cfg.Configured() {
		return watchsync.ServerConfig{}, errors.New("trakt credentials are not configured")
	}
	return cfg, nil
}

func (r *traktCollectionTokenResolver) loadConnection(ctx context.Context, profileID string) (watchsync.Connection, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT
			id::text, provider, user_id, profile_id, provider_account_id, provider_username,
			access_token, refresh_token, token_expires_at
		FROM watch_provider_connections
		WHERE provider = 'trakt'
		  AND profile_id = $1
		  AND access_token <> ''
		ORDER BY updated_at DESC
		LIMIT 1
	`, profileID)
	var conn watchsync.Connection
	err := row.Scan(
		&conn.ID,
		&conn.Provider,
		&conn.UserID,
		&conn.ProfileID,
		&conn.ProviderAccountID,
		&conn.ProviderUsername,
		&conn.AccessToken,
		&conn.RefreshToken,
		&conn.TokenExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return watchsync.Connection{}, errors.New("trakt connection not found for profile")
		}
		return watchsync.Connection{}, fmt.Errorf("load trakt connection: %w", err)
	}
	// This resolver reads watch_provider_connections directly (bypassing the
	// watchsync repo), so it must apply the same at-rest decryption the repo
	// does, bound to the same connection identity (watchsync.TokenAAD).
	if conn.AccessToken, err = r.cipher.DecryptIfEncrypted(conn.AccessToken, watchsync.TokenAAD("access_token", conn.Provider, conn.UserID, conn.ProfileID)); err != nil {
		return watchsync.Connection{}, fmt.Errorf("decrypt trakt access token: %w", err)
	}
	if conn.RefreshToken, err = r.cipher.DecryptIfEncrypted(conn.RefreshToken, watchsync.TokenAAD("refresh_token", conn.Provider, conn.UserID, conn.ProfileID)); err != nil {
		return watchsync.Connection{}, fmt.Errorf("decrypt trakt refresh token: %w", err)
	}
	return conn, nil
}

func (r *traktCollectionTokenResolver) updateTokens(ctx context.Context, conn watchsync.Connection) error {
	// Encrypt the refreshed tokens inline, bound to the connection identity, so
	// the raw-SQL write matches what the watchsync repo (and this resolver's read
	// path) store.
	accessToken, err := r.cipher.Encrypt(conn.AccessToken, watchsync.TokenAAD("access_token", conn.Provider, conn.UserID, conn.ProfileID))
	if err != nil {
		return fmt.Errorf("encrypt trakt access token: %w", err)
	}
	refreshToken, err := r.cipher.Encrypt(conn.RefreshToken, watchsync.TokenAAD("refresh_token", conn.Provider, conn.UserID, conn.ProfileID))
	if err != nil {
		return fmt.Errorf("encrypt trakt refresh token: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		UPDATE watch_provider_connections
		SET access_token = $2,
		    refresh_token = $3,
		    token_expires_at = $4,
		    updated_at = now()
		WHERE id = $1::uuid
	`, conn.ID, accessToken, refreshToken, conn.TokenExpiresAt)
	if err != nil {
		return fmt.Errorf("update refreshed trakt connection tokens: %w", err)
	}
	return nil
}
