package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Seek anchors make a window jump into a long list cheap. Without them a seek
// to position n has PostgreSQL number every match before n (OFFSET), which on
// a 100k-title library is a full scan and sort on every jump. One such pass
// records the sort tuple at every seekAnchorStride-th position; a seek then
// starts at the nearest anchor and walks fewer than seekAnchorStride matches
// through the sort index.
//
// Anchors are taken per browse snapshot: titles created after it are not in
// the list, so additions never move positions. A title removed or retitled
// after the anchors were taken shifts later positions by one until they
// expire, the drift a live list already shows between pages.
const (
	seekAnchorStride   = 256
	seekAnchorCacheTTL = 5 * time.Minute
	// seekAnchorCacheBudget caps the anchors one node holds, roughly 250
	// bytes each, so about 25 MB. A 100k-title list has about 400.
	seekAnchorCacheBudget = 100_000
	seekAnchorFillLimit   = 4
	// seekAnchorLoadTimeout bounds one anchor pass; it runs on after the
	// request that started it is canceled, for the next jump.
	seekAnchorLoadTimeout = 30 * time.Second
)

// seekAnchorCache holds the anchors of each anchored query: anchors[i] is the
// sort tuple at position i*seekAnchorStride.
type seekAnchorCache = queryCache[[]QueryCursor]

func newSeekAnchorCache() *seekAnchorCache {
	return newQueryCache(seekAnchorCacheTTL, seekAnchorCacheBudget, seekAnchorFillLimit, seekAnchorLoadTimeout,
		func(anchors []QueryCursor) int { return len(anchors) })
}

// anchoredSeek reads the sort tuple at position target, starting from the
// nearest anchor. anchored is false when anchors don't apply (see
// canAnchorSeek) or their pass failed; the caller then seeks with OFFSET.
func (e *QueryExecutor) anchoredSeek(ctx context.Context, plan previewPagePlan, target int) (key *QueryCursor, anchored bool, err error) {
	if !e.canAnchorSeek(plan, target) {
		return nil, false, nil
	}
	sql, args := plan.seekAnchorSQL()
	cacheKey, ok := queryCacheKey(sql, args)
	if !ok {
		return nil, false, nil
	}
	anchors, err := e.SeekAnchors.get(ctx, cacheKey, func(ctx context.Context) ([]QueryCursor, error) {
		return e.loadSeekAnchors(ctx, plan.cursorTerms, sql, args)
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, true, err
		}
		slog.WarnContext(ctx, "catalog: seek anchor pass failed; seeking with OFFSET", "component", "catalog", "error", err)
		return nil, false, nil
	}
	if len(anchors) == 0 {
		return nil, true, pgx.ErrNoRows
	}
	slot := min(target/seekAnchorStride, len(anchors)-1)
	anchor := anchors[slot]
	if walk := target - slot*seekAnchorStride; walk > 0 {
		key, err = e.queryTuple(ctx, plan, &anchor, walk-1)
		return key, true, err
	}
	return &QueryCursor{Keys: slices.Clone(anchor.Keys)}, true, nil
}

// canAnchorSeek reports whether a seek should use anchors: the executor has an
// anchor cache, the target lies past the first anchor (an OFFSET that short is
// already cheap), and the plan's leading sort term bounds an index scan, without
// which a walk from an anchor would scan from the first row anyway.
func (e *QueryExecutor) canAnchorSeek(plan previewPagePlan, target int) bool {
	return e.SeekAnchors != nil && target >= seekAnchorStride && len(plan.cursorTerms) > 0 && plan.cursorTerms[0].notNull
}

// seekAnchorSQL numbers the plan's rows in its own order and keeps the sort
// tuple of every seekAnchorStride-th one.
func (p previewPagePlan) seekAnchorSQL() (string, []any) {
	args := append([]any{}, p.cteArgs...)
	args = append(args, p.args...)
	args = append(args, p.sortArgs...)
	inner := make([]string, len(p.cursorTerms))
	outer := make([]string, len(p.cursorTerms))
	for i, term := range p.cursorTerms {
		inner[i] = fmt.Sprintf("(%s)::text AS seek_key_%d", term.expression, i)
		outer[i] = fmt.Sprintf("seek_key_%d", i)
	}
	sql := fmt.Sprintf(
		"%sSELECT %s FROM (SELECT %s, ROW_NUMBER() OVER (%s) - 1 AS seek_position %s %s) ranked WHERE seek_position %% %d = 0 ORDER BY seek_position",
		p.withClause(), strings.Join(outer, ", "), strings.Join(inner, ", "), p.orderBy, p.fromClausePaged, p.whereClause, seekAnchorStride,
	)
	return sql, args
}

func (e *QueryExecutor) loadSeekAnchors(ctx context.Context, terms []queryCursorTerm, sql string, args []any) ([]QueryCursor, error) {
	rows, err := e.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	anchors := []QueryCursor{}
	for rows.Next() {
		anchor := QueryCursor{Keys: make([]QueryCursorValue, len(terms))}
		dest := make([]any, len(terms))
		for i, term := range terms {
			anchor.Keys[i].Kind = term.kind
			dest[i] = &anchor.Keys[i].Value
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		anchors = append(anchors, anchor)
	}
	return anchors, rows.Err()
}
