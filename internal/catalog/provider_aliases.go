package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/contentid"
)

// resolvableAliasProviders are the providers ResolveProviderAliases matches.
var resolvableAliasProviders = []string{contentid.ProviderTMDB, contentid.ProviderIMDB, contentid.ProviderTVDB}

// ProviderAlias is one provider ID of a title the caller tracks outside the
// catalog (a watchlisted title the library may not have yet). Key groups the
// aliases of one title; the catalog never interprets it.
type ProviderAlias struct {
	Key        int64
	MediaType  string // "movie" or "series"
	Provider   string // "tmdb", "imdb" or "tvdb"
	ProviderID string
}

// resolveProviderAliasesSQL matches each alias against a catalog item's
// current provider columns, its media_item_provider_ids rows, and the values
// its providers rejected (stale_media_ids), so a library item that carries or
// once carried a since-deleted duplicate ID still matches. Each source is its
// own branch so every lookup uses its index. Only items of the alias's media
// type with a row in an enabled folder count.
func resolveProviderAliasesSQL() string {
	return `
		WITH requested(key, media_type, provider, provider_id) AS (
			SELECT * FROM unnest($1::bigint[], $2::text[], $3::text[], $4::text[])
		),
		matches(key, media_type, content_id) AS (
			SELECT r.key, r.media_type, mi.content_id
			FROM requested r
			JOIN media_items mi ON r.provider = 'tmdb' AND mi.tmdb_id <> '' AND mi.tmdb_id = r.provider_id
			UNION
			SELECT r.key, r.media_type, mi.content_id
			FROM requested r
			JOIN media_items mi ON r.provider = 'imdb' AND mi.imdb_id <> '' AND mi.imdb_id = r.provider_id
			UNION
			SELECT r.key, r.media_type, mi.content_id
			FROM requested r
			JOIN media_items mi ON r.provider = 'tvdb' AND mi.tvdb_id <> '' AND mi.tvdb_id = r.provider_id
			UNION
			SELECT r.key, r.media_type, mip.content_id
			FROM requested r
			JOIN media_item_provider_ids mip
			  ON mip.provider = r.provider
			 AND mip.provider_id = r.provider_id
			 AND mip.item_type = r.media_type
			UNION
			SELECT r.key, r.media_type, s.content_id
			FROM requested r
			JOIN stale_media_ids s ON s.provider = r.provider AND s.provider_id = r.provider_id
		)
		SELECT DISTINCT m.key, m.content_id
		FROM matches m
		JOIN media_items mi ON mi.content_id = m.content_id AND mi.type = m.media_type
		WHERE EXISTS (
			SELECT 1
			FROM media_item_libraries mil
			JOIN media_folders mf ON mf.id = mil.media_folder_id
			WHERE mil.content_id = mi.content_id
			  AND mf.enabled = true
		)
		ORDER BY m.key, m.content_id`
}

// ResolveProviderAliases returns, per alias key, every distinct catalog item
// any of the key's aliases matches (see resolveProviderAliasesSQL). Unlike
// LookupExternalIDs, which keeps the best match per candidate, it returns all
// of them: a caller must see that the library holds a title twice rather than
// silently pick one copy. Keys with no match are absent from the result, and
// each key's content IDs are sorted.
func (r *ItemRepository) ResolveProviderAliases(ctx context.Context, aliases []ProviderAlias) (map[int64][]string, error) {
	keys := make([]int64, 0, len(aliases))
	mediaTypes := make([]string, 0, len(aliases))
	providers := make([]string, 0, len(aliases))
	providerIDs := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		provider := strings.ToLower(strings.TrimSpace(alias.Provider))
		providerID := strings.TrimSpace(alias.ProviderID)
		if providerID == "" || !slices.Contains(resolvableAliasProviders, provider) {
			continue
		}
		keys = append(keys, alias.Key)
		mediaTypes = append(mediaTypes, alias.MediaType)
		providers = append(providers, provider)
		providerIDs = append(providerIDs, providerID)
	}
	out := make(map[int64][]string)
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, resolveProviderAliasesSQL(), keys, mediaTypes, providers, providerIDs)
	if err != nil {
		return nil, fmt.Errorf("resolving provider aliases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key int64
		var contentID string
		if err := rows.Scan(&key, &contentID); err != nil {
			return nil, fmt.Errorf("scanning provider alias match: %w", err)
		}
		out[key] = append(out[key], contentID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating provider alias matches: %w", err)
	}
	return out, nil
}

// ItemProviderAliases returns a movie or series item's media type and every
// provider ID it carries or once carried: its tmdb/imdb/tvdb columns, its
// media_item_provider_ids rows for those providers, and the values those
// providers rejected. Key is left zero. An item that does not exist, is not a
// movie or series, or carries no IDs has no aliases.
func (r *ItemRepository) ItemProviderAliases(ctx context.Context, contentID string) (string, []ProviderAlias, error) {
	rows, err := r.pool.Query(ctx, `
		WITH item AS (
			SELECT content_id, type, tmdb_id, imdb_id, tvdb_id
			FROM media_items
			WHERE content_id = $1 AND type IN ('movie', 'series')
		)
		SELECT item.type, v.provider, v.provider_id
		FROM item
		CROSS JOIN LATERAL (
			SELECT 'tmdb' AS provider, item.tmdb_id AS provider_id
			UNION SELECT 'imdb', item.imdb_id
			UNION SELECT 'tvdb', item.tvdb_id
			UNION SELECT mip.provider, mip.provider_id
			      FROM media_item_provider_ids mip
			      WHERE mip.content_id = item.content_id AND mip.provider IN ('tmdb', 'imdb', 'tvdb')
			UNION SELECT s.provider, s.provider_id
			      FROM stale_media_ids s
			      WHERE s.content_id = item.content_id AND s.provider IN ('tmdb', 'imdb', 'tvdb')
		) v
		WHERE coalesce(v.provider_id, '') <> ''
		ORDER BY v.provider, v.provider_id`, contentID)
	if err != nil {
		return "", nil, fmt.Errorf("reading item provider aliases: %w", err)
	}
	defer rows.Close()
	var mediaType string
	var aliases []ProviderAlias
	for rows.Next() {
		var alias ProviderAlias
		if err := rows.Scan(&alias.MediaType, &alias.Provider, &alias.ProviderID); err != nil {
			return "", nil, fmt.Errorf("scanning item provider alias: %w", err)
		}
		mediaType = alias.MediaType
		aliases = append(aliases, alias)
	}
	if err := rows.Err(); err != nil {
		return "", nil, fmt.Errorf("iterating item provider aliases: %w", err)
	}
	return mediaType, aliases, nil
}
