package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// DiscoveryRepository provides catalog query helpers used by discovery section
// recipes (critically_acclaimed, hidden_gems, and similar).
type DiscoveryRepository struct {
	pool *pgxpool.Pool
}

// NewDiscoveryRepository creates a DiscoveryRepository backed by pool.
func NewDiscoveryRepository(pool *pgxpool.Pool) *DiscoveryRepository {
	return &DiscoveryRepository{pool: pool}
}

// RatingFilter controls the ListByRatingThreshold query.
type RatingFilter struct {
	// Min is the minimum rating_imdb value (inclusive). Items with a NULL
	// rating_imdb are excluded.
	Min float64
	// Limit caps the number of rows returned. Zero or negative means no limit.
	Limit int
	// LibraryID, when non-nil, restricts results to items in that library.
	// Takes precedence over LibraryIDs.
	LibraryID *int
	// LibraryIDs, when non-empty, restricts results to items in any of these
	// libraries (multi-library section scope).
	LibraryIDs []int
	// Filter carries viewer-level access constraints (content rating ceiling,
	// allowed/disabled library sets).
	Filter AccessFilter
}

// buildRatingThresholdQuery builds the SQL statement and bind args for ListByRatingThreshold.
// It returns an empty query string when access rules exclude every
// library, signalling the caller to skip the query and return no rows.
func buildRatingThresholdQuery(f RatingFilter) (string, []any) {
	var conditions []string
	var args []any
	argIdx := 1

	// IMDb rating threshold - NULL ratings are excluded implicitly by >=.
	conditions = append(conditions, fmt.Sprintf("mi.rating_imdb >= $%d", argIdx))
	args = append(args, f.Min)
	argIdx++

	if ok := appendDiscoveryLibraryScope(&conditions, &args, &argIdx, f.LibraryID, f.LibraryIDs, f.Filter); !ok {
		return "", nil
	}

	applyAccessFilter("mi", f.Filter, &conditions, &args, &argIdx)

	conditions = append(conditions, MangaChapterExclusionWhere("mi"))

	query := fmt.Sprintf(
		"SELECT %s FROM media_items mi WHERE %s ORDER BY mi.rating_imdb DESC NULLS LAST, mi.content_id ASC",
		qualifiedItemColumns("mi"),
		strings.Join(conditions, " AND "),
	)

	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, f.Limit)
	}

	return query, args
}

// ListByRatingThreshold returns media items whose rating_imdb is >= f.Min,
// ordered by rating_imdb DESC NULLS LAST.  Items without an IMDb rating are
// always excluded.
func (r *DiscoveryRepository) ListByRatingThreshold(ctx context.Context, f RatingFilter) ([]*models.MediaItem, error) {
	query, args := buildRatingThresholdQuery(f)
	if query == "" {
		return []*models.MediaItem{}, nil
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing items by rating threshold: %w", err)
	}
	defer rows.Close()

	items, err := scanItems(rows)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// UnplayedFilter controls the ListUnplayedHighRated query.
type UnplayedFilter struct {
	// MinRating is the minimum rating_imdb value (inclusive).
	MinRating float64
	// MaxPlays is the maximum number of watch-history events the viewer may
	// have for an item before it stops counting as a hidden gem. Zero (the
	// default) keeps the strict "never started" semantics.
	MaxPlays int
	// Limit caps the number of rows returned. Zero or negative means no limit.
	Limit int
	// UserID and ProfileID identify the viewer whose watch history is checked.
	// Both are required; the function returns an error if either is absent.
	UserID    int
	ProfileID string
	// LibraryID, when non-nil, restricts results to items in that library.
	// Takes precedence over LibraryIDs.
	LibraryID *int
	// LibraryIDs, when non-empty, restricts results to items in any of these
	// libraries (multi-library section scope).
	LibraryIDs []int
	// Filter carries viewer-level access constraints.
	Filter AccessFilter
}

// buildUnplayedHighRatedQuery builds the SQL statement and bind args for ListUnplayedHighRated.
// It returns an empty query string when access rules exclude every
// library, signalling the caller to skip the query and return no rows.
func buildUnplayedHighRatedQuery(f UnplayedFilter) (string, []any) {
	var conditions []string
	var args []any
	argIdx := 1

	// IMDb rating threshold.
	conditions = append(conditions, fmt.Sprintf("mi.rating_imdb >= $%d", argIdx))
	args = append(args, f.MinRating)
	argIdx++

	// Items the viewer has watched more than MaxPlays times are excluded.
	// MaxPlays 0 (the default) reduces to the strict "never started" check.
	if f.MaxPlays > 0 {
		conditions = append(conditions, fmt.Sprintf(`(
			SELECT COUNT(*)
			FROM user_watch_history uwh
			WHERE uwh.user_id = $%d
			  AND uwh.profile_id = $%d
			  AND uwh.media_item_id = mi.content_id
		) <= $%d`, argIdx, argIdx+1, argIdx+2))
		args = append(args, f.UserID, f.ProfileID, f.MaxPlays)
		argIdx += 3
	} else {
		conditions = append(conditions, fmt.Sprintf(`NOT EXISTS (
			SELECT 1
			FROM user_watch_history uwh
			WHERE uwh.user_id = $%d
			  AND uwh.profile_id = $%d
			  AND uwh.media_item_id = mi.content_id
		)`, argIdx, argIdx+1))
		args = append(args, f.UserID, f.ProfileID)
		argIdx += 2
	}

	if ok := appendDiscoveryLibraryScope(&conditions, &args, &argIdx, f.LibraryID, f.LibraryIDs, f.Filter); !ok {
		return "", nil
	}

	applyAccessFilter("mi", f.Filter, &conditions, &args, &argIdx)

	conditions = append(conditions, MangaChapterExclusionWhere("mi"))

	query := fmt.Sprintf(
		"SELECT %s FROM media_items mi WHERE %s ORDER BY mi.rating_imdb DESC NULLS LAST, mi.content_id ASC",
		qualifiedItemColumns("mi"),
		strings.Join(conditions, " AND "),
	)

	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, f.Limit)
	}

	return query, args
}

// ListUnplayedHighRated returns high-rated items that the given user/profile has
// never started watching.  "Never started" means no row exists in
// user_watch_history for (user_id, profile_id, media_item_id), regardless of
// completion status.  Items without an IMDb rating are excluded.
//
// Results are ordered by rating_imdb DESC NULLS LAST.
func (r *DiscoveryRepository) ListUnplayedHighRated(ctx context.Context, f UnplayedFilter) ([]*models.MediaItem, error) {
	if f.UserID <= 0 || strings.TrimSpace(f.ProfileID) == "" {
		return nil, fmt.Errorf("ListUnplayedHighRated: UserID and ProfileID are required")
	}

	query, args := buildUnplayedHighRatedQuery(f)
	if query == "" {
		return []*models.MediaItem{}, nil
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing unplayed high-rated items: %w", err)
	}
	defer rows.Close()

	items, err := scanItems(rows)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ForgottenFavoritesFilter controls the ListForgottenFavorites query.
type ForgottenFavoritesFilter struct {
	// LookbackDays is the number of days in the past beyond which a watch
	// event is considered "forgotten".  Items last watched more recently than
	// this threshold are excluded.  Must be > 0.
	LookbackDays int
	// Limit caps the number of rows returned. Zero or negative means no limit.
	Limit int
	// UserID and ProfileID identify the viewer whose watch history is checked.
	// Both are required; the function returns an error if either is absent.
	UserID    int
	ProfileID string
	// LibraryID, when non-nil, restricts results to items in that library.
	// Takes precedence over LibraryIDs.
	LibraryID *int
	// LibraryIDs, when non-empty, restricts results to items in any of these
	// libraries (multi-library section scope).
	LibraryIDs []int
	// Filter carries viewer-level access constraints.
	Filter AccessFilter
}

// buildForgottenFavoritesQuery builds the SQL statement and bind args for ListForgottenFavorites.
// It returns an empty query string when access rules exclude every
// library, signalling the caller to skip the query and return no rows.
func buildForgottenFavoritesQuery(f ForgottenFavoritesFilter) (string, []any) {
	if f.LookbackDays <= 0 {
		f.LookbackDays = 365
	}

	var conditions []string
	var args []any
	argIdx := 1

	// Only items with an IMDb rating of at least 7.0.
	conditions = append(conditions, "mi.rating_imdb >= 7.0")

	// Items the user has never watched, or last watched before the lookback window.
	conditions = append(conditions, fmt.Sprintf(`NOT EXISTS (
		SELECT 1
		FROM user_watch_history uwh
		WHERE uwh.user_id = $%d
		  AND uwh.profile_id = $%d
		  AND uwh.media_item_id = mi.content_id
		  AND uwh.watched_at >= NOW() - ($%d || ' days')::interval
	)`, argIdx, argIdx+1, argIdx+2))
	args = append(args, f.UserID, f.ProfileID, f.LookbackDays)
	argIdx += 3

	if ok := appendDiscoveryLibraryScope(&conditions, &args, &argIdx, f.LibraryID, f.LibraryIDs, f.Filter); !ok {
		return "", nil
	}

	applyAccessFilter("mi", f.Filter, &conditions, &args, &argIdx)

	conditions = append(conditions, MangaChapterExclusionWhere("mi"))

	query := fmt.Sprintf(
		"SELECT %s FROM media_items mi WHERE %s ORDER BY mi.rating_imdb DESC NULLS LAST, mi.content_id ASC",
		qualifiedItemColumns("mi"),
		strings.Join(conditions, " AND "),
	)

	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, f.Limit)
	}

	return query, args
}

// ListForgottenFavorites returns high-rated items (rating_imdb >= 7.0) that the
// user/profile either has never watched OR last watched more than LookbackDays
// ago.  Results are ordered by rating_imdb DESC NULLS LAST.
func (r *DiscoveryRepository) ListForgottenFavorites(ctx context.Context, f ForgottenFavoritesFilter) ([]*models.MediaItem, error) {
	if f.UserID <= 0 || strings.TrimSpace(f.ProfileID) == "" {
		return nil, fmt.Errorf("ListForgottenFavorites: UserID and ProfileID are required")
	}
	query, args := buildForgottenFavoritesQuery(f)
	if query == "" {
		return []*models.MediaItem{}, nil
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing forgotten favorites: %w", err)
	}
	defer rows.Close()

	items, err := scanItems(rows)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func appendDiscoveryLibraryScope(
	conditions *[]string,
	args *[]any,
	argIdx *int,
	libraryID *int,
	libraryIDs []int,
	filter AccessFilter,
) bool {
	// Section-level scope: a single pinned library wins over a multi-library set.
	var scope []int
	switch {
	case libraryID != nil:
		scope = []int{*libraryID}
	case len(libraryIDs) > 0:
		scope = libraryIDs
	}

	// Intersect the section scope with the viewer's allowed set so a scoped
	// section can never widen access beyond what the viewer may see.
	allowed := filter.AllowedLibraryIDs
	if len(scope) > 0 {
		if allowed != nil {
			allowed = intersectInts(scope, allowed)
			if len(allowed) == 0 {
				return false
			}
		} else {
			allowed = scope
		}
	} else if allowed != nil && len(allowed) == 0 {
		return false
	}

	// buildLibraryScopeJoin uses semi-joins: disabled libraries are an item-level
	// exclusion, matching the rest of catalog access filtering and avoiding row fanout.
	if len(allowed) == 0 && len(filter.DisabledLibraryIDs) > 0 {
		// In deny-only mode, require at least one library membership. Without this,
		// stale or provisional media_items rows with no membership pass NOT EXISTS.
		*conditions = append(*conditions,
			"EXISTS (SELECT 1 FROM media_item_libraries mil_scope_any WHERE mil_scope_any.content_id = mi.content_id)",
		)
	}

	whereSQL, scopeArgs, ok := buildLibraryScopeJoin(
		allowed,
		filter.DisabledLibraryIDs,
		*argIdx,
		"",
		"mi.content_id",
	)
	if ok {
		*conditions = append(*conditions, whereSQL)
		*args = append(*args, scopeArgs...)
		*argIdx += len(scopeArgs)
	}
	return true
}
