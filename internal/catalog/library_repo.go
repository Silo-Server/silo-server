package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pathscope"
)

// LibraryItemRepository provides CRUD operations for the media_item_libraries
// junction table.
type LibraryItemRepository struct {
	pool *pgxpool.Pool
}

// NewLibraryItemRepository creates a new LibraryItemRepository backed by the
// given pool.
func NewLibraryItemRepository(pool *pgxpool.Pool) *LibraryItemRepository {
	return &LibraryItemRepository{pool: pool}
}

// libraryItemColumns is the list of columns returned by all SELECT queries on
// media_item_libraries.
const libraryItemColumns = `content_id, media_folder_id, first_seen_at`

// scanLibraryItem scans a single row into a *models.MediaItemLibrary.
func scanLibraryItem(row pgx.Row) (*models.MediaItemLibrary, error) {
	var lib models.MediaItemLibrary
	err := row.Scan(
		&lib.ContentID,
		&lib.MediaFolderID,
		&lib.FirstSeenAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scanning media item library: %w", err)
	}
	return &lib, nil
}

// scanLibraryItems scans multiple rows into a []*models.MediaItemLibrary slice.
func scanLibraryItems(rows pgx.Rows) ([]*models.MediaItemLibrary, error) {
	var items []*models.MediaItemLibrary
	for rows.Next() {
		var lib models.MediaItemLibrary
		err := rows.Scan(
			&lib.ContentID,
			&lib.MediaFolderID,
			&lib.FirstSeenAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning media item library row: %w", err)
		}
		items = append(items, &lib)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating media item library rows: %w", err)
	}
	return items, nil
}

// Upsert inserts a junction record linking a media item to a media folder.
// If the record already exists (same content_id and media_folder_id), the
// operation is a no-op via ON CONFLICT DO NOTHING.
func (r *LibraryItemRepository) Upsert(ctx context.Context, contentID string, folderID int, firstSeenAt time.Time) error {
	query := `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (content_id, media_folder_id) DO NOTHING`

	_, err := r.pool.Exec(ctx, query, contentID, folderID, firstSeenAt)
	if err != nil {
		return fmt.Errorf("upserting media item library: %w", err)
	}

	return nil
}

// GetByItem returns all library junction records for a given content ID.
func (r *LibraryItemRepository) GetByItem(ctx context.Context, contentID string) ([]*models.MediaItemLibrary, error) {
	query := `SELECT ` + libraryItemColumns + `
		FROM media_item_libraries
		WHERE content_id = $1
		ORDER BY media_folder_id ASC`

	rows, err := r.pool.Query(ctx, query, contentID)
	if err != nil {
		return nil, fmt.Errorf("getting library items by content: %w", err)
	}
	defer rows.Close()

	return scanLibraryItems(rows)
}

// GetByFolder returns all library junction records for a given media folder ID.
func (r *LibraryItemRepository) GetByFolder(ctx context.Context, folderID int) ([]*models.MediaItemLibrary, error) {
	query := `SELECT ` + libraryItemColumns + `
		FROM media_item_libraries
		WHERE media_folder_id = $1
		ORDER BY first_seen_at DESC`

	rows, err := r.pool.Query(ctx, query, folderID)
	if err != nil {
		return nil, fmt.Errorf("getting library items by folder: %w", err)
	}
	defer rows.Close()

	return scanLibraryItems(rows)
}

// GetItemsInFolder returns a membership map for the provided content IDs within
// a single library folder.
func (r *LibraryItemRepository) GetItemsInFolder(ctx context.Context, contentIDs []string, folderID int) (map[string]bool, error) {
	result := make(map[string]bool, len(contentIDs))
	if len(contentIDs) == 0 {
		return result, nil
	}

	rows, err := r.pool.Query(ctx,
		`SELECT req.content_id
		FROM unnest($2::text[]) AS req(content_id)
		WHERE EXISTS (
			SELECT 1
			FROM media_item_libraries mil
			WHERE mil.media_folder_id = $1
			  AND mil.content_id = req.content_id
		)
		OR EXISTS (
			SELECT 1
			FROM episode_libraries el
			WHERE el.media_folder_id = $1
			  AND el.episode_id = req.content_id
		)`,
		folderID, contentIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("getting folder membership for items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning folder membership row: %w", err)
		}
		result[contentID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating folder membership rows: %w", err)
	}

	return result, nil
}

// GetItemsInFolders returns membership for the provided content IDs across
// any of the supplied folders. Used by the user-collection sync service to
// constrain imports to a chosen subset of libraries in one query rather than
// looping GetItemsInFolder per library.
func (r *LibraryItemRepository) GetItemsInFolders(ctx context.Context, contentIDs []string, folderIDs []int) (map[string]bool, error) {
	result := make(map[string]bool, len(contentIDs))
	if len(contentIDs) == 0 || len(folderIDs) == 0 {
		return result, nil
	}

	rows, err := r.pool.Query(ctx,
		`SELECT req.content_id
		FROM unnest($2::text[]) AS req(content_id)
		WHERE EXISTS (
			SELECT 1
			FROM media_item_libraries mil
			WHERE mil.media_folder_id = ANY($1)
			  AND mil.content_id = req.content_id
		)
		OR EXISTS (
			SELECT 1
			FROM episode_libraries el
			WHERE el.media_folder_id = ANY($1)
			  AND el.episode_id = req.content_id
		)`,
		folderIDs, contentIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("getting multi-folder membership for items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning multi-folder membership row: %w", err)
		}
		result[contentID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating multi-folder membership rows: %w", err)
	}
	return result, nil
}

// FilterAccessibleContentIDs returns the subset of contentIDs that pass the
// viewer's access scope, checking library membership and the content-rating
// ceiling. It is the batched, set-oriented mirror of the per-item predicate the
// detail/watch path enforces (ItemRepository.EnsureAccessible +
// applyAccessFilter), so list endpoints (e.g. continue-watching) can drop
// out-of-scope items in one query instead of letting the client discover them
// via per-item 404s.
//
// Semantics match EnsureAccessible exactly:
//   - A media item (movie/series) is accessible when its media_items row
//     satisfies the rating ceiling and — when the viewer is library-restricted
//     — it has a media_item_libraries membership in a permitted folder (within
//     allowedFolderIDs when that slice is non-nil, and not in disabledFolderIDs).
//   - An episode is gated on its PARENT SERIES, mirroring how the detail/watch
//     path resolves an episode id to episode.SeriesID and calls
//     EnsureAccessible(series_id) (catalog.DetailService.GetItemDetail): series
//     media_item_libraries membership + series content_rating. It deliberately
//     does NOT key off episode_libraries — that membership can diverge from the
//     series for multi-folder shows, which would re-introduce the dead-tile /
//     leak this filter exists to prevent.
//   - When the viewer has no library restriction, membership is not required
//     (matching EnsureAccessible, which only joins media_item_libraries when a
//     library restriction is set, so a rating-only viewer is gated on rating
//     alone).
//
// A non-nil but empty allowedFolderIDs, or a maxContentRating that permits no
// ratings, means nothing is accessible. ExcludedMediaTypes is intentionally
// omitted: the viewer access.Scope does not carry it and the native request
// path never sets it (only the jellycompat layer populates it), so it is a
// no-op here.
//
// The emitted SQL is built by buildFilterAccessibleContentIDsSQL so its shape
// (placeholder numbering, the parent-series join for episodes, the optional
// rating predicate) is unit-testable without a database.
func (r *LibraryItemRepository) FilterAccessibleContentIDs(ctx context.Context, contentIDs []string, allowedFolderIDs, disabledFolderIDs []int, maxContentRating string) (map[string]bool, error) {
	result := make(map[string]bool, len(contentIDs))
	if len(contentIDs) == 0 {
		return result, nil
	}
	if allowedFolderIDs != nil && len(allowedFolderIDs) == 0 {
		// Library-restricted to nothing → nothing is accessible.
		return result, nil
	}

	var allowedRatings []string
	if maxContentRating != "" {
		allowedRatings = access.AllowedRatingsUpTo(maxContentRating)
		if len(allowedRatings) == 0 {
			// Ceiling permits no ratings → nothing is accessible.
			return result, nil
		}
	}

	query, args := buildFilterAccessibleContentIDsSQL(contentIDs, allowedFolderIDs, disabledFolderIDs, allowedRatings)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("filtering accessible content ids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning accessible content id row: %w", err)
		}
		result[contentID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating accessible content id rows: %w", err)
	}
	return result, nil
}

// buildFilterAccessibleContentIDsSQL builds the membership/rating query used by
// FilterAccessibleContentIDs. It is a pure function (no DB access) so the query
// shape can be unit-tested. allowedRatings must already be resolved via
// access.AllowedRatingsUpTo (nil/empty means no rating ceiling); the caller
// handles the "permits nothing" early-outs.
//
// The structure mirrors ItemRepository.EnsureAccessible: select FROM the owning
// media_items row, gate library membership through the shared per-item
// EXISTS / NOT EXISTS predicates (libraryAccessConditions), and resolve
// episodes through their parent series so an episode is gated on
// EnsureAccessible(series_id)-equivalent membership.
func buildFilterAccessibleContentIDsSQL(contentIDs []string, allowedFolderIDs, disabledFolderIDs []int, allowedRatings []string) (string, []any) {
	args := []any{contentIDs}
	var allowedIdx, disabledIdx, ratingIdx int
	if allowedFolderIDs != nil {
		args = append(args, allowedFolderIDs)
		allowedIdx = len(args)
	}
	if len(disabledFolderIDs) > 0 {
		args = append(args, disabledFolderIDs)
		disabledIdx = len(args)
	}
	if len(allowedRatings) > 0 {
		args = append(args, allowedRatings)
		ratingIdx = len(args)
	}

	// Item branch gates the media item directly; episode branch resolves the
	// parent series and gates on it (mirroring EnsureAccessible(series_id)).
	// Both branches share the same placeholder indexes.
	itemFrom := "media_items mi"
	episodeFrom := "episodes e JOIN media_items mi ON mi.content_id = e.series_id"
	itemConds := []string{"mi.content_id = req.content_id"}
	episodeConds := []string{"e.content_id = req.content_id"}

	itemConds = append(itemConds, libraryAccessConditions("mi.content_id", allowedIdx, disabledIdx)...)
	episodeConds = append(episodeConds, libraryAccessConditions("e.series_id", allowedIdx, disabledIdx)...)
	if ratingIdx > 0 {
		rc := fmt.Sprintf("mi.content_rating = ANY($%d)", ratingIdx)
		itemConds = append(itemConds, rc)
		episodeConds = append(episodeConds, rc)
	}

	query := fmt.Sprintf(`
		SELECT req.content_id
		FROM unnest($1::text[]) AS req(content_id)
		WHERE EXISTS (
			SELECT 1
			FROM %s
			WHERE %s
		)
		OR EXISTS (
			SELECT 1
			FROM %s
			WHERE %s
		)`,
		itemFrom, strings.Join(itemConds, " AND "),
		episodeFrom, strings.Join(episodeConds, " AND "),
	)
	return query, args
}

func (r *LibraryItemRepository) GetFolderIDsForItem(ctx context.Context, contentID string) ([]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT media_folder_id
		FROM media_item_libraries
		WHERE content_id = $1
		ORDER BY media_folder_id ASC
	`, contentID)
	if err != nil {
		return nil, fmt.Errorf("getting folder IDs for item: %w", err)
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning folder ID for item: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating folder IDs for item: %w", err)
	}
	return ids, nil
}

func (r *LibraryItemRepository) CountFoldersForItem(ctx context.Context, contentID string) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM media_item_libraries
		WHERE content_id = $1
	`, contentID).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting folders for item: %w", err)
	}
	return count, nil
}

func (r *LibraryItemRepository) GetDistinctMetadataLanguagesForItem(ctx context.Context, contentID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT COALESCE(NULLIF(mf.metadata_language, ''), 'en') AS language
		FROM media_item_libraries mil
		JOIN media_folders mf ON mf.id = mil.media_folder_id
		WHERE mil.content_id = $1
		ORDER BY language ASC
	`, contentID)
	if err != nil {
		return nil, fmt.Errorf("getting metadata languages for item: %w", err)
	}
	defer rows.Close()

	var languages []string
	for rows.Next() {
		var language string
		if err := rows.Scan(&language); err != nil {
			return nil, fmt.Errorf("scanning metadata language for item: %w", err)
		}
		languages = append(languages, language)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating metadata languages for item: %w", err)
	}
	return slices.Compact(languages), nil
}

// Delete removes a junction record linking a media item to a media folder.
func (r *LibraryItemRepository) Delete(ctx context.Context, contentID string, folderID int) error {
	_, err := r.pool.Exec(ctx,
		"DELETE FROM media_item_libraries WHERE content_id = $1 AND media_folder_id = $2",
		contentID, folderID,
	)
	if err != nil {
		return fmt.Errorf("deleting media item library: %w", err)
	}

	return nil
}

// ReconcileFolderMembership removes library memberships for content that no
// longer has any non-missing files in the given folder. It also deletes orphaned
// media items once they no longer belong to any library. Returns removed
// membership count, deleted item count, orphaned S3 image dirs, and any error.
//
// protectedPathPrefixes lists library roots that are currently unreachable
// (dead drive, lost mount). Membership removal proceeds regardless — browse
// and home queries hide items via media_item_libraries, so removal is what
// keeps a title with no playable files out of the catalog — but items whose
// files in this folder sit under a protected prefix are exempt from the
// orphan delete. Deleting them is not losslessly recoverable (user
// collections cascade via library_collection_items, manual metadata edits and
// cached artwork are lost), whereas a hidden membership-less item restores
// automatically: when the root returns, the scanner clears missing_since on
// its surviving media_files rows and syncPresentLibraryState re-inserts the
// membership from those rows.
func (r *LibraryItemRepository) ReconcileFolderMembership(ctx context.Context, folderID int, protectedPathPrefixes []string) (int, int, []string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("beginning membership reconciliation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Manga series items (type='manga') are virtual parents with no media_file of
	// their own — their membership is keyed to having chapters, not files. Exclude
	// them here so file-presence reconciliation never sweeps a live series; orphan
	// series (no remaining chapters) are cleaned up separately by the manga scan.
	rows, err := tx.Query(ctx, `
		DELETE FROM media_item_libraries mil
		WHERE mil.media_folder_id = $1
		  AND NOT EXISTS (
			SELECT 1
			FROM media_files mf
			WHERE mf.media_folder_id = mil.media_folder_id
			  AND mf.content_id = mil.content_id
			  AND mf.missing_since IS NULL
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM media_items mi
			WHERE mi.content_id = mil.content_id
			  AND mi.type = 'manga'
		  )
		RETURNING mil.content_id
	`, folderID)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("deleting stale folder memberships: %w", err)
	}
	defer rows.Close()

	removedContentIDs := make([]string, 0)
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return 0, 0, nil, fmt.Errorf("scanning removed folder membership: %w", err)
		}
		removedContentIDs = append(removedContentIDs, contentID)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, nil, fmt.Errorf("iterating removed folder memberships: %w", err)
	}
	rows.Close()

	deletedItems := 0
	var orphanedImageDirs []string
	// Find both newly orphaned items and items preserved by an earlier
	// protected-root pass. The latter no longer have a membership to return from
	// the DELETE above, but their surviving media_files row still ties them to
	// this folder so they can be reconsidered after the root recovers.
	orphanIDs, err := collectOrphanIDs(ctx, tx, removedContentIDs)
	if err != nil {
		return 0, 0, nil, err
	}
	previouslyProtected, err := collectFolderFileOrphanIDs(ctx, tx, folderID)
	if err != nil {
		return 0, 0, nil, err
	}
	orphanIDs = appendUniqueStrings(orphanIDs, previouslyProtected...)
	if len(orphanIDs) > 0 {

		// Exempt orphans whose files sit under an unreachable root: the files
		// still exist, the root is just offline. See the doc comment above.
		if len(orphanIDs) > 0 && len(protectedPathPrefixes) > 0 {
			orphanIDs, err = excludeOrphansUnderProtectedPrefixes(ctx, tx, orphanIDs, folderID, protectedPathPrefixes)
			if err != nil {
				return 0, 0, nil, err
			}
		}

		// Collect image paths before deletion.
		if len(orphanIDs) > 0 {
			orphanedImageDirs, err = collectImageDirs(ctx, tx, orphanIDs)
			if err != nil {
				return 0, 0, nil, err
			}
		}

		if len(orphanIDs) > 0 {
			rows, err := tx.Query(ctx, `
				DELETE FROM media_items mi
				WHERE mi.content_id = ANY($1)
				  AND NOT EXISTS (
					SELECT 1
					FROM media_item_libraries mil
					WHERE mil.content_id = mi.content_id
				  )
				RETURNING mi.content_id
			`, orphanIDs)
			if err != nil {
				return 0, 0, nil, fmt.Errorf("deleting orphaned media items after folder reconciliation: %w", err)
			}
			deletedContentIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return 0, 0, nil, fmt.Errorf("collecting deleted orphaned media item IDs: %w", err)
			}
			deletedItems = len(deletedContentIDs)
			if err := EnqueueSearchIndexDeletes(ctx, tx, deletedContentIDs); err != nil {
				return 0, 0, nil, fmt.Errorf("enqueueing catalog search orphan deletes: %w", err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, nil, fmt.Errorf("committing membership reconciliation transaction: %w", err)
	}

	return len(removedContentIDs), deletedItems, orphanedImageDirs, nil
}

func collectFolderFileOrphanIDs(ctx context.Context, tx pgx.Tx, folderID int) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT mf.content_id
		FROM media_files mf
		WHERE mf.media_folder_id = $1
		  AND mf.content_id IS NOT NULL
		  AND mf.content_id <> ''
		  AND NOT EXISTS (
			SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mf.content_id
		  )
	`, folderID)
	if err != nil {
		return nil, fmt.Errorf("finding previously protected folder orphans: %w", err)
	}
	defer rows.Close()
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("collecting previously protected folder orphans: %w", err)
	}
	return ids, nil
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

// excludeOrphansUnderProtectedPrefixes returns the subset of orphanIDs that
// have no media_files row in the folder at or under any protected prefix.
// Matching uses the exact-path + escaped prefix-LIKE shape shared with the
// scanner's root matching, so a sibling root that merely shares a string
// prefix (/mnt/movies2 vs /mnt/movies) is never protected by accident.
//
// Known limitation: the check is scoped to the reconciled folder's own files
// (mf.media_folder_id = folderID). An item shared across two libraries whose
// only surviving files sit under ANOTHER folder's currently-unreachable root
// is not exempted here — if its membership in this folder is genuinely
// removed while the other folder's root is offline, the item is purged even
// though its files under the dead root would have resurrected. Closing this
// would require probing every enabled folder's roots (or persisting per-
// folder unreachable state) on each reconcile; accepted for now given how
// narrow the window is.
func excludeOrphansUnderProtectedPrefixes(ctx context.Context, tx pgx.Tx, orphanIDs []string, folderID int, prefixes []string) ([]string, error) {
	conds, condArgs := pathscope.CoverageClauses("mf.file_path", prefixes, 3)
	args := append([]any{orphanIDs, folderID}, condArgs...)

	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT cid FROM unnest($1::text[]) AS cid
		WHERE NOT EXISTS (
			SELECT 1
			FROM media_files mf
			WHERE mf.media_folder_id = $2
			  AND mf.content_id = cid
			  AND (%s)
		)
	`, strings.Join(conds, " OR ")), args...)
	if err != nil {
		return nil, fmt.Errorf("filtering orphans under protected prefixes: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0, len(orphanIDs))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning unprotected orphan id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
