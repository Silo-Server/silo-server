package catalog

import (
	"fmt"
	"strconv"
	"strings"
)

// An exact episode title fixes all six relevance scores when no year/phrase
// hint is present. Within one library identity is unique, so the composite
// index can select the best remaining title/ID ties before scoring. A partial
// exact tier always falls back to the general query in the same snapshot.
func buildEpisodeExactTierSQL(parsed parsedSearchQuery, filter AccessFilter, cursor *searchCursorSQL, conditions []string, exactIdx, yearIdx, phraseIdx, limit int) (string, []any) {
	if cursor == nil || cursor.jump || parsed.ExactTitleHint == "" || parsed.Year != nil || parsed.Phrase != "" ||
		len(filter.AllowedLibraryIDs) != 1 || cursor.request.GroupByWork || len(cursor.request.Definition.Groups) > 0 {
		return "", nil
	}
	args := append([]any(nil), cursor.countArgs...)
	// The title vector is constant within this tier. Check FTS admission once;
	// a redundant GIN condition can instead combine indexes and sort every hit.
	admission := cursorTruePredicate
	if strings.Contains(conditions[0], episodeSearchTitleVector) {
		admission = strings.ReplaceAll(conditions[0], episodeSearchTitleVector, fmt.Sprintf("setweight(to_tsvector('simple', $%d::text), 'A')", exactIdx))
	}
	policyConditions := append([]string{fmt.Sprintf("ece.search_title_normalized = $%d", exactIdx)}, conditions[1:]...)
	conditions = []string{admission, fmt.Sprintf("ece.media_folder_id = $%d::bigint", len(args)+1)}
	args = append(args, filter.AllowedLibraryIDs[0])
	conditions = append(conditions, fmt.Sprintf("hashtext(ece.search_title_normalized) = hashtext($%d::text)", exactIdx))
	if after := cursor.after; after != nil && len(after.Keys) > 0 {
		if len(after.Keys) != len(searchFTSTerms()) {
			return "", nil
		}
		for index, want := range [...]float64{1, 1, 0, 0, 0, 0} {
			if index == 4 {
				continue
			}
			key := after.Keys[index]
			if key.Kind != cursorKindNumber || key.Value == nil {
				return "", nil
			}
			value, err := strconv.ParseFloat(*key.Value, 64)
			if err != nil || value != want {
				return "", nil
			}
		}
		if after.Keys[4].Value == nil || after.Keys[6].Value == nil || after.Keys[7].Value == nil {
			return "", nil
		}
		index := len(args) + 1
		args = append(args, after.Keys[4].Value, after.Keys[6].Value, after.Keys[7].Value)
		// Compare the constant prefix score in PostgreSQL's real type, matching
		// the rank and cursor text representation without a Go approximation.
		conditions = append(conditions,
			fmt.Sprintf("ts_rank_cd(setweight(to_tsvector('simple', $%d::text), 'A'), to_tsquery('simple', $2)) = $%d::real", exactIdx, index),
			fmt.Sprintf("(LOWER(ece.title), ece.episode_id) > ($%d::text, $%d::text)", index+1, index+2),
		)
	}
	limitIdx := len(args) + 1
	args = append(args, limit)
	// Keep parent admission correlated with the ordered entry scan. Pulling
	// those predicates into joins can replace the stream with a hash join and
	// a sort of every exact hit. Equality also rechecks any hash collision.
	source := fmt.Sprintf(`(
		SELECT ece.episode_id, ece.title, ece.year, ece.search_title_normalized,
		       ece.search_title_vector, ece.search_overview_vector
		FROM episode_catalog_entries ece
		WHERE %s
		  AND COALESCE((
			SELECT %s FROM media_items si WHERE si.content_id=ece.series_id OFFSET 0
		  ), FALSE)
		ORDER BY LOWER(ece.title), ece.episode_id
		LIMIT $%d
	) ece`, strings.Join(conditions, " AND "), strings.Join(policyConditions, " AND "), limitIdx)
	scored := buildMixedSearchCandidateBranch("ece.episode_id", "'episode'::text", episodeSearchTitleExpr, "ece.year",
		episodeSearchTitleVector, episodeSearchOverviewVector, []string{"ece.search_title_normalized"}, source,
		[]string{"$1::text IS NOT NULL"}, exactIdx, yearIdx, phraseIdx, nil, false, false)
	keys := ""
	for _, term := range searchFTSTerms() {
		expression := "page." + term.expression
		if term.expression == searchLowerTitleExpression {
			expression = "LOWER(page.title)"
		}
		keys += ", (" + expression + ")::text"
	}
	return fmt.Sprintf(`/* exact episode tier */ WITH scored AS (%s), page AS (SELECT * FROM scored)
		SELECT %s%s
		FROM page
		JOIN LATERAL (SELECT %s FROM %s WHERE mi.content_id=page.content_id OFFSET 0) hydrated ON true
		ORDER BY %s`, scored, qualifiedItemColumns("hydrated"), keys,
		qualifiedItemColumns("mi"), episodeCatalogBaseRelation, mixedSearchOrder("page.")), args
}
