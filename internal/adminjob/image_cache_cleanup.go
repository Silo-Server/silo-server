package adminjob

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

const JobTypeImageCacheCleanup = "image_cache_cleanup"

type ImageCacheCleanupRequest struct {
	LibraryID   int      `json:"library_id"`
	LibraryName string   `json:"library_name"`
	Prefixes    []string `json:"prefixes"`
}

type ImageCacheCleanupResult struct {
	LibraryID        int                             `json:"library_id"`
	LibraryName      string                          `json:"library_name"`
	DeletedPrefixes  int                             `json:"deleted_prefixes"`
	DeletedS3Objects int                             `json:"deleted_s3_objects"`
	GCStats          metadata.ArtworkRevisionGCStats `json:"gc_stats"`
}

type imageCacheCleanupExecutor interface {
	Execute(ctx context.Context, req ImageCacheCleanupRequest, progress func(current, total int, message string)) (*ImageCacheCleanupResult, error)
}

type ImageCacheCleanupExecutor struct {
	gc interface {
		Run(ctx context.Context) (metadata.ArtworkRevisionGCStats, error)
	}
}

func NewImageCacheCleanupExecutor(gc interface {
	Run(ctx context.Context) (metadata.ArtworkRevisionGCStats, error)
}) *ImageCacheCleanupExecutor {
	if gc == nil {
		return nil
	}
	return &ImageCacheCleanupExecutor{gc: gc}
}

func (e *ImageCacheCleanupExecutor) Execute(
	ctx context.Context,
	req ImageCacheCleanupRequest,
	progress func(current, total int, message string),
) (*ImageCacheCleanupResult, error) {
	if e == nil || e.gc == nil {
		return nil, fmt.Errorf("image cache cleanup executor is not configured")
	}
	if progress != nil {
		progress(0, 1, "Running reference-aware artwork cleanup")
	}
	stats, err := e.gc.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("reference-aware artwork cleanup: %w", err)
	}
	if progress != nil {
		progress(1, 1, "Reference-aware artwork cleanup completed")
	}
	return &ImageCacheCleanupResult{
		LibraryID:        req.LibraryID,
		LibraryName:      req.LibraryName,
		DeletedPrefixes:  0,
		DeletedS3Objects: 0,
		GCStats:          stats,
	}, nil
}

func decodeImageCacheCleanupRequest(data json.RawMessage) (ImageCacheCleanupRequest, error) {
	var req ImageCacheCleanupRequest
	if len(data) == 0 {
		return req, fmt.Errorf("missing image cache cleanup payload")
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return req, fmt.Errorf("invalid image cache cleanup request payload: %w", err)
	}
	return req, nil
}
