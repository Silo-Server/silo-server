package catalog

import (
	"fmt"
	"strings"
)

// groupedByWork chooses the first matching edition in the complete source
// ordering before applying a page boundary. Seeking raw editions first would
// repeat a work through another edition on a later page.
//
// boxSetCollections, when not empty, also groups the movies of those TMDB
// collections: each collection is represented by its first movie in the
// requested order, which the caller turns into a box set card.
func (p previewPagePlan) groupedByWork(boxSetCollections []int64) previewPagePlan {
	boxSetKey := ""
	if len(boxSetCollections) > 0 {
		boxSetKey = " WHEN box_set_link.tmdb_collection_id IS NOT NULL THEN '" + BoxSetContentIDPrefix + "' || box_set_link.tmdb_collection_id"
	}
	columns := []string{"mi.*", `CASE WHEN mi.type IN ('ebook','audiobook') AND work_link.work_id IS NOT NULL THEN 'work:' || work_link.work_id` + boxSetKey + ` ELSE 'item:' || mi.content_id END AS catalog_work_key`}
	terms := append([]queryCursorTerm(nil), p.cursorTerms...)
	innerOrder := make([]string, len(terms))
	outerOrder := make([]string, len(terms))
	for i := range terms {
		alias := fmt.Sprintf("catalog_work_sort_%d", i)
		columns = append(columns, terms[i].expression+" AS "+alias)
		suffix := " ASC NULLS LAST"
		if terms[i].descending {
			suffix = " DESC NULLS LAST"
		}
		if !terms[i].nullsLast {
			suffix = strings.ReplaceAll(suffix, "LAST", "FIRST")
		}
		innerOrder[i] = alias + suffix
		terms[i].expression = "mi." + alias
		outerOrder[i] = terms[i].expression + suffix
	}
	// Sort parameters become candidate parameters, because the representative
	// and exact grouped count both depend on the requested source ordering.
	p.args = append(p.args, p.sortArgs...)
	p.sortArgs = nil
	boxSetJoin := ""
	if len(boxSetCollections) > 0 {
		p.args = append(p.args, boxSetCollections)
		boxSetJoin = fmt.Sprintf(" LEFT JOIN media_item_tmdb_collections box_set_link ON box_set_link.content_id=mi.content_id AND mi.type='movie' AND box_set_link.checked_tmdb_id::text=mi.tmdb_id AND box_set_link.tmdb_collection_id = ANY($%d)", len(p.cteArgs)+len(p.args))
	}
	sourceLimit := ""
	sourceOrder := ""
	if p.maxResults > 0 {
		sourceOrder = p.orderBy
		p.args = append(p.args, p.maxResults)
		sourceLimit = fmt.Sprintf(" LIMIT $%d", len(p.cteArgs)+len(p.args))
	}
	candidates := fmt.Sprintf("catalog_work_candidates AS (SELECT %s %s LEFT JOIN literary_work_items work_link ON work_link.content_id=mi.content_id%s %s %s%s)", strings.Join(columns, ", "), p.fromClausePaged, boxSetJoin, p.whereClause, sourceOrder, sourceLimit)
	ranked := "catalog_work_ranked AS (SELECT catalog_work_candidates.*, ROW_NUMBER() OVER (PARTITION BY catalog_work_key ORDER BY " + strings.Join(innerOrder, ", ") + ") AS catalog_work_rank FROM catalog_work_candidates)"
	p.ctes = append(p.ctes, candidates, ranked)
	p.fromClausePaged = "FROM catalog_work_ranked mi"
	p.fromClauseCount = p.fromClausePaged
	p.whereClause = "WHERE mi.catalog_work_rank = 1"
	p.orderBy = "ORDER BY " + strings.Join(outerOrder, ", ")
	p.cursorTerms = terms
	p.maxResults = 0
	p.limitArgIdx = len(p.cteArgs) + len(p.args) + 1
	return p
}
