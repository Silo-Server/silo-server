package heroclips

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Clip is one item's published clip.
type Clip struct {
	ContentID  string
	Path       string
	Recipe     string
	Source     string
	SourceKey  string
	DurationMS int
	Width      int
	Height     int
	Bytes      int64
	UpdatedAt  time.Time
}

// Candidate is a title the generator may turn into a clip: the trailer it would
// come from, and nothing else.
type Candidate struct {
	ContentID string
	Site      string
	SiteKey   string
	Source    string
}

// Repository is the catalog's record of which items have a clip, and where the
// object that holds it lives.
type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// ClipPathsFor returns the stored path of the clip each of these items has.
//
// Only a ready clip resolves: a row that records a failure is not handed to a
// client as if it had a file.
func (r *Repository) ClipPathsFor(ctx context.Context, contentIDs []string) (map[string]string, error) {
	if r == nil || r.pool == nil || len(contentIDs) == 0 {
		return nil, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT content_id, clip_path
		FROM public.hero_clips
		WHERE content_id = ANY($1::text[]) AND state = 'ready' AND clip_path <> ''`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("load hero clips: %w", err)
	}
	defer rows.Close()

	paths := make(map[string]string, len(contentIDs))
	for rows.Next() {
		var contentID, path string
		if err := rows.Scan(&contentID, &path); err != nil {
			return nil, err
		}
		paths[contentID] = path
	}

	return paths, rows.Err()
}

// PublishReady records a clip and the object that holds it. The object is
// uploaded before this runs, so a row only ever names a file a client can read.
func (r *Repository) PublishReady(ctx context.Context, clip Clip) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("hero clips: no database")
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.hero_clips (
			content_id, clip_path, recipe, source, source_key,
			duration_ms, width, height, bytes, state, error, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'ready', '', now())
		ON CONFLICT (content_id) DO UPDATE SET
			clip_path = EXCLUDED.clip_path,
			recipe = EXCLUDED.recipe,
			source = EXCLUDED.source,
			source_key = EXCLUDED.source_key,
			duration_ms = EXCLUDED.duration_ms,
			width = EXCLUDED.width,
			height = EXCLUDED.height,
			bytes = EXCLUDED.bytes,
			state = 'ready',
			error = '',
			updated_at = now()`,
		clip.ContentID, clip.Path, clip.Recipe, clip.Source, clip.SourceKey,
		clip.DurationMS, clip.Width, clip.Height, clip.Bytes)
	if err != nil {
		return fmt.Errorf("publish hero clip: %w", err)
	}

	return nil
}

// RecordFailure records that an item's clip could not be made, which is what
// lets the generator back off instead of retrying it on every pass. A clip that
// is already published is left alone: a failed retry must not take a working
// clip away from viewers.
func (r *Repository) RecordFailure(ctx context.Context, contentID, recipe, source, sourceKey, message string) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("hero clips: no database")
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.hero_clips (content_id, clip_path, recipe, source, source_key, state, error, updated_at)
		VALUES ($1, '', $2, $3, $4, 'failed', $5, now())
		ON CONFLICT (content_id) DO UPDATE SET
			recipe = EXCLUDED.recipe,
			source = EXCLUDED.source,
			source_key = EXCLUDED.source_key,
			state = CASE WHEN public.hero_clips.state = 'ready' THEN 'ready' ELSE 'failed' END,
			error = EXCLUDED.error,
			updated_at = now()`,
		contentID, recipe, source, sourceKey, truncateError(message))
	if err != nil {
		return fmt.Errorf("record hero clip failure: %w", err)
	}

	return nil
}

// ClipCandidates lists the titles the hero could play a clip for: the items the
// featured discover rows hold that have an official trailer on YouTube and are
// missing a clip, holding one an older recipe made, or failed long enough ago to
// retry. One row per title, best trailer first.
//
// The featured rows are read from their published snapshot — the same ids the
// home hero renders — so the work follows what viewers are actually shown rather
// than every title in the library.
func (r *Repository) ClipCandidates(ctx context.Context, recipe string, retryAfter time.Duration, limit int) ([]Candidate, error) {
	if r == nil || r.pool == nil {
		return nil, nil
	}

	rows, err := r.pool.Query(ctx, `
		WITH featured AS (
			SELECT DISTINCT unnest(content_ids) AS content_id
			FROM public.trending_discover_snapshots
		)
		SELECT DISTINCT ON (iv.content_id)
		       iv.content_id, iv.site, iv.site_key, iv.provider
		FROM featured f
		JOIN public.item_videos iv ON iv.content_id = f.content_id
		LEFT JOIN public.hero_clips hc ON hc.content_id = f.content_id
		WHERE lower(iv.site) = 'youtube'
		  AND iv.kind = 'trailer'
		  AND (
			hc.content_id IS NULL
			OR hc.recipe <> $1
			OR (hc.state = 'failed' AND hc.updated_at < now() - $2::interval)
		  )
		ORDER BY iv.content_id, iv.is_official DESC, iv.size_hint DESC, iv.sort_order
		LIMIT $3`, recipe, retryAfter.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("load hero clip candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]Candidate, 0, limit)
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.ContentID, &c.Site, &c.SiteKey, &c.Source); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}

	return candidates, rows.Err()
}

// maxErrorLength bounds what a provider's failure text can grow to in the row.
const maxErrorLength = 500

func truncateError(message string) string {
	if len(message) <= maxErrorLength {
		return message
	}
	return message[:maxErrorLength]
}
