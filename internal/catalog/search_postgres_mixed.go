package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

// GetSearchItemsByIDsWithAccess hydrates mixed search hits in one round trip.
// Meilisearch IDs are only candidates: both branches reapply the effective
// access filter, and callers restore provider order after this method returns.
func (r *ItemRepository) GetSearchItemsByIDsWithAccess(
	ctx context.Context,
	contentIDs []string,
	filter AccessFilter,
) ([]*models.MediaItem, error) {
	if len(contentIDs) == 0 || (filter.AllowedLibraryIDs != nil && len(filter.AllowedLibraryIDs) == 0) {
		return []*models.MediaItem{}, nil
	}

	args := []any{contentIDs}
	argIdx := 2
	mediaConditions := []string{"hydrated_mi.content_id = ANY($1)"}
	appendLibraryAccessConditions("hydrated_mi.content_id", filter, &mediaConditions, &args, &argIdx)
	applyAccessFilter("hydrated_mi", AccessFilter{
		MaxContentRating:   filter.MaxContentRating,
		ExcludedMediaTypes: filter.ExcludedMediaTypes,
	}, &mediaConditions, &args, &argIdx)

	episodeConditions := []string{"mi.content_id = ANY($1)"}
	appendEpisodeLibrarySearchAccess(
		"mi.content_id",
		episodeParentSeriesIDExpr("mi.content_id"),
		filter,
		&episodeConditions,
		&args,
		&argIdx,
	)
	applyAccessFilter("mi", AccessFilter{
		MaxContentRating:   filter.MaxContentRating,
		ExcludedMediaTypes: filter.ExcludedMediaTypes,
	}, &episodeConditions, &args, &argIdx)

	query := fmt.Sprintf(`
		SELECT %s
		FROM media_items hydrated_mi
		WHERE %s
		UNION ALL
		SELECT %s
		FROM %s
		WHERE %s`,
		qualifiedItemColumns("hydrated_mi"), strings.Join(mediaConditions, " AND "),
		qualifiedItemColumns("mi"), episodeCatalogBaseRelation, strings.Join(episodeConditions, " AND "))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("hydrating mixed search items: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

const episodeSearchTitleExpr = `ece.title`

const episodeSearchTitleVector = `ece.search_title_vector`

const episodeSearchOverviewVector = `ece.search_overview_vector`

const mediaSearchTitleVector = `(
	setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.title, ''))), 'A') ||
	setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.original_title, ''))), 'A') ||
	setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.sort_title, ''))), 'B')
)`

const mediaSearchOverviewVector = `to_tsvector('english', COALESCE(mi.overview, ''))`

const mixedSearchOrder = `exact_title_match DESC, contiguous_title_match DESC, year_match DESC,
	phrase_rank DESC, title_prefix_rank DESC, overview_rank DESC,
	LOWER(title) ASC, content_id ASC`

// buildMixedSearchSQLFromParsed builds one ranked candidate set from the two
// physical catalog sources. The scored CTE deliberately carries only ranking
// fields; the wide MediaItem projection is hydrated after LIMIT/OFFSET so a
// broad match never sorts posters, arrays, or metadata blobs for every hit.
func (r *ItemRepository) buildMixedSearchSQLFromParsed(
	parsed parsedSearchQuery,
	itemTypes []string,
	limit, offset int,
	filter AccessFilter,
	includeTotal bool,
) (dataSQL, countSQL string, args []any) {
	searchText := searchTextFromParsed(parsed)
	if searchText == "" {
		return "", "", nil
	}

	mediaTypes, includeEpisodes := splitSearchItemTypes(itemTypes)
	includeMediaItems := len(itemTypes) == 0 || len(mediaTypes) > 0
	if !includeMediaItems && !includeEpisodes {
		return "", "", nil
	}

	args = []any{searchText, buildTitlePrefixTsQuery(searchText)}
	argIdx := 3
	var titleBranches []string
	var overviewBranches []string
	exactShortTitle := useExactShortTitleSearch(parsed)
	leadingShortTitle := useLeadingShortTitleSearch(parsed)
	narrowTitleLookup := exactShortTitle || leadingShortTitle
	aliasCandidateArm := `mi.content_id = ANY(COALESCE((
		SELECT array_agg(alias_scores.content_id) FROM alias_scores
	), '{}'::text[]))`

	mediaConditions := []string{}
	if includeMediaItems {
		if len(itemTypes) > 0 {
			mediaConditions = append(mediaConditions, fmt.Sprintf("mi.type = ANY($%d)", argIdx))
			args = append(args, mediaTypes)
			argIdx++
		}
		appendLibraryAccessConditions("mi.content_id", filter, &mediaConditions, &args, &argIdx)
		applyAccessFilter("mi", AccessFilter{
			MaxContentRating:   filter.MaxContentRating,
			ExcludedMediaTypes: filter.ExcludedMediaTypes,
		}, &mediaConditions, &args, &argIdx)
		mediaConditions = append(mediaConditions, MangaChapterExclusionWhere("mi"))
	}

	episodeConditions := []string{}
	if includeEpisodes {
		episodeConditions = append(episodeConditions,
			"si.type = 'series'",
		)
		appendEpisodeCatalogSearchAccess("ece", filter, &episodeConditions, &args, &argIdx)
		if filter.MaxContentRating != "" {
			allowedRatings := access.AllowedRatingsUpTo(filter.MaxContentRating)
			if len(allowedRatings) == 0 {
				episodeConditions = append(episodeConditions, "1 = 0")
			} else {
				episodeConditions = append(episodeConditions, fmt.Sprintf("ece.content_rating = ANY($%d)", argIdx))
				args = append(args, allowedRatings)
				argIdx++
			}
		}
		if len(filter.ExcludedMediaTypes) > 0 {
			episodeConditions = append(episodeConditions, fmt.Sprintf("NOT ('episode' = ANY($%d))", argIdx))
			args = append(args, filter.ExcludedMediaTypes)
			argIdx++
		}
	}

	exactIdx := argIdx
	args = append(args, parsed.ExactTitleHint)
	argIdx++
	titleLookupIdx := exactIdx
	if leadingShortTitle {
		titleLookupIdx = argIdx
		args = append(args, parsed.NormalizedText)
		argIdx++
	}
	var yearArg any
	if parsed.Year != nil {
		yearArg = *parsed.Year
	}
	yearIdx := argIdx
	args = append(args, yearArg)
	argIdx++
	phraseIdx := argIdx
	args = append(args, parsed.Phrase)
	argIdx++

	var mediaTitleConditions, mediaOverviewConditions []string
	if includeMediaItems {
		mediaMatch := searchTitleMatchCondition(mediaSearchTitleVector, aliasCandidateArm)
		if exactShortTitle {
			mediaMatch = fmt.Sprintf("(mi.title_normalized = $%d OR %s)", exactIdx, aliasCandidateArm)
		} else if leadingShortTitle {
			mediaMatch = fmt.Sprintf("(mi.title_normalized LIKE $%d || '%%' OR %s)", titleLookupIdx, aliasCandidateArm)
		}
		mediaTitleConditions = append([]string{mediaMatch}, mediaConditions...)
		if !narrowTitleLookup {
			mediaOverviewConditions = append([]string{searchOverviewMatchCondition(mediaSearchOverviewVector)}, mediaConditions...)
		}
	}
	var episodeTitleConditions, episodeOverviewConditions []string
	if includeEpisodes {
		episodeMatch := searchTitleMatchCondition(episodeSearchTitleVector)
		if exactShortTitle {
			episodeMatch = fmt.Sprintf("ece.search_title_normalized = $%d", exactIdx)
		} else if leadingShortTitle {
			episodeMatch = fmt.Sprintf("ece.search_title_normalized LIKE $%d || '%%'", titleLookupIdx)
		}
		episodeTitleConditions = append([]string{episodeMatch}, episodeConditions...)
		if !narrowTitleLookup {
			episodeOverviewConditions = append([]string{searchOverviewMatchCondition(episodeSearchOverviewVector)}, episodeConditions...)
		}
	}

	if includeMediaItems {
		mediaAliasArms := mixedSearchAliasArms{
			exactArm:      "COALESCE(search_alias.exact_title_match, 0) > 0",
			contiguousArm: "COALESCE(search_alias.contiguous_title_match, 0) > 0",
			prefixRank:    "COALESCE(search_alias.title_prefix_rank, 0)",
		}
		titleBranches = append(titleBranches, buildMixedSearchCandidateBranch(
			"mi.content_id", "mi.type", "mi.title", "mi.year",
			`(
				setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.title, ''))), 'A') ||
				setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.original_title, ''))), 'A') ||
				setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.sort_title, ''))), 'B')
			)`,
			`to_tsvector('english', COALESCE(mi.overview, ''))`,
			[]string{`mi.title_normalized`, `public.normalize_search_text(mi.original_title)`, `public.normalize_search_text(mi.sort_title)`},
			"media_items mi LEFT JOIN alias_scores search_alias ON search_alias.content_id = mi.content_id",
			mediaTitleConditions, exactIdx, yearIdx, phraseIdx,
			&mediaAliasArms, false, false,
		))
		if !narrowTitleLookup {
			overviewBranches = append(overviewBranches, buildMixedSearchCandidateBranch(
				"mi.content_id", "mi.type", "mi.title", "mi.year",
				mediaSearchTitleVector, mediaSearchOverviewVector,
				[]string{`mi.title_normalized`, `public.normalize_search_text(mi.original_title)`, `public.normalize_search_text(mi.sort_title)`},
				"media_items mi", mediaOverviewConditions, exactIdx, yearIdx, phraseIdx,
				nil, true, false,
			))
		}
	}
	if includeEpisodes {
		episodeNormalizedTitle := `ece.search_title_normalized`
		titleBranches = append(titleBranches, buildMixedSearchCandidateBranch(
			"ece.episode_id", "'episode'::text", episodeSearchTitleExpr,
			"ece.year",
			episodeSearchTitleVector, episodeSearchOverviewVector,
			[]string{episodeNormalizedTitle},
			"episode_catalog_entries ece JOIN media_items si ON si.content_id = ece.series_id",
			episodeTitleConditions, exactIdx, yearIdx, phraseIdx,
			nil, false, true,
		))
		if !narrowTitleLookup {
			overviewBranches = append(overviewBranches, buildMixedSearchCandidateBranch(
				"ece.episode_id", "'episode'::text", episodeSearchTitleExpr,
				"ece.year",
				episodeSearchTitleVector, episodeSearchOverviewVector,
				[]string{episodeNormalizedTitle},
				"episode_catalog_entries ece JOIN media_items si ON si.content_id = ece.series_id",
				episodeOverviewConditions, exactIdx, yearIdx, phraseIdx,
				nil, true, true,
			))
		}
	}

	innerCTEs := make([]string, 0, 2)
	if includeMediaItems {
		innerCTEs = append(innerCTEs, buildMixedSearchAliasScoresCTE(exactIdx, titleLookupIdx, exactShortTitle, leadingShortTitle))
	}
	titleScoredBody := strings.Join(titleBranches, "\nUNION ALL\n")
	var scoredBody string
	if len(overviewBranches) > 0 {
		// Overview is a true fallback: it is useful only when the complete
		// accessible catalog contains no title (or alias) hit. Keeping the two
		// candidate paths separate lets PostgreSQL gate the overview branch with
		// a one-time NOT EXISTS test. Broad overview terms can otherwise create
		// thousands of episode/library probes that are ranked and then discarded
		// whenever even one title match exists.
		innerCTEs = append(innerCTEs, "title_scored AS MATERIALIZED (\n"+titleScoredBody+"\n)")
		scoredBody = fmt.Sprintf(`SELECT * FROM title_scored
		UNION ALL
		SELECT *
		FROM (
			%s
		) overview_scored
		WHERE NOT EXISTS (SELECT 1 FROM title_scored)
		  AND overview_scored.overview_rank >= %g`, strings.Join(overviewBranches, "\nUNION ALL\n"), overviewMatchFloor)
	} else {
		scoredBody = titleScoredBody
	}
	if len(innerCTEs) > 0 {
		scoredBody = "WITH " + strings.Join(innerCTEs, ",\n") + "\n" + scoredBody
	}
	scoredCTE := "WITH scored AS (\n" + scoredBody + "\n)"
	postFilter := `FROM scored`
	if narrowTitleLookup {
		// Narrow title searches intentionally skip the overview branch. That
		// branch is normally what gives $1 (searchText) its PostgreSQL type;
		// without it, queries such as "Breaking Bad" reference $2 and later
		// placeholders but fail at parse time with SQLSTATE 42P18 because $1 is
		// untyped. This parameter-only guard is always true for a built search
		// (empty input returned above), types $1 explicitly, and is planned as a
		// one-time filter without widening either indexed title lookup.
		postFilter += ` WHERE $1::text IS NOT NULL`
	}

	pageTotalColumn := ""
	finalTotalColumn := ""
	if includeTotal {
		pageTotalColumn = ", COUNT(*) OVER () AS total_count"
		finalTotalColumn = ", page.total_count"
	}
	limitIdx, offsetIdx := argIdx, argIdx+1
	args = append(args, limit, offset)

	pageCTE := fmt.Sprintf(`, page AS (
		SELECT scored.*, ROW_NUMBER() OVER (ORDER BY %s) AS ordinal%s
		%s
		ORDER BY %s
		LIMIT $%d OFFSET $%d
	)`, mixedSearchOrder, pageTotalColumn, postFilter, mixedSearchOrder, limitIdx, offsetIdx)

	hydratedRelation := fmt.Sprintf(`LATERAL (
		SELECT %s
		FROM media_items hydrated_mi
		WHERE page.type <> 'episode'
		  AND hydrated_mi.content_id = page.content_id
		UNION ALL
		SELECT %s
		FROM %s
		WHERE page.type = 'episode'
		  AND mi.content_id = page.content_id
	) hydrated`, qualifiedItemColumns("hydrated_mi"), qualifiedItemColumns("mi"), episodeCatalogBaseRelation)

	dataSQL = scoredCTE + pageCTE + fmt.Sprintf(`
		SELECT %s%s
		FROM page
		JOIN %s ON true
		ORDER BY page.ordinal`, qualifiedItemColumns("hydrated"), finalTotalColumn, hydratedRelation)
	countSQL = scoredCTE + fmt.Sprintf("\nSELECT COUNT(*)\n%s", postFilter)
	return dataSQL, countSQL, args
}

func splitSearchItemTypes(itemTypes []string) (mediaTypes []string, includeEpisodes bool) {
	if len(itemTypes) == 0 {
		return nil, true
	}
	seen := make(map[string]struct{}, len(itemTypes))
	for _, itemType := range itemTypes {
		itemType = strings.ToLower(strings.TrimSpace(itemType))
		if itemType == "" {
			continue
		}
		if itemType == "episode" {
			includeEpisodes = true
			continue
		}
		if _, ok := seen[itemType]; ok {
			continue
		}
		seen[itemType] = struct{}{}
		mediaTypes = append(mediaTypes, itemType)
	}
	return mediaTypes, includeEpisodes
}

func searchTitleMatchCondition(titleVector string, extraArms ...string) string {
	prefixQuery := `to_tsquery('simple', $2)`
	arms := []string{
		fmt.Sprintf(`($2 <> '' AND (%s) @@ %s)`, titleVector, prefixQuery),
	}
	arms = append(arms, extraArms...)
	return "(" + strings.Join(arms, " OR ") + ")"
}

func searchOverviewMatchCondition(overviewVector string) string {
	return fmt.Sprintf(`(%s) @@ websearch_to_tsquery('english', $1)`, overviewVector)
}

// buildMixedSearchAliasScoresCTE performs one index-backed pass over aliases
// matching the exact, leading-prefix, or FTS-prefix path, then reuses those
// scores for candidate admission and ranking. Keeping this work uncorrelated is
// critical: the former rank subqueries could scan the complete covering alias
// index once per media candidate when PostgreSQL failed to parameterize them.
// On large alias catalogs that turned a selective search into millions of
// index-entry visits and pushed the plan over the JIT threshold.
func buildMixedSearchAliasScoresCTE(exactIdx, titleLookupIdx int, exactShortTitle, leadingShortTitle bool) string {
	aliasVector := `to_tsvector('simple', mia.normalized_title)`
	weightedAliasVector := `setweight(to_tsvector('simple', mia.normalized_title), 'A')`
	prefixQuery := `to_tsquery('simple', $2)`
	matchCondition := fmt.Sprintf("mia.normalized_title = $%d", exactIdx)
	if leadingShortTitle {
		matchCondition = fmt.Sprintf("mia.normalized_title LIKE $%d || '%%'", titleLookupIdx)
	} else if !exactShortTitle {
		matchCondition = fmt.Sprintf("$2 <> '' AND %s @@ %s", aliasVector, prefixQuery)
	}
	return fmt.Sprintf(`alias_scores AS MATERIALIZED (
		SELECT
			mia.content_id,
			MAX(CASE WHEN mia.normalized_title = $%d THEN 1 ELSE 0 END) AS exact_title_match,
			MAX(CASE WHEN mia.normalized_title LIKE '%%' || $%d || '%%' THEN 1 ELSE 0 END) AS contiguous_title_match,
			MAX(CASE WHEN $2 <> '' THEN ts_rank_cd(%s, %s) ELSE 0 END) AS title_prefix_rank
		FROM media_item_aliases mia
		WHERE %s
		GROUP BY mia.content_id
	)`,
		exactIdx, exactIdx,
		weightedAliasVector, prefixQuery,
		matchCondition,
	)
}

// mixedSearchAliasArms carries the media branch's provider-alias extensions to
// the per-branch ranking SELECT. Episodes have no aliases and pass nil.
type mixedSearchAliasArms struct {
	exactArm      string // OR'd into exact_title_match
	contiguousArm string // OR'd into contiguous_title_match
	prefixRank    string // GREATEST'd with title_prefix_rank
}

func buildMixedSearchCandidateBranch(
	contentIDExpr, typeExpr, titleExpr, yearExpr, titleVector, overviewVector string,
	exactTitleExprs []string,
	fromClause string,
	conditions []string,
	exactIdx, yearIdx, phraseIdx int,
	aliasArms *mixedSearchAliasArms,
	rankOverview bool,
	distinctRows bool,
) string {
	exactArms := make([]string, 0, len(exactTitleExprs)+1)
	contiguousArms := make([]string, 0, len(exactTitleExprs)+1)
	for _, expr := range exactTitleExprs {
		exactArms = append(exactArms, fmt.Sprintf("%s = $%d", expr, exactIdx))
		contiguousArms = append(contiguousArms, fmt.Sprintf("%s LIKE '%%' || $%d || '%%'", expr, exactIdx))
	}
	prefixQuery := `to_tsquery('simple', $2)`
	prefixRankExpr := fmt.Sprintf("ts_rank_cd(%s, %s)", titleVector, prefixQuery)
	if aliasArms != nil {
		// Alias exact/contiguous arms are hashed subplans in the CASE select
		// list (evaluated per grouped row, hash built once); the rank arms are
		// per-row correlated content_id lookups over the matched set only.
		exactArms = append(exactArms, aliasArms.exactArm)
		contiguousArms = append(contiguousArms, aliasArms.contiguousArm)
		prefixRankExpr = fmt.Sprintf("GREATEST(%s, %s)", prefixRankExpr, aliasArms.prefixRank)
	}
	overviewRankExpr := "0::real"
	if rankOverview {
		overviewRankExpr = fmt.Sprintf("ts_rank_cd(%s, websearch_to_tsquery('english', $1))", overviewVector)
	}
	selectModifier := ""
	if distinctRows {
		selectModifier = "DISTINCT "
	}
	return fmt.Sprintf(`
		SELECT %s
			%s AS content_id,
			%s AS type,
			%s AS title,
			CASE WHEN $%d <> '' AND (%s) THEN 1 ELSE 0 END AS exact_title_match,
			CASE WHEN $%d <> '' AND (%s) THEN 1 ELSE 0 END AS contiguous_title_match,
			CASE WHEN $%d::int IS NOT NULL AND (%s) = $%d::int THEN 1 ELSE 0 END AS year_match,
			CASE WHEN $2 <> '' THEN %s ELSE 0 END AS title_prefix_rank,
			%s AS overview_rank,
			CASE WHEN $%d <> '' THEN ts_rank_cd(%s, phraseto_tsquery('simple', public.normalize_search_text($%d))) ELSE 0 END AS phrase_rank
		FROM %s
		WHERE %s`,
		selectModifier, contentIDExpr, typeExpr, titleExpr,
		exactIdx, strings.Join(exactArms, " OR "),
		exactIdx, strings.Join(contiguousArms, " OR "),
		yearIdx, yearExpr, yearIdx,
		prefixRankExpr,
		overviewRankExpr,
		phraseIdx, titleVector, phraseIdx,
		fromClause, strings.Join(conditions, " AND "))
}

// appendEpisodeCatalogSearchAccess applies episode-library policy directly to
// the maintained episode_catalog_entries relation. Each row is already proof
// that an episode is available in one library, so the common unrestricted path
// needs no per-candidate lookup into episode_libraries. DISTINCT in the episode
// candidate branch collapses the uncommon episode linked to multiple folders.
func appendEpisodeCatalogSearchAccess(
	alias string,
	filter AccessFilter,
	conditions *[]string,
	args *[]any,
	argIdx *int,
) {
	if filter.AllowedLibraryIDs != nil {
		*conditions = append(*conditions, fmt.Sprintf("%s.media_folder_id = ANY($%d)", alias, *argIdx))
		*args = append(*args, filter.AllowedLibraryIDs)
		*argIdx++
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		*conditions = append(*conditions, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM episode_catalog_entries disabled_ece WHERE disabled_ece.episode_id = %s.episode_id AND disabled_ece.media_folder_id = ANY($%d))",
			alias, *argIdx))
		*args = append(*args, filter.DisabledLibraryIDs)
		*argIdx++
	}
	// The parent series can be library-restricted independently of its episode
	// files, so retain that policy check even though availability is denormalized.
	appendEpisodeParentLibraryAccess(alias+".series_id", filter, conditions, args, argIdx)
}

func appendEpisodeLibrarySearchAccess(
	episodeIDExpr string,
	seriesIDExpr string,
	filter AccessFilter,
	conditions *[]string,
	args *[]any,
	argIdx *int,
) {
	if filter.AllowedLibraryIDs != nil {
		*conditions = append(*conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM episode_libraries allowed_el WHERE allowed_el.episode_id = %s AND allowed_el.media_folder_id = ANY($%d))",
			episodeIDExpr, *argIdx))
		*args = append(*args, filter.AllowedLibraryIDs)
		*argIdx++
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		*conditions = append(*conditions, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM episode_libraries disabled_el WHERE disabled_el.episode_id = %s AND disabled_el.media_folder_id = ANY($%d))",
			episodeIDExpr, *argIdx))
		*args = append(*args, filter.DisabledLibraryIDs)
		*argIdx++
	}
	// File membership is not enough: a series linked to a disabled library is
	// hidden at detail/play, so search must hide those episodes too.
	appendEpisodeParentLibraryAccess(seriesIDExpr, filter, conditions, args, argIdx)
}
