package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Section cursor ordering matches BrowseRepository, including its historical
// added_at mapping to created_at and content-ID tie breakers. It deliberately
// does not replace section ordering with the generic query sort definitions.
func (r *CatalogResolver) resolveSectionOrderCursor(ctx context.Context, req CatalogRequest, access AccessFilter, snapshot time.Time, sort, order string) (*CatalogResult, error) {
	req.Query.Sort = QuerySort{Field: querySortTitle, Order: querySortAsc}
	executor := r.queryExecutorForScope(req.Query.MediaScope, &snapshot)
	executor.SourceWhere = cursorTruePredicate
	descending := !strings.EqualFold(order, querySortAsc)
	switch sort {
	case defaultSortField:
		executor.SourceOrder = []queryCursorTerm{{expression: "mi.created_at", kind: cursorKindTimestamp, descending: descending, nullsLast: !descending}}
	case querySortReleaseDate:
		// Non-TV sections use the same mixed-item expression as BrowseRepository;
		// movie-only DATE and its ISO text ordering are equivalent.
		expression := "COALESCE(mi.release_date::text, NULLIF(BTRIM(mi.first_air_date), ''))"
		kind := cursorKindText
		if req.Query.MediaScope == playableTypeMovie {
			expression = "mi.release_date"
			kind = cursorKindDate
		}
		executor.SourceOrder = []queryCursorTerm{{expression: expression, kind: kind, descending: descending, nullsLast: true}}
	case querySortRandom:
		// A fresh request may choose another seed. Continuations retain the signed
		// UTC RFC3339Nano seed; no server-local random state is needed.
		executor.SourceWhere = "$1::text IS NOT NULL"
		executor.SourceArgs = []any{snapshot.UTC().Format(time.RFC3339Nano)}
		executor.SourceOrder = []queryCursorTerm{{expression: "md5(mi.content_id || $1::text)", kind: cursorKindText, nullsLast: true}}
	default:
		return nil, fmt.Errorf("%w: unsupported section order %q", ErrInvalidCatalogRequest, sort)
	}
	executor.SourceOrder = append(executor.SourceOrder, queryCursorTerm{expression: cursorContentIDExpression, kind: cursorKindText, nullsLast: true})
	return resolvePersonalExecutorCursor(ctx, executor, req, access, snapshot)
}
