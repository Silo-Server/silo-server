package chapterthumbs

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// NewURLResolver protects each issued chapter image until its actual URL
// expiry. Other artwork passes through the underlying resolver. Protection
// commits before exposing URLs and shares file row locks with chapter updates
// and deletion, so a stale image cannot acquire a new URL after retirement.
func NewURLResolver(pool *pgxpool.Pool, resolver artworkurl.Resolver) artworkurl.Resolver {
	return chapterURLResolver{pool: pool, resolver: resolver}
}

type chapterURLResolver struct {
	pool     *pgxpool.Pool
	resolver artworkurl.Resolver
}

func (r chapterURLResolver) ResolveURLs(ctx context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	if r.resolver == nil {
		return nil
	}
	return r.protect(ctx, r.resolver.ResolveURLs(ctx, keys))
}

func (r chapterURLResolver) ResolveURLFor(ctx context.Context, key string, ttl time.Duration) (catalog.ResolvedImageURL, bool) {
	resolved, ok := artworkurl.ResolveURLFor(ctx, r.resolver, key, ttl)
	if !ok {
		return catalog.ResolvedImageURL{}, false
	}
	resolved, ok = r.protect(ctx, map[string]catalog.ResolvedImageURL{key: resolved})[key]
	return resolved, ok && resolved.URL != ""
}

func (r chapterURLResolver) protect(ctx context.Context, resolved map[string]catalog.ResolvedImageURL) map[string]catalog.ResolvedImageURL {
	out := maps.Clone(resolved)
	var keys []string
	var ids []int64
	for key, value := range resolved {
		if _, ok := imageKeyGroup(key); !ok {
			continue
		}
		delete(out, key)
		if value.URL == "" || value.ExpiresAt == nil || !value.ExpiresAt.After(time.Now()) {
			continue
		}
		keys = append(keys, key)
		id, _ := imagesFileID(key)
		ids = append(ids, int64(id))
	}
	if len(keys) == 0 || r.pool == nil {
		return out
	}
	slices.Sort(keys)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	live, err := r.protectImages(ctx, keys, ids, resolved)
	if err != nil {
		slog.WarnContext(ctx, "chapter image URLs could not be protected", "component", "chapterthumbs", "images", len(keys), "error", err)
		return out
	}
	for key := range live {
		out[key] = resolved[key]
	}
	return out
}

func (r chapterURLResolver) protectImages(ctx context.Context, keys []string, ids []int64, resolved map[string]catalog.ResolvedImageURL) (map[string]bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Lock files in ID order, matching batched deletion, before queuing image
	// protection. FOR SHARE conflicts with both chapter UPDATE and file DELETE.
	rows, err := tx.Query(ctx, `
		SELECT mf.id, ARRAY(
			SELECT chapter->>'thumbnail_path'
			FROM jsonb_array_elements(
				(CASE WHEN jsonb_typeof(mf.chapters) = 'array' THEN mf.chapters ELSE '[]'::jsonb END) || mf.marker_thumbnails
			) AS chapter
			WHERE chapter->>'thumbnail_path' = ANY($2::text[])
		)
		FROM public.media_files mf
		WHERE mf.id = ANY($1::bigint[])
		ORDER BY mf.id
		FOR SHARE OF mf`, ids, keys)
	if err != nil {
		return nil, fmt.Errorf("lock chapter images: %w", err)
	}
	live := make(map[string]bool, len(keys))
	for rows.Next() {
		var id int64
		var referenced []string
		if err := rows.Scan(&id, &referenced); err != nil {
			rows.Close()
			return nil, err
		}
		for _, key := range referenced {
			keyID, _ := imagesFileID(key)
			if int64(keyID) == id {
				live[key] = true
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var protected []string
	var expiries []time.Time
	for _, key := range keys {
		if live[key] {
			protected = append(protected, key)
			// PostgreSQL timestamps have microsecond precision; round up so
			// persistence never makes a reported URL expire earlier.
			expiry := *resolved[key].ExpiresAt
			deadline := expiry.Truncate(time.Microsecond)
			if deadline.Before(expiry) {
				deadline = deadline.Add(time.Microsecond)
			}
			expiries = append(expiries, deadline)
		}
	}
	if len(protected) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.blob_gc_queue (prefix, not_before)
			SELECT * FROM unnest($1::text[], $2::timestamptz[])
			ON CONFLICT (prefix) DO UPDATE
			SET not_before = EXCLUDED.not_before
			WHERE public.blob_gc_queue.not_before < EXCLUDED.not_before`, protected, expiries); err != nil {
			return nil, fmt.Errorf("protect chapter image URLs: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return live, nil
}
