package catalog

// personalCollectionBaseRelation is the source relation for one personal
// collection: every media_items row, plus one row for each stored season
// that user $1 has in collection $2. A season row is its parent series'
// media_items row with the season's identity, artwork and year laid over it
// by jsonb_populate_record, so both branches always share the media_items
// column list. access_content_id names the row whose library membership
// decides visibility: the item itself, or a season's series. A season keeps
// its series' title and sort title; collection_season_key (empty for catalog
// rows, the zero-padded season number for seasons) breaks the tie, so a title
// sort places seasons directly after their series, in season order.
const personalCollectionBaseRelation = `(
	SELECT catalog_item.*, catalog_item.content_id AS access_content_id, ''::text AS collection_season_key
	FROM media_items catalog_item
	UNION ALL
	SELECT season_row.*, series_item.content_id AS access_content_id, LPAD(season.season_number::text, 6, '0') AS collection_season_key
	FROM user_personal_collection_items season_member
	JOIN seasons season ON season.content_id = season_member.media_item_id
	JOIN media_items series_item ON series_item.content_id = season.series_id AND series_item.type = 'series'
	CROSS JOIN LATERAL jsonb_populate_record(series_item, jsonb_build_object(
		'content_id', season.content_id,
		'type', 'season',
		'overview', COALESCE(NULLIF(BTRIM(season.overview), ''), series_item.overview),
		'year', COALESCE(EXTRACT(YEAR FROM season.air_date)::integer, series_item.year),
		'release_date', season.air_date,
		'poster_path', COALESCE(NULLIF(season.poster_path, ''), series_item.poster_path),
		'poster_source_path', COALESCE(NULLIF(season.poster_source_path, ''), series_item.poster_source_path),
		'poster_thumbhash', COALESCE(NULLIF(season.poster_thumbhash, ''), series_item.poster_thumbhash),
		'season_count', NULL,
		'metadata_s3_path', season.metadata_s3_path,
		'metadata_etag', season.metadata_etag,
		'created_at', season.created_at,
		'updated_at', season.updated_at
	)) AS season_row
	WHERE season_member.user_id = $1
	  AND season_member.collection_id = $2
	  AND season_member.sub_item_id = ''
) mi`

// usePersonalCollectionSource points executor at the members of userID's
// collectionID, stored seasons included. It leaves the episode scope alone:
// that scope replaces the base relation with its own.
func usePersonalCollectionSource(executor *QueryExecutor, userID int, collectionID string) {
	executor.SourceWhere = personalMembershipSourceWhere
	executor.SourceArgs = []any{userID, collectionID}
	if isEpisodeCatalogScope(executor.Scope) {
		return
	}
	executor.BaseRelationSQL = personalCollectionBaseRelation
	executor.BaseRelationArgs = []any{userID, collectionID}
	executor.LibraryContentExpr = "mi.access_content_id"
	executor.SeasonsMatchSeriesType = true
	executor.SeasonOrderKeyExpr = "mi.collection_season_key"
}

// inheritPersonalCollectionSource gives dst the season-aware relation of src,
// for nested predicates (display filters) that must see the same rows.
func inheritPersonalCollectionSource(dst, src *QueryExecutor) {
	if src.BaseRelationSQL != personalCollectionBaseRelation || isEpisodeCatalogScope(dst.Scope) {
		return
	}
	dst.BaseRelationSQL = src.BaseRelationSQL
	dst.BaseRelationArgs = append([]any(nil), src.BaseRelationArgs...)
	dst.LibraryContentExpr = src.LibraryContentExpr
	dst.SeasonsMatchSeriesType = src.SeasonsMatchSeriesType
	dst.SeasonOrderKeyExpr = src.SeasonOrderKeyExpr
}
