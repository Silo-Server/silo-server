package adminjob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/blobstore"
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
	// StalledClaims counts consecutive claims that deleted nothing.
	StalledClaims int `json:"stalled_claims,omitempty"`
}

type imageCacheCleanupExecutor interface {
	Execute(ctx context.Context, req ImageCacheCleanupRequest, start int, progress func(next int, deleted ImageCacheCleanupResult)) (int, ImageCacheCleanupResult, error)
}

type ImageCacheCleanupExecutor struct {
	store blobstore.Store
}

func NewImageCacheCleanupExecutor(store blobstore.Store) *ImageCacheCleanupExecutor {
	if store == nil {
		return nil
	}
	return &ImageCacheCleanupExecutor{store: store}
}

// Execute deletes req.Prefixes from index start onward. After each prefix it
// reports the index of the next one and the counts so far; a failed delete is
// logged and skipped. It returns the index of the first prefix it did not
// finish. When ctx ends, Execute stops there and returns ctx's error, so a
// later call resumes at the interrupted prefix. A prefix whose delete was
// running when ctx ended counts as unfinished even if the store reported
// success, because the store may have stopped partway through it.
func (e *ImageCacheCleanupExecutor) Execute(
	ctx context.Context,
	req ImageCacheCleanupRequest,
	start int,
	progress func(next int, deleted ImageCacheCleanupResult),
) (int, ImageCacheCleanupResult, error) {
	deleted := ImageCacheCleanupResult{LibraryID: req.LibraryID, LibraryName: req.LibraryName}
	if e == nil || e.store == nil {
		return start, deleted, fmt.Errorf("image cache cleanup executor is not configured")
	}

	total := len(req.Prefixes)
	for index := min(max(start, 0), total); index < total; index++ {
		if err := ctx.Err(); err != nil {
			return index, deleted, err
		}
		prefix := req.Prefixes[index]
		n, err := e.store.DeletePrefix(ctx, prefix)
		deleted.DeletedS3Objects += n
		if ctxErr := ctx.Err(); ctxErr != nil {
			// After the deadline every remaining delete fails at once; stop
			// instead of logging and skipping each of them. The S3 client's
			// per-object fallback logs such failures and still returns nil.
			return index, deleted, ctxErr
		}
		if err != nil {
			slog.WarnContext(ctx, "image cache cleanup: s3 delete failed", "component", "adminjob", "prefix", prefix, "error", err)
		} else {
			deleted.DeletedPrefixes++
		}
		if progress != nil {
			progress(index+1, deleted)
		}
	}
	return total, deleted, nil
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
