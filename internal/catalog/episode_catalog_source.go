package catalog

import (
	"fmt"
	"strings"
)

const episodeCatalogSelectBody = `(
	SELECT
		e.content_id,
		e.air_date AS episode_air_date,
		'episode'::text AS type,
		COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text) AS title,
		COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text) AS sort_title,
		LOWER(COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text)) AS sort_key,
		COALESCE(NULLIF(BTRIM(e.default_metadata_language), ''), COALESCE(si.default_metadata_language, '')) AS default_metadata_language,
		''::text AS original_title,
		COALESCE(si.year, EXTRACT(YEAR FROM e.air_date)::integer, 0) AS year,
		COALESCE(si.genres, '{}'::text[]) AS genres,
		COALESCE(si.content_rating, '') AS content_rating,
		COALESCE(NULLIF(e.runtime, 0), COALESCE(si.runtime, 0)) AS runtime,
		COALESCE(e.overview, '') AS overview,
		''::text AS tagline,
		e.rating_imdb,
		e.rating_tmdb,
		NULL::integer AS rating_rt_critic,
		NULL::integer AS rating_rt_audience,
		COALESCE(e.imdb_id, '') AS imdb_id,
		COALESCE(e.tmdb_id, '') AS tmdb_id,
		COALESCE(e.tvdb_id, '') AS tvdb_id,
		COALESCE(e.still_path, '') AS poster_path,
		COALESCE(e.still_thumbhash, '') AS poster_thumbhash,
		COALESCE(si.backdrop_path, '') AS backdrop_path,
		COALESCE(si.backdrop_thumbhash, '') AS backdrop_thumbhash,
		COALESCE(si.logo_path, '') AS logo_path,
		COALESCE(e.metadata_s3_path, '') AS metadata_s3_path,
		COALESCE(e.metadata_etag, '') AS metadata_etag,
		NULL::integer AS season_count,
		COALESCE(si.studios, '{}'::text[]) AS studios,
		COALESCE(si.networks, '{}'::text[]) AS networks,
		COALESCE(si.countries, '{}'::text[]) AS countries,
		COALESCE(si.keywords, '{}'::text[]) AS keywords,
		COALESCE(si.original_language, '') AS original_language,
		CASE WHEN e.air_date IS NULL THEN NULL ELSE e.air_date::text END AS release_date,
		NULL::text AS first_air_date,
		NULL::text AS last_air_date,
		e.air_date AS last_air_date_at,
		si.air_time,
		COALESCE(si.show_status, '') AS show_status,
		si.matched_at,
		si.last_refreshed,
		si.refresh_failures,
		si.episode_metadata_incomplete,
		si.episode_metadata_last_checked_at,
		COALESCE(si.locked_fields, '{}'::integer[]) AS locked_fields,
		COALESCE(NULLIF(BTRIM(si.status), ''), 'matched') AS status,
		e.created_at,
		e.updated_at
	FROM episodes e
	JOIN media_items si ON si.content_id = e.series_id
	WHERE %s
) mi`

const episodeCatalogActiveLibraryExists = `EXISTS (
		SELECT 1
		FROM episode_libraries el
		WHERE el.episode_id = e.content_id
	)`

var episodeCatalogBaseRelation = fmt.Sprintf(episodeCatalogSelectBody, episodeCatalogActiveLibraryExists)

func isEpisodeCatalogScope(scope string) bool {
	return scope == "episode"
}

func catalogBaseRelationForScope(scope string) string {
	if isEpisodeCatalogScope(scope) {
		return episodeCatalogBaseRelation
	}
	return "media_items mi"
}

func episodeCatalogBaseRelationForLibraries(
	allowedLibraryIDs []int,
	disabledLibraryIDs []int,
	argIdx int,
) (string, []any, bool) {
	if len(allowedLibraryIDs) == 0 && len(disabledLibraryIDs) == 0 {
		return episodeCatalogBaseRelation, nil, false
	}

	libraryConditions := []string{
		"el.episode_id = e.content_id",
	}
	args := make([]any, 0, len(allowedLibraryIDs)+len(disabledLibraryIDs))

	if len(allowedLibraryIDs) > 0 {
		libraryConditions = append(
			libraryConditions,
			fmt.Sprintf("el.media_folder_id = ANY($%d)", argIdx),
		)
		args = append(args, allowedLibraryIDs)
		argIdx++
	}

	if len(disabledLibraryIDs) > 0 {
		libraryConditions = append(
			libraryConditions,
			fmt.Sprintf("NOT (el.media_folder_id = ANY($%d))", argIdx),
		)
		args = append(args, disabledLibraryIDs)
		argIdx++
	}

	relation := fmt.Sprintf(
		episodeCatalogSelectBody,
		fmt.Sprintf(
			`EXISTS (
		SELECT 1
		FROM episode_libraries el
		WHERE %s
	)`,
			strings.Join(libraryConditions, "\n\t\t  AND "),
		),
	)
	return relation, args, true
}

func catalogLibraryContentExprForScope(scope, alias string) string {
	if isEpisodeCatalogScope(scope) {
		return alias + ".content_id"
	}
	return alias + ".content_id"
}

func catalogLibraryMembershipTableAndKeyForScope(scope string) (string, string) {
	if isEpisodeCatalogScope(scope) {
		return "episode_libraries", "episode_id"
	}
	return "media_item_libraries", "content_id"
}

func catalogMediaFileJoinConditionForScope(scope, mediaFileAlias, itemAlias string) string {
	if isEpisodeCatalogScope(scope) {
		return mediaFileAlias + ".episode_id = " + itemAlias + ".content_id"
	}
	return mediaFileAlias + ".content_id = " + itemAlias + ".content_id"
}

func catalogMediaFileGroupExprForScope(scope, mediaFileAlias string) string {
	if isEpisodeCatalogScope(scope) {
		return mediaFileAlias + ".episode_id"
	}
	return mediaFileAlias + ".content_id"
}
