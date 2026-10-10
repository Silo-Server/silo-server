package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
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
	// Min is the minimum TMDB vote average (inclusive).
	Min float64
	// MinVotes is the minimum TMDB vote count; below 1 counts as 1.
	MinVotes int
	// Types, when non-empty, limits results to these media types.
	Types []string
	// GenresAny, when non-empty, requires at least one of these genres.
	GenresAny []string
	// MaxRuntime, when positive, keeps titles with a known runtime of at most
	// this many minutes.
	MaxRuntime int
	// Limit caps the number of rows returned. Zero or negative means no limit.
	Limit int
	// LibraryID, when non-nil, restricts results to items in that library.
	// Takes precedence over LibraryIDs.
	LibraryID *int
	// LibraryIDs, when non-nil, restricts results to items in any of these
	// libraries (multi-library section scope); an empty set matches nothing.
	LibraryIDs []int
	// Filter carries viewer-level access constraints (content rating ceiling,
	// allowed/disabled library sets).
	Filter AccessFilter
}

// DiscoveryRatingOrder is the ORDER BY list for discovery rows over alias mi:
// vote-weighted TMDB rating, matching idx_media_items_tmdb_weighted_rating
// (see TMDBWeightedRatingSQL).
var DiscoveryRatingOrder = TMDBWeightedRatingSQL("mi") + " DESC NULLS LAST, mi.content_id ASC"

// RatedOrder is the ORDER BY list over alias mi for rows that sort by rating
// without requiring a vote count (format showcases, anniversaries, seasonal
// picks): vote-weighted TMDB rating first, then titles with no known count by
// their TMDB and then IMDb rating, so a perfect score from a few votes never
// leads.
var RatedOrder = TMDBWeightedRatingSQL("mi") + " DESC NULLS LAST, mi.rating_tmdb DESC NULLS LAST, mi.rating_imdb DESC NULLS LAST, mi.content_id ASC"

// AppendTMDBRatingFloor requires, for alias mi, a TMDB vote average of at
// least minRating from at least minVotes votes. minVotes below 1 counts as 1,
// so an item whose count is unknown never qualifies on a rating a handful of
// people gave.
func AppendTMDBRatingFloor(conditions *[]string, args *[]any, argIdx *int, minRating float64, minVotes int) {
	minVotes = max(minVotes, 1)
	*conditions = append(*conditions,
		fmt.Sprintf("mi.tmdb_vote_average >= $%d", *argIdx),
		fmt.Sprintf("mi.tmdb_vote_count >= $%d", *argIdx+1),
	)
	*args = append(*args, minRating, minVotes)
	*argIdx += 2
}

// AppendContentScope narrows a query over alias mi to filter's content
// allow-list and name prefix, the content-level limits applyAccessFilter
// leaves out, matching them the way the query executor does: a non-nil empty
// allow-list matches nothing, and the prefix matches the sort-title key.
func AppendContentScope(conditions *[]string, args *[]any, argIdx *int, filter AccessFilter) {
	appendAllowedContentCondition("mi.content_id", filter.AllowedContentIDs, conditions, args, argIdx)
	if prefix := strings.TrimSpace(filter.NamePrefix); prefix != "" {
		*conditions = append(*conditions, sortTitlePrefixCondition(*argIdx))
		*args = append(*args, escapePrefixForLike(prefix)+"%")
		*argIdx++
	}
}

// buildRatingThresholdQuery builds the SQL statement and bind args for ListByRatingThreshold.
// It returns an empty query string when access rules exclude every
// library, signalling the caller to skip the query and return no rows.
func buildRatingThresholdQuery(f RatingFilter) (string, []any) {
	var conditions []string
	var args []any
	argIdx := 1

	AppendTMDBRatingFloor(&conditions, &args, &argIdx, f.Min, f.MinVotes)
	if len(f.Types) > 0 {
		conditions = append(conditions, fmt.Sprintf("mi.type = ANY($%d)", argIdx))
		args = append(args, f.Types)
		argIdx++
	}
	if len(f.GenresAny) > 0 {
		conditions = append(conditions, fmt.Sprintf("mi.genres && $%d::text[]", argIdx))
		args = append(args, f.GenresAny)
		argIdx++
	}
	if f.MaxRuntime > 0 {
		conditions = append(conditions, fmt.Sprintf("mi.runtime > 0 AND mi.runtime <= $%d", argIdx))
		args = append(args, f.MaxRuntime)
		argIdx++
	}

	if ok := appendDiscoveryLibraryScope(&conditions, &args, &argIdx, f.LibraryID, f.LibraryIDs, f.Filter); !ok {
		return "", nil
	}

	applyAccessFilter("mi", f.Filter, &conditions, &args, &argIdx)
	AppendContentScope(&conditions, &args, &argIdx, f.Filter)

	conditions = append(conditions, MangaChapterExclusionWhere("mi"))

	query := fmt.Sprintf(
		"SELECT %s FROM media_items mi WHERE %s ORDER BY "+DiscoveryRatingOrder,
		qualifiedItemColumns("mi"),
		strings.Join(conditions, " AND "),
	)

	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, f.Limit)
	}

	return query, args
}

// ListByRatingThreshold returns media items whose TMDB vote average is at
// least f.Min from at least f.MinVotes votes, narrowed by f.Types, f.GenresAny
// and f.MaxRuntime, ordered by vote-weighted TMDB rating. Items without a
// known vote count are always excluded.
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

	return scanDiscoveryItems(rows)
}

// UnplayedFilter controls the ListUnplayedHighRated query.
type UnplayedFilter struct {
	// MinRating is the minimum TMDB vote average (inclusive).
	MinRating float64
	// MinVotes is the minimum TMDB vote count; below 1 counts as 1.
	MinVotes int
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
	// LibraryIDs, when non-nil, restricts results to items in any of these
	// libraries (multi-library section scope); an empty set matches nothing.
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

	AppendTMDBRatingFloor(&conditions, &args, &argIdx, f.MinRating, f.MinVotes)

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
	AppendContentScope(&conditions, &args, &argIdx, f.Filter)

	conditions = append(conditions, MangaChapterExclusionWhere("mi"))

	query := fmt.Sprintf(
		"SELECT %s FROM media_items mi WHERE %s ORDER BY "+DiscoveryRatingOrder,
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
// completion status.  Items without a known TMDB vote count are excluded.
//
// Results are ordered by vote-weighted TMDB rating.
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

	return scanDiscoveryItems(rows)
}

// ForgottenFavoritesFilter controls the ListForgottenFavorites query.
type ForgottenFavoritesFilter struct {
	// LookbackDays is the number of days in the past beyond which a watch
	// event is considered "forgotten".  Items last watched more recently than
	// this threshold are excluded.  Must be > 0.
	LookbackDays int
	// MinVotes is the minimum TMDB vote count; below 1 counts as 1.
	MinVotes int
	// Limit caps the number of rows returned. Zero or negative means no limit.
	Limit int
	// UserID and ProfileID identify the viewer whose watch history is checked.
	// Both are required; the function returns an error if either is absent.
	UserID    int
	ProfileID string
	// LibraryID, when non-nil, restricts results to items in that library.
	// Takes precedence over LibraryIDs.
	LibraryID *int
	// LibraryIDs, when non-nil, restricts results to items in any of these
	// libraries (multi-library section scope); an empty set matches nothing.
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

	AppendTMDBRatingFloor(&conditions, &args, &argIdx, 7.0, f.MinVotes)

	// Items the user has never watched, or last watched before the lookback window.
	conditions = append(conditions, fmt.Sprintf(`NOT EXISTS (
		SELECT 1
		FROM user_watch_history uwh
		WHERE uwh.user_id = $%d
		  AND uwh.profile_id = $%d
		  AND uwh.media_item_id = mi.content_id
		  AND uwh.watched_at >= NOW() - make_interval(days => $%d)
	)`, argIdx, argIdx+1, argIdx+2))
	args = append(args, f.UserID, f.ProfileID, f.LookbackDays)
	argIdx += 3

	if ok := appendDiscoveryLibraryScope(&conditions, &args, &argIdx, f.LibraryID, f.LibraryIDs, f.Filter); !ok {
		return "", nil
	}

	applyAccessFilter("mi", f.Filter, &conditions, &args, &argIdx)
	AppendContentScope(&conditions, &args, &argIdx, f.Filter)

	conditions = append(conditions, MangaChapterExclusionWhere("mi"))

	query := fmt.Sprintf(
		"SELECT %s FROM media_items mi WHERE %s ORDER BY "+DiscoveryRatingOrder,
		qualifiedItemColumns("mi"),
		strings.Join(conditions, " AND "),
	)

	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, f.Limit)
	}

	return query, args
}

// ListForgottenFavorites returns high-rated items (a TMDB vote average of 7.0+
// from at least MinVotes votes) that the user/profile either has never watched
// OR last watched more than LookbackDays ago.  Results are ordered by
// vote-weighted TMDB rating.
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

	return scanDiscoveryItems(rows)
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
	case libraryIDs != nil:
		// An explicit empty set scopes the section to no library, as the
		// section fetcher's own library scope does.
		if len(libraryIDs) == 0 {
			return false
		}
		scope = libraryIDs
	}

	// Limit the section scope to the viewer's access so a scoped section can
	// never widen it beyond what the viewer may see.
	allowed, none := filter.LibraryScope(scope)
	if none {
		return false
	}

	// buildLibraryScopeJoin uses semi-joins: disabled libraries are an item-level
	// exclusion, matching the rest of catalog access filtering and avoiding row fanout.
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

// scanDiscoveryItems scans discovery rows and, like the query executor's
// preview path, falls back to created_at for added_at so section responses
// carry it.
func scanDiscoveryItems(rows pgx.Rows) ([]*models.MediaItem, error) {
	items, err := scanItems(rows)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.AddedAt == nil && !item.CreatedAt.IsZero() {
			added := item.CreatedAt
			item.AddedAt = &added
		}
	}
	return items, nil
}
