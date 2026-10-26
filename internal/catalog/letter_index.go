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

// LetterRun is a block of consecutive list positions whose items all file
// under one letter, starting at Position and ending where the next run
// starts. A letter usually has one run, but a collation can sort some of its
// titles apart from the rest: under C, accented initials sort after z, and
// most collations put non-Latin scripts after the Latin letters.
type LetterRun struct {
	Letter   string
	Position int
}

// LetterIndex is a title-ordered list's letters, with their counts and first
// positions in list order, and the runs that tell which letter any position
// is under.
type LetterIndex struct {
	Letters []LetterBucket
	Runs    []LetterRun
}

// letterFoldFrom and letterFoldTo fold accented Latin initials to their base
// letter, so "Élite" files under E rather than #. Capitals are listed too:
// under a C ctype the title key's LOWER() leaves non-ASCII letters as they are.
const (
	letterFoldFrom = "àáâãäåāăąçćĉċčďđèéêëēĕėęěĝğġģĥħìíîïĩīĭįıĵķĺļľŀłñńņňòóôõöøōŏőŕŗřśŝşšţťŧùúûüũūŭůűųŵýÿŷźżžÀÁÂÃÄÅĀĂĄÇĆĈĊČĎĐÈÉÊËĒĔĖĘĚĜĞĠĢĤĦÌÍÎÏĨĪĬĮĴĶĹĻĽĿŁÑŃŅŇÒÓÔÕÖØŌŎŐŔŖŘŚŜŞŠŢŤŦÙÚÛÜŨŪŬŮŰŲŴÝŸŶŹŻŽșțȘȚ"
	letterFoldTo   = "aaaaaaaaacccccddeeeeeeeeegggghhiiiiiiiiijklllllnnnnooooooooorrrsssstttuuuuuuuuuuwyyyzzzaaaaaaaaacccccddeeeeeeeeegggghhiiiiiiiijklllllnnnnooooooooorrrsssstttuuuuuuuuuuwyyyzzzstst"
)

// letterBucketSQL files a lowercased title key under its initial: A to Z after
// folding accents, else LetterOther. ascii() returns the first character's
// code point, so the range check holds under any database collation.
func letterBucketSQL(titleKey string) string {
	initial := fmt.Sprintf("translate(left(%s, 1), '%s', '%s')", titleKey, letterFoldFrom, letterFoldTo)
	return fmt.Sprintf("CASE WHEN ascii(%[1]s) BETWEEN 97 AND 122 THEN upper(%[1]s) ELSE '%[2]s' END", initial, LetterOther)
}

// LetterIndex answers, for def in title order, how many matches file under
// each letter, where each letter's first match sits in the list, and the runs
// of consecutive matches under one letter. Letters without matches are left
// out; the rest come in list order. The positions number the rows of the same
// plan PreviewCursorPage and SeekCursor page through, so a client can seek
// straight to one.
func (e *QueryExecutor) LetterIndex(ctx context.Context, def QueryDefinition, access AccessFilter) (LetterIndex, error) {
	if e == nil || e.Pool == nil {
		return LetterIndex{}, fmt.Errorf("query executor requires a database pool")
	}
	if e.GroupByWork {
		return LetterIndex{}, fmt.Errorf("%w: the letter index does not group by work", ErrInvalidCatalogRequest)
	}
	if NormalizeQuerySort(def.Sort).Field != querySortTitle {
		return LetterIndex{}, fmt.Errorf("%w: the letter index needs title order", ErrInvalidCatalogRequest)
	}
	plan, err := e.buildPreviewPagePlan(def, access, 1, 0)
	if err != nil {
		return LetterIndex{}, err
	}
	sql, args := plan.letterIndexSQL()
	load := func(ctx context.Context) (LetterIndex, error) { return e.queryLetterIndex(ctx, sql, args) }
	key, ok := queryCacheKey(sql, args)
	if !ok {
		return load(ctx)
	}
	return e.LetterIndexes.get(ctx, key, load)
}

func (e *QueryExecutor) queryLetterIndex(ctx context.Context, sql string, args []any) (LetterIndex, error) {
	rows, err := e.Pool.Query(ctx, sql, args...)
	if err != nil {
		return LetterIndex{}, fmt.Errorf("querying letter index: %w", err)
	}
	defer rows.Close()
	index := LetterIndex{Letters: []LetterBucket{}, Runs: []LetterRun{}}
	bucketAt := map[string]int{}
	for rows.Next() {
		var run LetterRun
		var length int
		if err := rows.Scan(&run.Letter, &run.Position, &length); err != nil {
			return LetterIndex{}, fmt.Errorf("scanning letter index: %w", err)
		}
		index.Runs = append(index.Runs, run)
		if i, ok := bucketAt[run.Letter]; ok {
			index.Letters[i].Count += length
			continue
		}
		bucketAt[run.Letter] = len(index.Letters)
		index.Letters = append(index.Letters, LetterBucket{Letter: run.Letter, Count: length, Position: run.Position})
	}
	if err := rows.Err(); err != nil {
		return LetterIndex{}, fmt.Errorf("reading letter index: %w", err)
	}
	return index, nil
}

const (
	// letterIndexCacheTTL bounds how stale a cached index can be: a title
	// added, removed or retitled moves later positions within this window.
	// Clients keep an index longer than this anyway.
	letterIndexCacheTTL = 2 * time.Minute
	// letterIndexCacheBudget caps the runs one node holds. An index has a
	// few hundred at most.
	letterIndexCacheBudget = 200_000
	letterIndexFillLimit   = 4
	letterIndexLoadTimeout = 30 * time.Second
)

// letterIndexCache holds the letter index of each title-ordered query; it
// numbers every match, so building one is a full scan and sort.
type letterIndexCache = queryCache[LetterIndex]

func newLetterIndexCache() *letterIndexCache {
	return newQueryCache(letterIndexCacheTTL, letterIndexCacheBudget, letterIndexFillLimit, letterIndexLoadTimeout,
		func(index LetterIndex) int { return len(index.Runs) })
}

// letterIndexSQL numbers the plan's rows in its own order, files each under
// its initial, and keeps the rows that open a run (their letter differs from
// the row before), with the run's length.
func (p previewPagePlan) letterIndexSQL() (string, []any) {
	args := append([]any{}, p.cteArgs...)
	args = append(args, p.args...)
	args = append(args, p.sortArgs...)
	withClause := ""
	if len(p.ctes) > 0 {
		withClause = "WITH " + strings.Join(p.ctes, ",\n") + "\n"
	}
	sql := fmt.Sprintf(
		`%sSELECT bucket, start, COALESCE(LEAD(start) OVER (ORDER BY start), total) - start FROM (
			SELECT bucket, (position - 1)::int AS start, total::int AS total,
				bucket IS DISTINCT FROM LAG(bucket) OVER (ORDER BY position) AS opens_run
			FROM (
				SELECT %s AS bucket, ROW_NUMBER() OVER (%s) AS position, COUNT(*) OVER () AS total %s %s
			) ranked
		) marked WHERE opens_run ORDER BY start`,
		withClause, letterBucketSQL(p.titleKey), p.orderBy, p.fromClausePaged, p.whereClause,
	)
	return sql, args
}

// LetterIndex answers the letter index of a title-ordered query-source
// browse: the catalog, optionally narrowed by library, media type, and rule
// groups. Search text and other sources have no stable title position to
// jump to, so they are refused.
func (r *CatalogResolver) LetterIndex(ctx context.Context, req CatalogRequest, access AccessFilter) (LetterIndex, error) {
	if r == nil || r.itemRepo == nil {
		return LetterIndex{}, fmt.Errorf("catalog resolver requires an item repository")
	}
	if req.Source != CatalogSourceQuery {
		return LetterIndex{}, fmt.Errorf("%w: the letter index covers source=query only", ErrInvalidCatalogRequest)
	}
	if strings.TrimSpace(req.SearchQuery) != "" || strings.TrimSpace(req.NamePrefix) != "" {
		return LetterIndex{}, fmt.Errorf("%w: the letter index does not take search text or a name prefix", ErrInvalidCatalogRequest)
	}
	if err := validateCatalogQueryRequest(req, strings.TrimSpace(access.ProfileID) != ""); err != nil {
		return LetterIndex{}, err
	}
	if err := r.requireQueryStore(ctx, req.Query, access); err != nil {
		return LetterIndex{}, err
	}
	// No snapshot: the index covers the live list, and a stable query is what
	// lets it be cached.
	executor := r.queryExecutorForScope(req.Query.MediaScope, nil)
	executor.LetterIndexes = r.letterIndexes
	return executor.LetterIndex(ctx, req.Query, access)
}
