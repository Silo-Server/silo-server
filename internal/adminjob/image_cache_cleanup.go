package adminjob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

const JobTypeImageCacheCleanup = "image_cache_cleanup"

type ImageCacheCleanupRequest struct {
	LibraryID   int      `json:"library_id"`
	LibraryName string   `json:"library_name"`
	Prefixes    []string `json:"prefixes"`
}

type ImageCacheCleanupResult struct {
	LibraryID        int    `json:"library_id"`
	LibraryName      string `json:"library_name"`
	DeletedPrefixes  int    `json:"deleted_prefixes"`
	DeletedS3Objects int    `json:"deleted_s3_objects"`
}

type imageCacheCleanupExecutor interface {
	Execute(ctx context.Context, req ImageCacheCleanupRequest, progress func(current, total int, message string)) (*ImageCacheCleanupResult, error)
}

type ImageCacheCleanupExecutor struct {
	store artworkstore.Store
}

func NewImageCacheCleanupExecutor(store artworkstore.Store) *ImageCacheCleanupExecutor {
	if store == nil {
		return nil
	}
	return &ImageCacheCleanupExecutor{store: store}
}

func (e *ImageCacheCleanupExecutor) Execute(
	ctx context.Context,
	req ImageCacheCleanupRequest,
	progress func(current, total int, message string),
) (*ImageCacheCleanupResult, error) {
	if e == nil || e.store == nil {
		return nil, fmt.Errorf("image cache cleanup executor is not configured")
	}
	if len(req.Prefixes) == 0 {
		return &ImageCacheCleanupResult{
			LibraryID:        req.LibraryID,
			LibraryName:      req.LibraryName,
			DeletedPrefixes:  0,
			DeletedS3Objects: 0,
		}, nil
	}

	total := len(req.Prefixes)
	deletedPrefixes := 0
	deletedObjects := 0
	for index, prefix := range req.Prefixes {
		if progress != nil {
			progress(index, total, fmt.Sprintf("Cleaning cached images %d/%d", index+1, total))
		}
		n, err := e.store.DeletePrefix(ctx, prefix)
		if err != nil {
			slog.WarnContext(ctx, "image cache cleanup: s3 delete failed", "component", "adminjob", "prefix", prefix, "error", err)
			continue
		}
		deletedPrefixes++
		deletedObjects += n
	}

	if progress != nil {
		progress(total, total, "Cached image cleanup completed")
	}

	return &ImageCacheCleanupResult{
		LibraryID:        req.LibraryID,
		LibraryName:      req.LibraryName,
		DeletedPrefixes:  deletedPrefixes,
		DeletedS3Objects: deletedObjects,
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
