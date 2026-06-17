package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

var (
	// ErrMediaItemProviderIDNotFound is returned when no provider IDs exist for
	// a given content item.
	ErrMediaItemProviderIDNotFound = errors.New("media item provider ids not found")
)

// ProviderIDRepository persists durable provider IDs for media items.
type ProviderIDRepository struct {
	pool *pgxpool.Pool
}

// NewProviderIDRepository creates a new provider ID repository backed by the
// given pool.
func NewProviderIDRepository(pool *pgxpool.Pool) *ProviderIDRepository {
	return &ProviderIDRepository{pool: pool}
}

func (r *ProviderIDRepository) AttachTMDBID(ctx context.Context, contentID, itemType string, tmdbID int) error {
	contentID = strings.TrimSpace(contentID)
	itemType = strings.TrimSpace(itemType)
	if contentID == "" {
		return fmt.Errorf("content_id is required")
	}
	if itemType == "" {
		return fmt.Errorf("item_type is required")
	}
	if tmdbID <= 0 {
		return fmt.Errorf("tmdb_id must be positive")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin attach tmdb transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tmdbText := strconv.Itoa(tmdbID)
	var existingType, existingTMDBID string
	if err := tx.QueryRow(ctx, `
		SELECT type, COALESCE(tmdb_id, '')
		FROM media_items
		WHERE content_id = $1
		FOR UPDATE
	`, contentID).Scan(&existingType, &existingTMDBID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("loading media item for tmdb attach: content not found")
		}
		return fmt.Errorf("loading media item for tmdb attach: %w", err)
	}
	if existingType != itemType {
		return fmt.Errorf("media item type mismatch: got %q, want %q", existingType, itemType)
	}
	if existingTMDBID != "" && existingTMDBID != tmdbText {
		return fmt.Errorf("media item tmdb id conflict: got %q, want %q", existingTMDBID, tmdbText)
	}

	var existingProviderTMDBID string
	err = tx.QueryRow(ctx, `
		SELECT provider_id
		FROM media_item_provider_ids
		WHERE content_id = $1 AND provider = 'tmdb'
		FOR UPDATE
	`, contentID).Scan(&existingProviderTMDBID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("loading media item tmdb provider id: %w", err)
	}
	if existingProviderTMDBID != "" && existingProviderTMDBID != tmdbText {
		return fmt.Errorf("media item tmdb provider id conflict: got %q, want %q", existingProviderTMDBID, tmdbText)
	}

	var existingOwnerContentID string
	err = tx.QueryRow(ctx, `
		SELECT content_id
		FROM media_items
		WHERE type = $1
		  AND tmdb_id = $2
		  AND content_id <> $3
		LIMIT 1
	`, itemType, tmdbText, contentID).Scan(&existingOwnerContentID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("checking tmdb id owner: %w", err)
	}
	if existingOwnerContentID != "" {
		return fmt.Errorf("tmdb id %q already belongs to content_id %q", tmdbText, existingOwnerContentID)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE media_items
		SET tmdb_id = $1,
		    updated_at = NOW()
		WHERE content_id = $2
		  AND type = $3
	`, tmdbText, contentID, itemType); err != nil {
		return fmt.Errorf("updating media item tmdb id: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id, created_at, updated_at)
		VALUES ($1, $2, 'tmdb', $3, NOW(), NOW())
		ON CONFLICT (content_id, provider) DO UPDATE
		SET item_type = EXCLUDED.item_type,
		    provider_id = EXCLUDED.provider_id,
		    updated_at = NOW()
	`, contentID, itemType, tmdbText); err != nil {
		return fmt.Errorf("upserting media item tmdb provider id: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit attach tmdb transaction: %w", err)
	}
	return nil
}

const providerIDColumns = `content_id, item_type, provider, provider_id, created_at, updated_at`

// excludedProviderIDs lists providers that ReplaceByContentID does NOT manage:
// it neither persists nor deletes them. Two kinds live here:
//   - ephemeral, query-only inputs (metadb, _filepath, oshash) that must never
//     be written as durable rows; and
//   - Silo-internal identity anchors (manga_series) stamped directly by the
//     scanner to keep manga re-scans idempotent. Replace must leave these rows
//     intact — otherwise the first manga enrichment (which calls
//     ReplaceByContentID with only the external IDs) would delete the
//     manga_series anchor, and the next scan would mint a duplicate series and
//     lose the enriched metadata.
var excludedProviderIDs = map[string]struct{}{
	"metadb":       {},
	"_filepath":    {},
	"oshash":       {},
	"manga_series": {},
}

// unmanagedProviderIDList returns excludedProviderIDs as a lowercased, sorted
// slice for binding into the Replace DELETE so those rows are preserved.
func unmanagedProviderIDList() []string {
	out := make([]string, 0, len(excludedProviderIDs))
	for p := range excludedProviderIDs {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

var preferredProviderIDOrder = map[string]int{
	"tmdb": 0,
	"tvdb": 1,
	"imdb": 2,
}

func normalizeDurableProviderIDs(providerIDs map[string]string) []models.MediaItemProviderID {
	if len(providerIDs) == 0 {
		return nil
	}

	entries := make([]models.MediaItemProviderID, 0, len(providerIDs))
	for provider, providerID := range providerIDs {
		provider = strings.TrimSpace(provider)
		providerID = strings.TrimSpace(providerID)
		if provider == "" || providerID == "" {
			continue
		}
		if _, excluded := excludedProviderIDs[strings.ToLower(provider)]; excluded {
			continue
		}
		entries = append(entries, models.MediaItemProviderID{
			Provider:   provider,
			ProviderID: providerID,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		leftRank, leftOK := preferredProviderIDOrder[strings.ToLower(entries[i].Provider)]
		rightRank, rightOK := preferredProviderIDOrder[strings.ToLower(entries[j].Provider)]
		switch {
		case leftOK && rightOK && leftRank != rightRank:
			return leftRank < rightRank
		case leftOK != rightOK:
			return leftOK
		case strings.ToLower(entries[i].Provider) != strings.ToLower(entries[j].Provider):
			return strings.ToLower(entries[i].Provider) < strings.ToLower(entries[j].Provider)
		default:
			return entries[i].ProviderID < entries[j].ProviderID
		}
	})

	return entries
}

func scanProviderID(row pgx.Row) (*models.MediaItemProviderID, error) {
	var id models.MediaItemProviderID
	if err := row.Scan(
		&id.ContentID,
		&id.ItemType,
		&id.Provider,
		&id.ProviderID,
		&id.CreatedAt,
		&id.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("scanning media item provider id: %w", err)
	}
	return &id, nil
}

func scanProviderIDs(rows pgx.Rows) ([]*models.MediaItemProviderID, error) {
	var ids []*models.MediaItemProviderID
	for rows.Next() {
		var id models.MediaItemProviderID
		if err := rows.Scan(
			&id.ContentID,
			&id.ItemType,
			&id.Provider,
			&id.ProviderID,
			&id.CreatedAt,
			&id.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning media item provider id row: %w", err)
		}
		ids = append(ids, &id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating media item provider id rows: %w", err)
	}
	if ids == nil {
		ids = []*models.MediaItemProviderID{}
	}
	return ids, nil
}

// GetByContentID loads all durable provider IDs for the given media item.
func (r *ProviderIDRepository) GetByContentID(ctx context.Context, contentID string) ([]*models.MediaItemProviderID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+providerIDColumns+`
		FROM media_item_provider_ids
		WHERE content_id = $1
		ORDER BY
			CASE LOWER(provider)
				WHEN 'tmdb' THEN 0
				WHEN 'tvdb' THEN 1
				WHEN 'imdb' THEN 2
				ELSE 3
			END,
			LOWER(provider) ASC,
			provider_id ASC
	`, contentID)
	if err != nil {
		return nil, fmt.Errorf("getting provider IDs by content_id: %w", err)
	}
	defer rows.Close()
	return scanProviderIDs(rows)
}

// GetByContentIDs fetches provider IDs for many content items in one query,
// grouped by content_id (IDs with no rows are absent). Replaces per-item
// GetByContentID loops on the enrichment claim path.
func (r *ProviderIDRepository) GetByContentIDs(ctx context.Context, contentIDs []string) (map[string][]*models.MediaItemProviderID, error) {
	out := make(map[string][]*models.MediaItemProviderID, len(contentIDs))
	if len(contentIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+providerIDColumns+`
		FROM media_item_provider_ids
		WHERE content_id = ANY($1)
		ORDER BY content_id,
			CASE LOWER(provider)
				WHEN 'tmdb' THEN 0
				WHEN 'tvdb' THEN 1
				WHEN 'imdb' THEN 2
				ELSE 3
			END,
			LOWER(provider) ASC,
			provider_id ASC
	`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("getting provider IDs by content_ids: %w", err)
	}
	defer rows.Close()
	all, err := scanProviderIDs(rows)
	if err != nil {
		return nil, err
	}
	for _, pid := range all {
		out[pid.ContentID] = append(out[pid.ContentID], pid)
	}
	return out, nil
}

// ReplaceByContentID replaces the durable provider IDs it manages for a content
// item, leaving unmanaged providers (excludedProviderIDs, e.g. the scanner's
// manga_series identity anchor) intact.
func (r *ProviderIDRepository) ReplaceByContentID(ctx context.Context, contentID string, providerIDs map[string]string) error {
	if strings.TrimSpace(contentID) == "" {
		return fmt.Errorf("content_id is required")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin provider id replace transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var itemType string
	if err := tx.QueryRow(ctx, `SELECT type FROM media_items WHERE content_id = $1`, contentID).Scan(&itemType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("loading item type for %s: content not found", contentID)
		}
		return fmt.Errorf("loading item type for %s: %w", contentID, err)
	}

	if err := r.ReplaceByContentIDTx(ctx, tx, contentID, itemType, providerIDs); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit provider id replace transaction: %w", err)
	}
	return nil
}

// ReplaceByContentIDTx replaces durable provider IDs using the caller's transaction.
func (r *ProviderIDRepository) ReplaceByContentIDTx(
	ctx context.Context,
	tx pgx.Tx,
	contentID string,
	itemType string,
	providerIDs map[string]string,
) error {
	if strings.TrimSpace(contentID) == "" {
		return fmt.Errorf("content_id is required")
	}
	itemType = strings.TrimSpace(itemType)
	if itemType == "" {
		return fmt.Errorf("item_type is required")
	}
	entries := normalizeDurableProviderIDs(providerIDs)

	// Preserve providers Replace does not manage (see excludedProviderIDs):
	// query-only inputs and the scanner's manga_series identity anchor.
	if _, err := tx.Exec(ctx, `
		DELETE FROM media_item_provider_ids
		WHERE content_id = $1
		  AND lower(provider) <> ALL($2::text[])
	`, contentID, unmanagedProviderIDList()); err != nil {
		return fmt.Errorf("deleting provider IDs for %s: %w", contentID, err)
	}

	for _, entry := range entries {
		_, err := tx.Exec(ctx, `
			INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NOW(), NOW())
		`, contentID, itemType, entry.Provider, entry.ProviderID)
		if err != nil {
			return fmt.Errorf("inserting provider ID %s for %s: %w", entry.Provider, contentID, err)
		}
	}
	return nil
}

// FindContentIDByProviderIDs looks up the first item matching any durable
// provider ID in priority order. Empty provider maps return no match.
func (r *ProviderIDRepository) FindContentIDByProviderIDs(
	ctx context.Context,
	providerIDs map[string]string,
	itemType string,
	excludeContentID string,
) (string, error) {
	entries := normalizeDurableProviderIDs(providerIDs)
	if len(entries) == 0 {
		return "", nil
	}

	providers := make([]string, len(entries))
	values := make([]string, len(entries))
	ordinals := make([]int32, len(entries))
	for i, entry := range entries {
		providers[i] = entry.Provider
		values[i] = entry.ProviderID
		ordinals[i] = int32(i)
	}

	query := `
		WITH requested(provider, provider_id, ord) AS (
			SELECT * FROM unnest($1::text[], $2::text[], $3::int[])
		)
		SELECT mi.content_id
		FROM requested r
		JOIN media_item_provider_ids mip
		  ON mip.provider = r.provider
		 AND mip.provider_id = r.provider_id
		JOIN media_items mi
		  ON mi.content_id = mip.content_id
		WHERE ($4 = '' OR mip.item_type = $4)
		  AND ($5 = '' OR mi.content_id <> $5)
		ORDER BY
			r.ord ASC,
			CASE lower(trim(mi.status))
				WHEN 'matched' THEN 0
				WHEN 'pending' THEN 1
				WHEN 'unmatched' THEN 2
				ELSE 3
			END,
			mi.updated_at DESC,
			mi.content_id ASC
		LIMIT 1`

	var contentID string
	err := r.pool.QueryRow(ctx, query, providers, values, ordinals, strings.TrimSpace(itemType), strings.TrimSpace(excludeContentID)).Scan(&contentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("finding content_id by provider IDs: %w", err)
	}

	return contentID, nil
}
