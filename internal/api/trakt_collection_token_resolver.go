package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// watchProviderAccessTokens hands out a saved watch-provider connection's
// access token, refreshing it first when needed (watchsync.Service).
type watchProviderAccessTokens interface {
	AccessToken(ctx context.Context, connectionID string) (string, error)
}

// traktCollectionTokenResolver finds a profile's Trakt connection for
// recommendation collections and takes its token from watch sync, so a
// refresh started here is serialized with watch sync's own refreshes.
type traktCollectionTokenResolver struct {
	pool   *pgxpool.Pool
	tokens watchProviderAccessTokens
}

func (r *traktCollectionTokenResolver) ResolveTraktAccessToken(ctx context.Context, profileID string) (string, error) {
	if r == nil || r.pool == nil || r.tokens == nil {
		return "", errors.New("trakt token resolver is not configured")
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return "", errors.New("profile id is required")
	}

	var connectionID string
	err := r.pool.QueryRow(ctx, `
		SELECT id::text
		FROM watch_provider_connections
		WHERE provider = 'trakt'
		  AND profile_id = $1
		  AND access_token <> ''
		ORDER BY updated_at DESC
		LIMIT 1
	`, profileID).Scan(&connectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("trakt connection not found for profile")
	}
	if err != nil {
		return "", fmt.Errorf("load trakt connection: %w", err)
	}
	return r.tokens.AccessToken(ctx, connectionID)
}
