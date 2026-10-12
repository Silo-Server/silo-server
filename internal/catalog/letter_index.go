package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LetterOther is the letter index bucket for titles that start with anything
// but a Latin letter: digits, punctuation, and other scripts.
const LetterOther = "#"

// LetterBucket is one letter of a title-ordered list: how many items file
// under it and the zero-based list position of the first of them.
type LetterBucket struct {
	Letter   string
	Count    int
	Position int
}

// letterFoldFrom and letterFoldTo fold accented Latin initials to their base
// letter, so "Élite" files under E rather than #. Capitals are listed too:
// under a C ctype the title key's LOWER() leaves non-ASCII letters as they are.
const (
	letterFoldFrom = "àáâãäåāăąçćĉċčďđèéêëēĕėęěĝğġģĥħìíîïĩīĭįıĵķĺļľŀłñńņňòóôõöøōŏőŕŗřśŝşšţťŧùúûüũūŭůűųŵýÿŷźżžÀÁÂÃÄÅĀĂĄÇĆĈĊČĎĐÈÉÊËĒĔĖĘĚĜĞĠĢĤĦÌÍÎÏĨĪĬĮĴĶĹĻĽĿŁÑŃŅŇÒÓÔÕÖØŌŎŐŔŖŘŚŜŞŠŢŤŦÙÚÛÜŨŪŬŮŰŲŴÝŸŶŹŻŽ"
	letterFoldTo   = "aaaaaaaaacccccddeeeeeeeeegggghhiiiiiiiiijklllllnnnnooooooooorrrsssstttuuuuuuuuuuwyyyzzzaaaaaaaaacccccddeeeeeeeeegggghhiiiiiiiijklllllnnnnooooooooorrrsssstttuuuuuuuuuuwyyyzzz"
)

// letterBucketSQL files a lowercased title key under its initial: A to Z after
// folding accents, else LetterOther. ascii() returns the first character's
// code point, so the range check holds under any database collation.
func letterBucketSQL(titleKey string) string {
	initial := fmt.Sprintf("translate(left(%s, 1), '%s', '%s')", titleKey, letterFoldFrom, letterFoldTo)
	return fmt.Sprintf("CASE WHEN ascii(%[1]s) BETWEEN 97 AND 122 THEN upper(%[1]s) ELSE '%[2]s' END", initial, LetterOther)
}

// LetterIndex answers, for def in title order, how many matches file under
// each letter and where each letter's first match sits in the list. Letters
// without matches are left out; the rest come in list order. The positions
// number the rows of the same plan PreviewCursorPage and SeekCursor page
// through, so a client can seek straight to one.
func (e *QueryExecutor) LetterIndex(ctx context.Context, def QueryDefinition, access AccessFilter) ([]LetterBucket, error) {
	if e == nil || e.Pool == nil {
		return nil, fmt.Errorf("query executor requires a database pool")
	}
	if e.GroupByWork {
		return nil, fmt.Errorf("%w: the letter index does not group by work", ErrInvalidCatalogRequest)
	}
	if NormalizeQuerySort(def.Sort).Field != querySortTitle {
		return nil, fmt.Errorf("%w: the letter index needs title order", ErrInvalidCatalogRequest)
	}
	plan, err := e.buildPreviewPagePlan(def, access, 1, 0)
	if err != nil {
		return nil, err
	}
	sql, args := plan.letterIndexSQL()
	rows, err := e.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("querying letter index: %w", err)
	}
	defer rows.Close()
	buckets := []LetterBucket{}
	for rows.Next() {
		var b LetterBucket
		if err := rows.Scan(&b.Letter, &b.Count, &b.Position); err != nil {
			return nil, fmt.Errorf("scanning letter index: %w", err)
		}
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading letter index: %w", err)
	}
	return buckets, nil
}

// letterIndexSQL numbers the plan's rows in its own order and groups them by
// initial, keeping each group's size and first row.
func (p previewPagePlan) letterIndexSQL() (string, []any) {
	args := append([]any{}, p.cteArgs...)
	args = append(args, p.args...)
	args = append(args, p.sortArgs...)
	withClause := ""
	if len(p.ctes) > 0 {
		withClause = "WITH " + strings.Join(p.ctes, ",\n") + "\n"
	}
	sql := fmt.Sprintf(
		`%sSELECT bucket, COUNT(*)::int, (MIN(position) - 1)::int FROM (
			SELECT %s AS bucket, ROW_NUMBER() OVER (%s) AS position %s %s
		) ranked GROUP BY bucket ORDER BY 3`,
		withClause, letterBucketSQL(p.titleKey), p.orderBy, p.fromClausePaged, p.whereClause,
	)
	return sql, args
}

// LetterIndex answers the letter index of a title-ordered query-source
// browse: the catalog, optionally narrowed by library, media type, and rule
// groups. Search text and other sources have no stable title position to
// jump to, so they are refused.
func (r *CatalogResolver) LetterIndex(ctx context.Context, req CatalogRequest, access AccessFilter) ([]LetterBucket, error) {
	if r == nil || r.itemRepo == nil {
		return nil, fmt.Errorf("catalog resolver requires an item repository")
	}
	if req.Source != CatalogSourceQuery {
		return nil, fmt.Errorf("%w: the letter index covers source=query only", ErrInvalidCatalogRequest)
	}
	if strings.TrimSpace(req.SearchQuery) != "" || strings.TrimSpace(req.NamePrefix) != "" {
		return nil, fmt.Errorf("%w: the letter index does not take search text or a name prefix", ErrInvalidCatalogRequest)
	}
	if err := validateCatalogQueryRequest(req, strings.TrimSpace(access.ProfileID) != ""); err != nil {
		return nil, err
	}
	if err := r.requireQueryStore(ctx, req.Query, access); err != nil {
		return nil, err
	}
	now := time.Now()
	return r.queryExecutorForScope(req.Query.MediaScope, &now).LetterIndex(ctx, req.Query, access)
}
