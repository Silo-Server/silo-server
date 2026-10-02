package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Search selectivity varies by term and library. Cache the description while
// using an unnamed statement, so PostgreSQL plans each execution for its bound
// values instead of eventually choosing one generic plan for rare and broad
// terms alike.
func searchPlanArgs(args []any) []any {
	return append([]any{pgx.QueryExecModeCacheDescribe}, args...)
}

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
		MaturityLimits:     filter.MaturityLimits,
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
		MaturityLimits:     filter.MaturityLimits,
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

// episodeSearchParentIsSeries checks the parent row joined as si.
const episodeSearchParentIsSeries = `si.type = 'series'`

const mediaSearchTitleVector = `mi.search_title_vector`

const mediaSearchOverviewVector = `mi.search_overview_vector`

func mixedSearchOrder(prefix string) string {
	return fmt.Sprintf(`%[1]sexact_title_match DESC, %[1]scontiguous_title_match DESC, %[1]syear_match DESC,
	%[1]sphrase_rank DESC, %[1]stitle_prefix_rank DESC, %[1]soverview_rank DESC,
	LOWER(%[1]stitle) ASC, %[1]scontent_id ASC`, prefix)
}

// yearSuffixedTitleSQL matches a title that is the query followed by the
// item's own year. Providers disambiguate remakes and reboots that way, so
// "Castle (2009)" is the exact title for a "Castle" search.
func yearSuffixedTitleSQL(normalizedTitle, year string, exactIdx int) string {
	return fmt.Sprintf("(%[2]s > 0 AND %[1]s = $%[3]d || ' ' || %[2]s::text)", normalizedTitle, year, exactIdx)
}

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
	return r.buildMixedSearchCursorSQL(parsed, itemTypes, limit, offset, filter, includeTotal, nil)
}
func (r *ItemRepository) buildMixedSearchCursorSQL(parsed parsedSearchQuery, itemTypes []string, limit, offset int, filter AccessFilter, includeTotal bool, cursor *searchCursorSQL) (dataSQL, countSQL string, args []any) {
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
	lookup := searchTitleLookup{exactShort: useExactShortTitleSearch(parsed), leadingShort: useLeadingShortTitleSearch(parsed)}
	narrowTitleLookup := lookup.exactShort || lookup.leadingShort

	mediaConditions := []string{}
	if includeMediaItems {
		if len(itemTypes) > 0 {
			mediaConditions = append(mediaConditions, fmt.Sprintf("mi.type = ANY($%d)", argIdx))
			args = append(args, mediaTypes)
			argIdx++
		}
		var libraryConditions []string
		appendLibraryAccessConditions("mi.content_id", filter, &libraryConditions, &args, &argIdx)
		if len(libraryConditions) > 0 {
			// Keep membership checks on the indexed title/alias identities. A
			// hoisted semi-join can hash an entire allowed library for a rare hit.
			mediaConditions = append(mediaConditions, "COALESCE((SELECT "+strings.Join(libraryConditions, " AND ")+" OFFSET 0), FALSE)")
		}
		applyAccessFilter("mi", AccessFilter{
			MaturityLimits:     filter.MaturityLimits,
			ExcludedMediaTypes: filter.ExcludedMediaTypes,
		}, &mediaConditions, &args, &argIdx)
		mediaConditions = append(mediaConditions, MangaChapterExclusionWhere("mi"))
	}

	var episodeLibraries, episodePolicy []string
	if includeEpisodes {
		appendEpisodeCatalogSearchAccess("ece", filter, &episodeLibraries, &episodePolicy, &args, &argIdx)
		ApplyMaturityLimits("ece", AccessFilter{
			MaturityLimits: filter.MaturityLimits,
		}, &episodePolicy, &args, &argIdx)
		if len(filter.ExcludedMediaTypes) > 0 {
			episodePolicy = append(episodePolicy, fmt.Sprintf("NOT ('episode' = ANY($%d))", argIdx))
			args = append(args, filter.ExcludedMediaTypes)
			argIdx++
		}
	}

	if includeMediaItems {
		r.appendSearchCursorDefinition(cursor, false, filter, &mediaConditions, &args, &argIdx)
	}
	if includeEpisodes {
		r.appendSearchCursorDefinition(cursor, true, filter, &episodePolicy, &args, &argIdx)
	}
	if cursor != nil && cursor.err != nil {
		return "", "", nil
	}
	exactIdx := argIdx
	args = append(args, parsed.ExactTitleHint)
	argIdx++
	lookup.exactIdx, lookup.titleLookupIdx = exactIdx, exactIdx
	if lookup.leadingShort {
		lookup.titleLookupIdx = argIdx
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
		titleMatch := lookup.condition("title_mi.title_normalized", "title_mi.search_title_vector")
		if lookup.exactShort {
			// A short word admits only whole titles, so also admit the title
			// followed by its year. The LIKE prefix keeps that arm on the
			// title_normalized index.
			titleMatch = fmt.Sprintf("(%s OR (title_mi.title_normalized LIKE $%d || ' %%' AND %s))",
				titleMatch, exactIdx, yearSuffixedTitleSQL("title_mi.title_normalized", "title_mi.year", exactIdx))
		}
		// Index each source separately and deduplicate identity before policy
		// and ranking. A runtime alias array inside OR forces generic prepared
		// plans to recheck that array per row, making broad alias queries quadratic.
		mediaMatch := "mi.content_id IN (SELECT title_mi.content_id FROM media_items title_mi WHERE " +
			titleMatch + " UNION SELECT alias_scores.content_id FROM alias_scores)"
		mediaTitleConditions = append([]string{mediaMatch}, mediaConditions...)
		if !narrowTitleLookup {
			mediaOverviewConditions = append([]string{searchOverviewMatchCondition(mediaSearchOverviewVector)}, mediaConditions...)
		}
	}
	var episodeTitle, episodeOverview *episodeSearchSource
	if includeEpisodes {
		fenced := filter.AllowedLibraryIDs != nil || len(filter.DisabledLibraryIDs) > 0 ||
			(cursor != nil && searchDefinitionNeedsPredicate(cursor.request.Definition))
		// The primary key is (media_folder_id, episode_id). A single allowed
		// library therefore yields each episode once without sorting its full
		// relevance tuple for DISTINCT; broader scopes still need deduplication.
		episodeTitle = &episodeSearchSource{
			match:     lookup.condition("ece.search_title_normalized", episodeSearchTitleVector),
			libraries: episodeLibraries, policy: episodePolicy, fenced: fenced,
			distinct: len(filter.AllowedLibraryIDs) != 1,
		}
		if !narrowTitleLookup {
			overview := *episodeTitle
			overview.match, overview.overview = searchOverviewMatchCondition(episodeSearchOverviewVector), true
			episodeOverview = &overview
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
			mediaSearchTitleVector, mediaSearchOverviewVector,
			[]string{`mi.title_normalized`, `mi.original_title_normalized`, `mi.sort_title_normalized`}, `mi.title_normalized`,
			"media_items mi LEFT JOIN alias_scores search_alias ON search_alias.content_id = mi.content_id",
			mediaTitleConditions, exactIdx, yearIdx, phraseIdx,
			&mediaAliasArms, false, false,
		))
		if !narrowTitleLookup {
			overviewBranches = append(overviewBranches, buildMixedSearchCandidateBranch(
				"mi.content_id", "mi.type", "mi.title", "mi.year",
				mediaSearchTitleVector, mediaSearchOverviewVector,
				[]string{`mi.title_normalized`, `mi.original_title_normalized`, `mi.sort_title_normalized`}, `mi.title_normalized`,
				"media_items mi", mediaOverviewConditions, exactIdx, yearIdx, phraseIdx,
				nil, true, false,
			))
		}
	}
	if episodeTitle != nil {
		titleBranches = append(titleBranches, episodeTitle.branch(exactIdx, yearIdx, phraseIdx))
	}
	if episodeOverview != nil {
		overviewBranches = append(overviewBranches, episodeOverview.branch(exactIdx, yearIdx, phraseIdx))
	}

	innerCTEs := make([]string, 0, 2)
	if includeMediaItems {
		innerCTEs = append(innerCTEs, buildMixedSearchAliasScoresCTE(lookup))
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
	if cursor != nil && cursor.request.GroupByWork {
		if cap := cursor.request.Definition.Limit; cap != nil {
			scoredBody = "SELECT * FROM (" + scoredBody + ") source_scored" + fmt.Sprintf(" ORDER BY %s LIMIT $%d", mixedSearchOrder(""), argIdx)
			args = append(args, *cap)
			argIdx++
		}
		scoredCTE = "WITH raw_scored AS (" + scoredBody + "), work_scored AS (SELECT raw_scored.*, ROW_NUMBER() OVER (PARTITION BY CASE WHEN raw_scored.type IN ('ebook','audiobook') AND work_link.work_id IS NOT NULL THEN 'work:' || work_link.work_id ELSE 'item:' || raw_scored.content_id END ORDER BY " + mixedSearchOrder("raw_scored.") + ") AS work_rank FROM raw_scored LEFT JOIN literary_work_items work_link ON work_link.content_id=raw_scored.content_id), scored AS (SELECT * FROM work_scored WHERE work_rank=1)"
	}
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

	if cursor != nil {
		cursor.relation = scoredCTE + " SELECT content_id " + postFilter
		cursor.relationArgs = append([]any(nil), args...)
	}
	countPostFilter := postFilter
	if cursor != nil {
		cursor.countArgs = append([]any(nil), args...)
		if cursor.after != nil && len(cursor.after.Keys) > 0 {
			seek, seekArgs, err := cursorSeekSQL(searchFTSTerms(), &QueryCursor{Keys: cursor.after.Keys}, argIdx)
			if err != nil {
				cursor.err = err
				return "", "", nil
			}
			if strings.Contains(postFilter, " WHERE ") {
				postFilter += " AND " + seek
			} else {
				postFilter += " WHERE " + seek
			}
			args = append(args, seekArgs...)
			argIdx += len(seekArgs)
		}
	}
	pageTotalColumn := ""
	finalTotalColumn := ""
	if includeTotal {
		pageTotalColumn = ", COUNT(*) OVER () AS total_count"
		finalTotalColumn = ", page.total_count"
	}
	limitIdx, offsetIdx := argIdx, argIdx+1
	args = append(args, limit)
	offsetClause := ""
	if cursor == nil || cursor.jump {
		args = append(args, offset)
		offsetClause = fmt.Sprintf(" OFFSET $%d", offsetIdx)
	}

	pageCTE := fmt.Sprintf(`, page AS (
		SELECT scored.*%s
		%s
		ORDER BY %s
		LIMIT $%d%s
	)`, pageTotalColumn, postFilter, mixedSearchOrder(""), limitIdx, offsetClause)

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

	if cursor != nil {
		finalTotalColumn += searchCursorKeyColumns()
	}
	dataSQL = scoredCTE + pageCTE + fmt.Sprintf(`
		SELECT %s%s
		FROM page
		JOIN %s ON true
		ORDER BY %s`, qualifiedItemColumns("hydrated"), finalTotalColumn, hydratedRelation, mixedSearchOrder("page."))
	countSQL = scoredCTE + fmt.Sprintf("\nSELECT COUNT(*)\n%s", countPostFilter)
	if cursor == nil || !cursor.request.GroupByWork {
		countSQL = buildMixedSearchCountSQL(mediaTitleConditions, mediaOverviewConditions, includeMediaItems,
			episodeTitle, episodeOverview, lookup, yearIdx, phraseIdx)
	}
	if cursor != nil && episodeTitle != nil && !includeMediaItems {
		cursor.exactSQL, cursor.exactArgs = buildEpisodeExactTierSQL(parsed, filter, cursor, episodeTitle, lookup, yearIdx, phraseIdx, limit)
	}
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

// searchTitleLookup selects title admission for every search source: equality
// for one short word, a leading-title lookup while the final word is short, and
// word-prefix FTS otherwise. titleLookupIdx equals exactIdx unless leadingShort.
type searchTitleLookup struct {
	exactShort, leadingShort bool
	exactIdx, titleLookupIdx int
}

func (l searchTitleLookup) condition(normalizedTitle, titleVector string) string {
	switch {
	case l.exactShort:
		return fmt.Sprintf("%s = $%d", normalizedTitle, l.exactIdx)
	case l.leadingShort:
		return fmt.Sprintf("%s LIKE $%d || '%%'", normalizedTitle, l.titleLookupIdx)
	}
	return searchTitleMatchCondition(titleVector)
}

// episodeSearchSource keeps one episode family's predicates by role. Fenced
// searches admit entries by match and library inside a planning boundary, then
// check the parent and policy; unrestricted searches flatten all of them.
type episodeSearchSource struct {
	match     string   // title or overview admission over ece
	overview  bool     // match reads ece.search_overview_vector
	libraries []string // allowed-library admission; empty when unrestricted
	policy    []string // membership, parent-library, maturity, type, and definition checks
	fenced    bool
	distinct  bool // entries can repeat an episode across allowed libraries
}

// Keep FTS admission ahead of parent policy joins. Otherwise PostgreSQL can
// start at the allowed series and read every episode in those series, applying
// a selective title match only as a filter. OFFSET 0 preserves this narrow
// streaming relation as a planning boundary without materializing its rows.
// Unrestricted queries keep a flattenable join so PostgreSQL can parallelize
// admission and DISTINCT sorting for broad matches.
func (s *episodeSearchSource) relation() string {
	if !s.fenced {
		return "episode_catalog_entries ece JOIN media_items si ON si.content_id = ece.series_id"
	}
	columns := `ece.episode_id, ece.series_id, ece.media_folder_id, ece.title, ece.year,
		       ece.search_title_normalized, ece.search_title_vector`
	if s.overview {
		columns += ", ece.search_overview_vector"
	}
	columns += ", ece.content_rating_age, ece.advisory_age"
	admission := append([]string{s.match}, s.libraries...)
	return `(
		SELECT ` + columns + `, si.content_id AS search_series_parent_id
		FROM (
		SELECT ` + columns + `
		FROM episode_catalog_entries ece WHERE ` + strings.Join(admission, " AND ") + ` OFFSET 0
		) ece LEFT JOIN media_items si ON si.content_id = ece.series_id AND si.type = 'series'
		OFFSET 0
	) ece`
}

// conditions returns the WHERE predicates that accompany relation.
func (s *episodeSearchSource) conditions() []string {
	if !s.fenced {
		conditions := append([]string{s.match, episodeSearchParentIsSeries}, s.libraries...)
		return append(conditions, s.policy...)
	}
	// Preserve the left join inside its planning boundary. Its output
	// cardinality lets rare FTS terms probe a few parents while broad
	// terms choose a spillable hash join before library semi-joins.
	return append([]string{"ece.search_series_parent_id IS NOT NULL"}, s.policy...)
}

func (s *episodeSearchSource) branch(exactIdx, yearIdx, phraseIdx int) string {
	return buildMixedSearchCandidateBranch(
		"ece.episode_id", "'episode'::text", episodeSearchTitleExpr, "ece.year",
		episodeSearchTitleVector, episodeSearchOverviewVector,
		[]string{"ece.search_title_normalized"}, "",
		s.relation(), s.conditions(), exactIdx, yearIdx, phraseIdx,
		nil, s.overview, s.distinct,
	)
}

// Counts need candidate identity, not relevance. Keep the same access and
// title-family gate, and rank only overview fallback rows to enforce its floor.
// Grouped work counts retain the scored path because a definition's cap applies
// before grouping and therefore depends on relevance order.
func buildMixedSearchCountSQL(
	mediaTitle, mediaOverview []string, includeMedia bool,
	episodeTitle, episodeOverview *episodeSearchSource,
	lookup searchTitleLookup, yearIdx, phraseIdx int,
) string {
	ctes := []string{fmt.Sprintf(`search_parameters AS (SELECT $1::text, $2::text, $%d::text, $%d::int, $%d::text)`, lookup.exactIdx, yearIdx, phraseIdx)}
	if includeMedia {
		ctes = append(ctes, `alias_scores AS MATERIALIZED (
			SELECT DISTINCT mia.content_id FROM media_item_aliases mia
			WHERE `+mixedSearchAliasMatchCondition(lookup)+`)`)
	}
	titles := []string{}
	overviews := []string{}
	if includeMedia {
		titles = append(titles, "SELECT mi.content_id FROM media_items mi WHERE "+strings.Join(mediaTitle, " AND "))
		if len(mediaOverview) > 0 {
			overviews = append(overviews, "SELECT mi.content_id FROM media_items mi WHERE "+strings.Join(mediaOverview, " AND ")+fmt.Sprintf(" AND ts_rank_cd(%s, websearch_to_tsquery('english', $1)) >= %g", mediaSearchOverviewVector, overviewMatchFloor))
		}
	}
	episodeCandidates := func(source *episodeSearchSource) string {
		projection := "SELECT ece.episode_id AS content_id"
		if source.distinct {
			projection = "SELECT DISTINCT ece.episode_id AS content_id"
		}
		return projection + " FROM " + source.relation() + " WHERE " + strings.Join(source.conditions(), " AND ")
	}
	if episodeTitle != nil {
		titles = append(titles, episodeCandidates(episodeTitle))
	}
	if episodeOverview != nil {
		overviews = append(overviews, episodeCandidates(episodeOverview)+fmt.Sprintf(" AND ts_rank_cd(%s, websearch_to_tsquery('english', $1)) >= %g", episodeSearchOverviewVector, overviewMatchFloor))
	}
	ctes = append(ctes, "title_candidates AS MATERIALIZED ("+strings.Join(titles, " UNION ALL ")+")")
	if len(overviews) == 0 {
		return "WITH " + strings.Join(ctes, ",\n") + " SELECT COUNT(*) FROM title_candidates"
	}
	ctes = append(ctes, `candidates AS (
		SELECT content_id FROM title_candidates
		UNION ALL
		SELECT content_id FROM (`+strings.Join(overviews, " UNION ALL ")+`) overview_candidates
		WHERE NOT EXISTS (SELECT 1 FROM title_candidates)
	)`)
	return "WITH " + strings.Join(ctes, ",\n") + " SELECT COUNT(*) FROM candidates"
}

func mixedSearchAliasMatchCondition(lookup searchTitleLookup) string {
	return lookup.condition("mia.normalized_title", "to_tsvector('simple', mia.normalized_title)")
}

// buildMixedSearchAliasScoresCTE performs one index-backed pass over aliases
// matching the exact, leading-prefix, or FTS-prefix path, then reuses those
// scores for candidate admission and ranking. Keeping this work uncorrelated is
// critical: the former rank subqueries could scan the complete covering alias
// index once per media candidate when PostgreSQL failed to parameterize them.
// On large alias catalogs that turned a selective search into millions of
// index-entry visits and pushed the plan over the JIT threshold.
func buildMixedSearchAliasScoresCTE(lookup searchTitleLookup) string {
	weightedAliasVector := `setweight(to_tsvector('simple', mia.normalized_title), 'A')`
	prefixQuery := `to_tsquery('simple', $2)`
	matchCondition := mixedSearchAliasMatchCondition(lookup)
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
		lookup.exactIdx, lookup.exactIdx,
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
	yearSuffixedTitle string, // normalized title that may also match as yearSuffixedTitleSQL; "" for none
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
	if yearSuffixedTitle != "" {
		exactArms = append(exactArms, yearSuffixedTitleSQL(yearSuffixedTitle, yearExpr, exactIdx))
	}
	prefixQuery := `to_tsquery('simple', $2)`
	prefixRankExpr := fmt.Sprintf("ts_rank_cd(%s, %s)", titleVector, prefixQuery)
	if aliasArms != nil {
		// The shared alias_scores join supplies exact/contiguous flags and
		// prefix rank for matched identities alongside the stored title vector.
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
// The allowed-library predicate goes to libraries, which can admit entries
// before the parent join; membership and parent checks go to policy.
func appendEpisodeCatalogSearchAccess(
	alias string,
	filter AccessFilter,
	libraries, policy *[]string,
	args *[]any,
	argIdx *int,
) {
	if filter.AllowedLibraryIDs != nil {
		*libraries = append(*libraries, fmt.Sprintf("%s.media_folder_id = ANY($%d)", alias, *argIdx))
		*args = append(*args, filter.AllowedLibraryIDs)
		*argIdx++
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		*policy = append(*policy, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM episode_catalog_entries disabled_ece WHERE disabled_ece.episode_id = %s.episode_id AND disabled_ece.media_folder_id = ANY($%d))",
			alias, *argIdx))
		*args = append(*args, filter.DisabledLibraryIDs)
		*argIdx++
	}
	// The parent series can be library-restricted independently of its episode
	// files, so retain that policy check even though availability is denormalized.
	appendEpisodeParentLibraryAccess(alias+".series_id", filter, policy, args, argIdx)
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
