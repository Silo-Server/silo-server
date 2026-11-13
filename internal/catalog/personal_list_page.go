package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// PersonalList names a profile's personal list table in PostgreSQL.
type PersonalList string

// The personal lists ListVisiblePersonalPage reads.
const (
	PersonalListFavorites     PersonalList = "user_favorites"
	PersonalListWatchlist     PersonalList = "user_watchlist"
	PersonalListNotInterested PersonalList = "user_not_interested"
)

// PersonalListRow is one entry of a personal list page. AddedAt is in the
// RFC 3339 form the user store answers, so a key read back from a row
// resumes the page exactly.
type PersonalListRow struct {
	ProfileID   string
	MediaItemID string
	AddedAt     string
}

// ListVisiblePersonalPage answers at most limit entries of the profile's
// list that the viewer may see, ordered by (added_at DESC, media_item_id
// DESC) strictly after the key (nil = from the newest). An entry is visible
// when it names a catalog item the filter admits, or an episode whose
// series the filter admits: the rule the list cards follow. The filter is
// applied in the page query with the predicates EnsureAccessibleIDs uses, so
// one index-ordered read answers a page however many hidden entries come
// before it, and the last row, which a cursor is built from, is always
// visible.
//
// The lists live in the user store; this reads them from the catalog side
// because only a PostgreSQL user store keeps them beside the catalog.
// Callers check userstore.HasCatalogSQLState first.
func (r *ItemRepository) ListVisiblePersonalPage(ctx context.Context, list PersonalList, userID int, profileID string, filter AccessFilter, after *userstore.ListKey, limit int) ([]PersonalListRow, error) {
	// An empty (non-nil) allowlist means "no libraries allowed".
	if filter.AllowedLibraryIDs != nil && len(filter.AllowedLibraryIDs) == 0 {
		return nil, nil
	}
	query, args, err := personalListPageSQL(list, userID, profileID, filter, after, limit)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing visible %s page: %w", list, err)
	}
	defer rows.Close()

	var out []PersonalListRow
	for rows.Next() {
		var row PersonalListRow
		var addedAt time.Time
		if err := rows.Scan(&row.ProfileID, &row.MediaItemID, &addedAt); err != nil {
			return nil, fmt.Errorf("scanning %s row: %w", list, err)
		}
		row.AddedAt = addedAt.UTC().Format(time.RFC3339Nano)
		out = append(out, row)
	}
	return out, rows.Err()
}

func personalListPageSQL(list PersonalList, userID int, profileID string, filter AccessFilter, after *userstore.ListKey, limit int) (string, []any, error) {
	switch list {
	case PersonalListFavorites, PersonalListWatchlist, PersonalListNotInterested:
	default:
		return "", nil, fmt.Errorf("unknown personal list %q", list)
	}
	conditions := []string{"l.user_id = $1", "l.profile_id = $2"}
	args := []any{userID, profileID}
	argIdx := 3
	if after != nil {
		addedAt, err := time.Parse(time.RFC3339Nano, after.AddedAt)
		if err != nil {
			return "", nil, fmt.Errorf("parsing list key: %w", err)
		}
		conditions = append(conditions, fmt.Sprintf("(l.added_at, l.media_item_id) < ($%d::timestamptz, $%d)", argIdx, argIdx+1))
		args = append(args, addedAt, after.MediaItemID)
		argIdx += 2
	}
	// visibleItemSQL is the EnsureAccessibleIDs predicate set over one
	// media_items alias.
	visibleItemSQL := func(alias string) string {
		var conds []string
		appendLibraryAccessConditions(alias+".content_id", filter, &conds, &args, &argIdx)
		applyAccessFilter(alias, AccessFilter{MaturityLimits: filter.MaturityLimits, ExcludedMediaTypes: filter.ExcludedMediaTypes}, &conds, &args, &argIdx)
		if len(conds) == 0 {
			return ""
		}
		return " AND " + strings.Join(conds, " AND ")
	}
	item := visibleItemSQL("mi")
	series := visibleItemSQL("s")
	conditions = append(conditions, fmt.Sprintf(`(EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = l.media_item_id%s)
		  OR EXISTS (SELECT 1 FROM episodes e JOIN media_items s ON s.content_id = e.series_id WHERE e.content_id = l.media_item_id%s))`, item, series))
	args = append(args, limit)
	return fmt.Sprintf(`
		SELECT l.profile_id, l.media_item_id, l.added_at
		FROM %s l
		WHERE %s
		ORDER BY l.added_at DESC, l.media_item_id DESC
		LIMIT $%d`, string(list), strings.Join(conditions, " AND "), argIdx), args, nil
}
