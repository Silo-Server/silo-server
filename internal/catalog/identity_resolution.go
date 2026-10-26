package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	MaxIdentitySeeds     = 48
	identityProviderTMDB = "tmdb"
	identityProviderIMDB = "imdb"
	identityProviderTVDB = "tvdb"
)

type IdentitySeed struct {
	Type   string
	TmdbID string
	ImdbID string
	TvdbID string
}
type IdentityPosition struct {
	Seed      int    `json:"seed"`
	ContentID string `json:"content_id"`
}
type IdentityRequest struct {
	Seeds []IdentitySeed
	Limit int
	After IdentityPosition
}
type IdentityMatch struct {
	Seed      int
	ContentID string
}
type IdentityResult struct {
	Matches []IdentityMatch
	HasMore bool
	Next    IdentityPosition
}

func (s IdentitySeed) Valid() bool {
	return (s.Type == playableTypeMovie || s.Type == playableTypeSeries) && (strings.TrimSpace(s.TmdbID) != "" || strings.TrimSpace(s.ImdbID) != "" || strings.TrimSpace(s.TvdbID) != "")
}

// ResolveIdentities pages candidates, not just positives: conflicting IDs cannot
// turn a small request into an unbounded scan through duplicate provider IDs.
func (r *ItemRepository) ResolveIdentities(ctx context.Context, req IdentityRequest, access AccessFilter) (IdentityResult, error) {
	out := IdentityResult{Matches: []IdentityMatch{}}
	if len(req.Seeds) == 0 || len(req.Seeds) > MaxIdentitySeeds || req.Limit < 1 || req.Limit > 100 {
		return out, fmt.Errorf("%w: identity batch bounds", ErrInvalidCatalogRequest)
	}
	types, tmdb, imdb, tvdb := []string{}, []string{}, []string{}, []string{}
	for _, s := range req.Seeds {
		if !s.Valid() {
			return out, fmt.Errorf("%w: identity seed", ErrInvalidCatalogRequest)
		}
		types = append(types, s.Type)
		tmdb = append(tmdb, NormalizeTMDBID(s.TmdbID))
		imdb = append(imdb, strings.TrimSpace(s.ImdbID))
		tvdb = append(tvdb, strings.TrimSpace(s.TvdbID))
	}
	if access.AllowedLibraryIDs != nil && len(access.AllowedLibraryIDs) == 0 {
		return out, nil
	}
	args := []any{types, tmdb, imdb, tvdb, req.After.Seed, req.After.ContentID, req.Limit + 1}
	argIdx := 8
	conditions := itemAccessConditions(access, &args, &argIdx)
	conditions = append(conditions, `mi.type = s.media_type`, `EXISTS (SELECT 1 FROM media_item_libraries mil JOIN media_folders mf ON mf.id=mil.media_folder_id WHERE mil.content_id=mi.content_id AND mf.enabled)`)
	conditions = append(conditions, `(s.seed,mi.content_id)>($5::int,$6::text)`)
	where := strings.Join(conditions, " AND ")
	branches := make([]string, 0, 4)
	for _, provider := range []string{identityProviderTMDB, identityProviderIMDB, identityProviderTVDB} {
		branches = append(branches, `(SELECT mi.content_id FROM media_items mi WHERE s.`+provider+`<>'' AND mi.`+provider+`_id<>'' AND mi.`+provider+`_id=s.`+provider+` AND `+where+` ORDER BY mi.content_id LIMIT $7)`)
	}
	branches = append(branches, `(SELECT mi.content_id FROM media_item_provider_ids p JOIN media_items mi ON mi.content_id=p.content_id WHERE p.item_type=s.media_type AND ((p.provider='tmdb' AND NULLIF(BTRIM(mi.tmdb_id),'') IS NULL AND s.tmdb<>'' AND p.provider_id=s.tmdb) OR (p.provider='imdb' AND NULLIF(BTRIM(mi.imdb_id),'') IS NULL AND s.imdb<>'' AND p.provider_id=s.imdb) OR (p.provider='tvdb' AND NULLIF(BTRIM(mi.tvdb_id),'') IS NULL AND s.tvdb<>'' AND p.provider_id=s.tvdb)) AND `+where+` ORDER BY mi.content_id LIMIT $7)`)
	sql := `WITH seeds AS (
 SELECT media_type, tmdb, imdb, tvdb, (ordinality-1)::int AS seed
 FROM unnest($1::text[],$2::text[],$3::text[],$4::text[]) WITH ORDINALITY AS s(media_type,tmdb,imdb,tvdb,ordinality)
 ) SELECT s.seed, c.content_id, c.tmdb, c.imdb, c.tvdb FROM seeds s
 CROSS JOIN LATERAL (
 SELECT mi.content_id, ` + effectiveIdentitySQL(identityProviderTMDB) + ` AS tmdb, ` + effectiveIdentitySQL(identityProviderIMDB) + ` AS imdb, ` + effectiveIdentitySQL(identityProviderTVDB) + ` AS tvdb
 FROM (` + strings.Join(branches, " UNION ") + `) candidates JOIN media_items mi USING(content_id)
 ORDER BY mi.content_id LIMIT $7
 ) c WHERE s.seed >= $5
 ORDER BY s.seed,c.content_id LIMIT $7`
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return out, fmt.Errorf("resolving catalog identities: %w", err)
	}
	defer rows.Close()
	processed := 0
	for rows.Next() {
		var seed int
		var id, t, i, v string
		if err := rows.Scan(&seed, &id, &t, &i, &v); err != nil {
			return out, err
		}
		if processed == req.Limit {
			out.HasMore = true
			break
		}
		processed++
		out.Next = IdentityPosition{Seed: seed, ContentID: id}

		if req.Seeds[seed].MatchesItem(&models.MediaItem{Type: types[seed], TmdbID: t, ImdbID: i, TvdbID: v}, nil) {
			out.Matches = append(out.Matches, IdentityMatch{Seed: seed, ContentID: id})
		}

	}
	return out, rows.Err()
}

// The item column is authoritative; an active provider row fills a missing
// namespace. Rejected historical aliases never establish a positive match.
func effectiveIdentitySQL(provider string) string {
	return `COALESCE(NULLIF(BTRIM(mi.` + provider + `_id),''),(SELECT provider_id FROM media_item_provider_ids p WHERE p.content_id=mi.content_id AND p.item_type=mi.type AND p.provider='` + provider + `' ORDER BY provider_id LIMIT 1),'')`
}

func (r *ItemRepository) GetIdentityProviderIDs(ctx context.Context, ids []string) (map[string][]*models.MediaItemProviderID, error) {
	return NewProviderIDRepository(r.pool).GetByContentIDs(ctx, ids)
}

// MatchesItem checks the hydrated identity again: a metadata rematch between
// resolution and card loading must not confirm a different title.
func (s IdentitySeed) MatchesItem(item *models.MediaItem, providers []*models.MediaItemProviderID) bool {
	if item == nil || item.Type != s.Type {
		return false
	}
	current := map[string]string{identityProviderTMDB: strings.TrimSpace(item.TmdbID), identityProviderIMDB: strings.TrimSpace(item.ImdbID), identityProviderTVDB: strings.TrimSpace(item.TvdbID)}
	for _, p := range providers {
		if p != nil && p.ItemType == item.Type && current[p.Provider] == "" {
			current[p.Provider] = strings.TrimSpace(p.ProviderID)
		}
	}
	equal := false
	for _, pair := range [][2]string{{NormalizeTMDBID(s.TmdbID), NormalizeTMDBID(current[identityProviderTMDB])}, {strings.TrimSpace(s.ImdbID), current[identityProviderIMDB]}, {strings.TrimSpace(s.TvdbID), current[identityProviderTVDB]}} {
		if pair[0] != "" && pair[1] != "" {
			if pair[0] != pair[1] {
				return false
			}
			equal = true
		}
	}
	return equal
}
