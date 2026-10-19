package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrExtraNotFound is returned when a media_extras row does not exist.
var ErrExtraNotFound = errors.New("media extra not found")

// ExtraRepository persists scanner-discovered local extras (media_extras).
// Extras are child entities of a movie/series item, playable via their own
// content_id through GetWatchDetail's fallback chain.
type ExtraRepository struct {
	pool *pgxpool.Pool
}

// NewExtraRepository creates an extra repository backed by the given pool.
func NewExtraRepository(pool *pgxpool.Pool) *ExtraRepository {
	return &ExtraRepository{pool: pool}
}

// Upsert inserts or refreshes an extra. The content_id is deterministic
// (contentid.ForLocal of the backing file path), so rescans converge on the
// same row; kind/title/parent follow the latest scan classification. The
// still columns are left alone; Upsert returns the row's current
// still_source so the scanner can tell whether its sidecar changed.
func (r *ExtraRepository) Upsert(ctx context.Context, extra models.MediaExtra) (string, error) {
	if strings.TrimSpace(extra.ContentID) == "" {
		return "", errors.New("extra content_id is required")
	}
	if strings.TrimSpace(extra.ParentID) == "" {
		return "", errors.New("extra parent_id is required")
	}
	var stillSource string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO media_extras (content_id, parent_id, kind, title, sort_order)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (content_id) DO UPDATE SET
			parent_id = EXCLUDED.parent_id,
			kind = EXCLUDED.kind,
			title = EXCLUDED.title,
			sort_order = EXCLUDED.sort_order,
			updated_at = now()
		RETURNING still_source`,
		extra.ContentID, extra.ParentID, string(extra.Kind), extra.Title, extra.SortOrder).
		Scan(&stillSource)
	if err != nil {
		return "", fmt.Errorf("upsert media extra: %w", err)
	}
	return stillSource, nil
}

// SetStill records an extra's cached card image and the source it came
// from. Empty values clear it.
func (r *ExtraRepository) SetStill(ctx context.Context, contentID, path, source, thumbhash string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE media_extras
		SET still_path = $2, still_source = $3, still_thumbhash = $4, updated_at = now()
		WHERE content_id = $1`,
		contentID, path, source, thumbhash)
	if err != nil {
		return fmt.Errorf("set media extra still: %w", err)
	}
	return nil
}

// ClearEmptiedExtraStillsCTE follows a "deleted" CTE that deletes media_files
// rows RETURNING id, extra_id. It clears the image of each local extra those
// rows leave without any file, so the artwork revision GC collects it; the
// media_extras row itself is kept. A file that is only marked missing still
// counts, so an offline share keeps its extras' images.
const ClearEmptiedExtraStillsCTE = `,
	emptied_extras AS (
		UPDATE media_extras e
		SET still_path = '', still_source = '', still_thumbhash = '', updated_at = now()
		WHERE e.content_id IN (SELECT extra_id FROM deleted WHERE extra_id IS NOT NULL)
		  AND (e.still_path <> '' OR e.still_source <> '')
		  AND NOT EXISTS (
			SELECT 1 FROM media_files f
			WHERE f.extra_id = e.content_id AND f.id NOT IN (SELECT id FROM deleted)
		  )
	)`

// GetByID returns a single extra, or ErrExtraNotFound.
func (r *ExtraRepository) GetByID(ctx context.Context, contentID string) (*models.MediaExtra, error) {
	var extra models.MediaExtra
	var kind string
	err := r.pool.QueryRow(ctx, `
		SELECT content_id, parent_id, kind, title, sort_order,
			still_path, still_source, still_thumbhash
		FROM media_extras
		WHERE content_id = $1`, contentID).
		Scan(&extra.ContentID, &extra.ParentID, &kind, &extra.Title, &extra.SortOrder,
			&extra.StillPath, &extra.StillSource, &extra.StillThumbhash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrExtraNotFound
		}
		return nil, fmt.Errorf("query media extra: %w", err)
	}
	extra.Kind = models.ExtraKind(kind)
	return &extra, nil
}

// ExtraWithFile pairs an extra with summary fields of its live backing file
// for detail listings.
type ExtraWithFile struct {
	models.MediaExtra
	FileID   int
	Duration int // seconds
}

// ListWithFilesByParentID returns the parent's extras that have at least one
// live (non-missing) backing file, with per-extra file summary data.
func (r *ExtraRepository) ListWithFilesByParentID(ctx context.Context, parentID string) ([]ExtraWithFile, error) {
	result, err := r.ListWithFilesByParentIDs(ctx, []string{parentID})
	if err != nil {
		return nil, err
	}
	return result[parentID], nil
}

// ListWithFilesByParentIDs is the batch form of ListWithFilesByParentID,
// keyed by parent content_id. Parents without live extras are absent.
func (r *ExtraRepository) ListWithFilesByParentIDs(ctx context.Context, parentIDs []string) (map[string][]ExtraWithFile, error) {
	if len(parentIDs) == 0 {
		return map[string][]ExtraWithFile{}, nil
	}
	// DISTINCT ON keeps one live file per extra (an extra is 1:1 with a file
	// in practice; duplicates would only appear transiently mid-rescan).
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (e.content_id)
			e.content_id, e.parent_id, e.kind, e.title, e.sort_order,
			e.still_path, e.still_thumbhash,
			f.id, COALESCE(f.duration, 0)
		FROM media_extras e
		JOIN media_files f ON f.extra_id = e.content_id AND f.missing_since IS NULL
		WHERE e.parent_id = ANY($1)
		ORDER BY e.content_id, f.id`, parentIDs)
	if err != nil {
		return nil, fmt.Errorf("query media extras with files: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]ExtraWithFile, len(parentIDs))
	for rows.Next() {
		var e ExtraWithFile
		var kind string
		if err := rows.Scan(&e.ContentID, &e.ParentID, &kind, &e.Title, &e.SortOrder,
			&e.StillPath, &e.StillThumbhash, &e.FileID, &e.Duration); err != nil {
			return nil, fmt.Errorf("scan media extra: %w", err)
		}
		e.Kind = models.ExtraKind(kind)
		result[e.ParentID] = append(result[e.ParentID], e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate media extras: %w", err)
	}
	for _, extras := range result {
		sortExtrasForDisplay(extras)
	}
	return result, nil
}

// sortExtrasForDisplay orders trailers first, then the remaining kinds in
// vocabulary order, then title, keeping output stable for the API.
func sortExtrasForDisplay(extras []ExtraWithFile) {
	rank := make(map[models.ExtraKind]int, len(models.AllExtraKinds))
	for i, k := range models.AllExtraKinds {
		rank[k] = i
	}
	slices.SortStableFunc(extras, func(a, b ExtraWithFile) int {
		if ra, rb := rank[a.Kind], rank[b.Kind]; ra != rb {
			return ra - rb
		}
		if a.SortOrder != b.SortOrder {
			return a.SortOrder - b.SortOrder
		}
		return strings.Compare(a.Title, b.Title)
	})
}
