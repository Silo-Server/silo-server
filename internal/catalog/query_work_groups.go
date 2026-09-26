package catalog

import (
	"fmt"
	"strings"
)

// groupedByWork chooses the first matching edition in the complete source
// ordering before applying a page boundary. Seeking raw editions first would
// repeat a work through another edition on a later page.
func (p previewPagePlan) groupedByWork() previewPagePlan {
	columns := []string{"mi.*", `CASE WHEN mi.type IN ('ebook','audiobook') AND work_link.work_id IS NOT NULL THEN 'work:' || work_link.work_id ELSE 'item:' || mi.content_id END AS catalog_work_key`}
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
	sourceLimit := ""
	sourceOrder := ""
	if p.maxResults > 0 {
		sourceOrder = p.orderBy
		p.args = append(p.args, p.maxResults)
		sourceLimit = fmt.Sprintf(" LIMIT $%d", len(p.cteArgs)+len(p.args))
	}
	candidates := fmt.Sprintf("catalog_work_candidates AS (SELECT %s %s LEFT JOIN literary_work_items work_link ON work_link.content_id=mi.content_id %s %s%s)", strings.Join(columns, ", "), p.fromClausePaged, p.whereClause, sourceOrder, sourceLimit)
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
