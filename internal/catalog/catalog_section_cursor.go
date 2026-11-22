package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Section cursor ordering matches BrowseRepository, including its historical
// added_at mapping to created_at and content-ID tie breakers. It deliberately
// does not replace section ordering with the generic query sort definitions.
func (r *CatalogResolver) resolveSectionOrderCursor(ctx context.Context, req CatalogRequest, access AccessFilter, snapshot time.Time, sort, order string) (*CatalogResult, error) {
	executor, err := sectionOrderExecutor(r.itemRepo.pool, &req.Query, snapshot, sort, order)
	if err != nil {
		return nil, err
	}
	return resolvePersonalExecutorCursor(ctx, executor, req, access, snapshot)
}

// SectionOrderPreview returns the first limit titles of a recently added
// (sort added_at), recently released (release_date) or random section's See
// All page for def, in that page's order, and whether the page holds more. A
// row whose own query cannot express def's scope, such as an episode scope,
// reads its titles here.
func SectionOrderPreview(ctx context.Context, pool *pgxpool.Pool, def QueryDefinition, access AccessFilter, sort string, limit int) ([]*models.MediaItem, bool, error) {
	executor, err := sectionOrderExecutor(pool, &def, time.Now(), sort, "desc")
	if err != nil {
		return nil, false, err
	}
	// A cursor page holds at most cursorPageMax titles; a longer row reads
	// several.
	var items []*models.MediaItem
	var after *QueryCursor
	for {
		page, err := executor.PreviewCursorPage(ctx, def, access, min(limit-len(items), cursorPageMax), after, false)
		if err != nil {
			return nil, false, err
		}
		items = append(items, page.Items...)
		if !page.HasMore || page.Next == nil || len(items) >= limit {
			return items, page.HasMore, nil
		}
		after = page.Next
	}
}

// sectionOrderExecutor builds the executor that lists query's titles in a
// section's See All order; it resets query's own sort, which the order
// replaces.
func sectionOrderExecutor(pool *pgxpool.Pool, query *QueryDefinition, snapshot time.Time, sort, order string) (*QueryExecutor, error) {
	query.Sort = QuerySort{Field: querySortTitle, Order: querySortAsc}
	executor := newScopeQueryExecutor(pool, query.MediaScope, &snapshot)
	executor.SourceWhere = cursorTruePredicate
	descending := !strings.EqualFold(order, querySortAsc)
	switch sort {
	case defaultSortField:
		executor.SourceOrder = []queryCursorTerm{{expression: "mi.created_at", kind: cursorKindTimestamp, descending: descending, nullsLast: !descending}}
	case querySortReleaseDate:
		// Non-TV sections use the same mixed-item expression as BrowseRepository;
		// movie-only DATE and its ISO text ordering are equivalent.
		expression := ReleaseDateSortSQL("mi")
		kind := cursorKindText
		if query.MediaScope == playableTypeMovie {
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
	return executor, nil
}
