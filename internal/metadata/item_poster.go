package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ErrItemNotFound reports a content ID with no media_items row.
var ErrItemNotFound = errors.New("metadata: item not found")

// ItemPosterPath returns the stored poster path of the media_items row
// contentID names: an artwork store key once cached, a provider URL or plugin
// path before, or "" when the item has no poster.
func (r *ImageCacheJobRepository) ItemPosterPath(ctx context.Context, contentID string) (string, error) {
	if r == nil || r.pool == nil {
		return "", errors.New("metadata: image cache repository has no database pool")
	}
	var posterPath string
	err := r.pool.QueryRow(ctx, `SELECT COALESCE(poster_path, '') FROM media_items WHERE content_id = $1`, contentID).Scan(&posterPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrItemNotFound
	}
	if err != nil {
		return "", fmt.Errorf("reading item poster path: %w", err)
	}
	return posterPath, nil
}

// EnqueueItemPosterCache makes sure the poster of the item contentID names has
// an image-cache job queued while the item has no cached poster and a provider
// source to cache from. It is the ordinary upsert, so a job already queued or
// running is left alone and a failed one keeps its backoff; only a job that
// succeeded for a source whose copy is gone is queued again.
func (r *ImageCacheJobRepository) EnqueueItemPosterCache(ctx context.Context, contentID string) error {
	if r == nil || r.pool == nil {
		return nil
	}
	var in EnqueueImageCacheJobInput
	var posterPath, tmdbID, tvdbID, imdbID string
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(poster_path, ''), COALESCE(poster_source_path, ''), COALESCE(type, ''),
		       COALESCE(tmdb_id, ''), COALESCE(tvdb_id, ''), COALESCE(imdb_id, '')
		FROM media_items WHERE content_id = $1`, contentID).
		Scan(&posterPath, &in.SourcePath, &in.ContentType, &tmdbID, &tvdbID, &imdbID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading item poster source: %w", err)
	}
	// The same candidate rule the provider-artwork discovery applies: a
	// provider source, and no cached copy in the slot.
	cached := posterPath != "" && !strings.Contains(posterPath, "://")
	if cached || !isRemoteImageSourcePath(in.SourcePath) {
		return nil
	}
	in.TargetType = ImageCacheTargetItem
	in.TargetContentID = contentID
	in.SeriesID = contentID
	in.ImageType = ImageCacheImagePoster
	in.ProviderID = imageCacheProviderIDFromSource(in.SourcePath, imageCachePrimaryProvider(tmdbID, tvdbID, imdbID))
	in.ProviderContentID = imageCacheProviderContentID(in.ProviderID, tmdbID, tvdbID, imdbID, contentID)
	in.ContentType = imageCacheContentType(in.ContentType)
	in.RequeueSucceeded = true
	_, err = r.EnqueueBatch(ctx, []EnqueueImageCacheJobInput{in})
	return err
}

// IsRemoteImageSourcePath reports whether path is a provider's artwork: an
// http(s) URL or a plugin path, not a local source (file://, nfo+…) that only
// the image-cache job reads.
func IsRemoteImageSourcePath(path string) bool { return isRemoteImageSourcePath(path) }
