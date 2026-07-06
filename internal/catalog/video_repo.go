package catalog

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
)

// VideoRepository persists remote provider videos (trailers, teasers, ...) in
// the item_videos table. The set is replaced wholesale on each metadata
// refresh, mirroring ItemRepository.ReplacePeople.
type VideoRepository struct {
	pool *pgxpool.Pool
}

// NewVideoRepository creates a video repository backed by the given pool.
func NewVideoRepository(pool *pgxpool.Pool) *VideoRepository {
	return &VideoRepository{pool: pool}
}

const itemVideoColumns = `content_id, provider, provider_key, kind, site, site_key, name, language, is_official, size_hint, published_at, sort_order`

// ReplaceByContentID transactionally replaces every stored video for the item
// with the given set. An empty set clears the item's videos.
func (r *VideoRepository) ReplaceByContentID(ctx context.Context, contentID string, videos []models.ItemVideo) error {
	contentID = strings.TrimSpace(contentID)
	if contentID == "" {
		return fmt.Errorf("content_id is required")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin replace videos transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, "DELETE FROM item_videos WHERE content_id = $1", contentID); err != nil {
		return fmt.Errorf("delete existing videos: %w", err)
	}

	if len(videos) == 0 {
		return tx.Commit(ctx)
	}

	// Deduplicate by (provider, provider_key) — ON CONFLICT cannot handle the
	// same row appearing twice within a single INSERT.
	type dedupKey struct {
		Provider    string
		ProviderKey string
	}
	seen := make(map[dedupKey]struct{}, len(videos))
	deduped := make([]models.ItemVideo, 0, len(videos))
	for _, v := range videos {
		key := dedupKey{v.Provider, v.ProviderKey}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, v)
	}

	var sb strings.Builder
	sb.WriteString("INSERT INTO item_videos (id, " + itemVideoColumns + ") VALUES ")
	args := make([]interface{}, 0, len(deduped)*13)
	for i, v := range deduped {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * 13
		fmt.Fprintf(&sb, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11, base+12, base+13)

		rowIDStr, err := idgen.NextID()
		if err != nil {
			return fmt.Errorf("generate item video id: %w", err)
		}
		rowID, _ := strconv.ParseInt(rowIDStr, 10, 64)
		args = append(args, rowID, contentID, v.Provider, v.ProviderKey, string(v.Kind), v.Site, v.SiteKey,
			v.Name, v.Language, v.IsOfficial, v.SizeHint, v.PublishedAt, i)
	}
	sb.WriteString(` ON CONFLICT (content_id, provider, provider_key) DO UPDATE SET
		kind = EXCLUDED.kind, site = EXCLUDED.site, site_key = EXCLUDED.site_key,
		name = EXCLUDED.name, language = EXCLUDED.language, is_official = EXCLUDED.is_official,
		size_hint = EXCLUDED.size_hint, published_at = EXCLUDED.published_at,
		sort_order = EXCLUDED.sort_order, updated_at = now()`)

	if _, err := tx.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("insert videos: %w", err)
	}

	return tx.Commit(ctx)
}

// GetByContentID returns the item's videos ordered for display: official
// entries before unofficial within the stored sort order.
func (r *VideoRepository) GetByContentID(ctx context.Context, contentID string) ([]models.ItemVideo, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, `+itemVideoColumns+`
		FROM item_videos
		WHERE content_id = $1
		ORDER BY sort_order, id`, contentID)
	if err != nil {
		return nil, fmt.Errorf("query item videos: %w", err)
	}
	defer rows.Close()
	return scanItemVideos(rows)
}

// ListByContentIDs returns videos for a batch of items, keyed by content_id.
// Items without videos are absent from the map.
func (r *VideoRepository) ListByContentIDs(ctx context.Context, contentIDs []string) (map[string][]models.ItemVideo, error) {
	if len(contentIDs) == 0 {
		return map[string][]models.ItemVideo{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, `+itemVideoColumns+`
		FROM item_videos
		WHERE content_id = ANY($1)
		ORDER BY content_id, sort_order, id`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("query item videos batch: %w", err)
	}
	defer rows.Close()

	videos, err := scanItemVideos(rows)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]models.ItemVideo, len(contentIDs))
	for _, v := range videos {
		result[v.ContentID] = append(result[v.ContentID], v)
	}
	return result, nil
}

func scanItemVideos(rows pgx.Rows) ([]models.ItemVideo, error) {
	var videos []models.ItemVideo
	for rows.Next() {
		var v models.ItemVideo
		var kind string
		if err := rows.Scan(&v.ID, &v.ContentID, &v.Provider, &v.ProviderKey, &kind, &v.Site, &v.SiteKey,
			&v.Name, &v.Language, &v.IsOfficial, &v.SizeHint, &v.PublishedAt, &v.SortOrder); err != nil {
			return nil, fmt.Errorf("scan item video: %w", err)
		}
		v.Kind = models.ExtraKind(kind)
		videos = append(videos, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate item videos: %w", err)
	}
	return videos, nil
}
