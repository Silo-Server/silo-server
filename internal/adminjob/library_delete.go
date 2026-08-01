package adminjob

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

const JobTypeDeleteLibrary = "delete_library"

type DeleteLibraryRequest struct {
	LibraryID   int    `json:"library_id"`
	LibraryName string `json:"library_name"`
}

type DeleteLibraryResult struct {
	LibraryID            int    `json:"library_id"`
	LibraryName          string `json:"library_name"`
	DeletedMediaFiles    int    `json:"deleted_media_files"`
	DeletedItemLinks     int    `json:"deleted_item_links"`
	DeletedOrphanedItems int    `json:"deleted_orphaned_items"`
	DeletedS3Objects     int    `json:"deleted_s3_objects"`
	ImageCleanupQueued   bool   `json:"image_cleanup_queued"`
	ImageCleanupJobID    string `json:"image_cleanup_job_id,omitempty"`
	ImageCleanupDirs     int    `json:"image_cleanup_dirs"`

	orphanedImageDirs []string
}

// S3PrefixDeleter can delete all objects under a prefix.
type S3PrefixDeleter interface {
	DeletePrefix(ctx context.Context, bucket, prefix string) (int, error)
	Bucket() string
}

type deleteLibraryExecutor interface {
	Execute(ctx context.Context, req DeleteLibraryRequest, progress func(current, total int, message string)) (*DeleteLibraryResult, error)
}

// LibrarySettingsCleaner removes per-user canonical setting values scoped to a
// deleted library. Satisfied by *userstore.SettingValuesCleaner.
type LibrarySettingsCleaner interface {
	DeleteForLibrary(ctx context.Context, libraryID int) int64
}

type LibraryDeleteExecutor struct {
	folderRepo      *catalog.FolderRepository
	sectionRepo     *sections.Repository
	settingsCleaner LibrarySettingsCleaner
}

func NewLibraryDeleteExecutor(
	folderRepo *catalog.FolderRepository,
	sectionRepo *sections.Repository,
	settingsCleaner LibrarySettingsCleaner,
) *LibraryDeleteExecutor {
	return &LibraryDeleteExecutor{
		folderRepo:      folderRepo,
		sectionRepo:     sectionRepo,
		settingsCleaner: settingsCleaner,
	}
}

func (e *LibraryDeleteExecutor) Execute(
	ctx context.Context,
	req DeleteLibraryRequest,
	progress func(current, total int, message string),
) (*DeleteLibraryResult, error) {
	if e == nil || e.folderRepo == nil {
		return nil, fmt.Errorf("library delete executor is not configured")
	}
	if req.LibraryID <= 0 {
		return nil, fmt.Errorf("library_id is required")
	}

	if progress != nil {
		progress(0, 5, "Counting library contents")
	}

	stats, err := e.folderRepo.DeleteWithStats(ctx, req.LibraryID, func(current, total int, message string) {
		if progress != nil {
			progress(current, total+2, message)
		}
	})
	if err != nil {
		return nil, err
	}

	if e.sectionRepo != nil {
		if progress != nil {
			progress(4, 5, "Cleaning up generated home sections")
		}
		if err := e.sectionRepo.DeleteGeneratedHomeLibraryRecentSections(ctx, req.LibraryID); err != nil {
			return nil, fmt.Errorf("deleting generated home sections: %w", err)
		}
	}
	if e.settingsCleaner != nil {
		// The canonical settings schema declares no FK on library_id, so the
		// per-user profile_library values must go with the library or they
		// orphan. Best-effort inside the cleaner: the library itself is
		// already deleted at this point.
		e.settingsCleaner.DeleteForLibrary(ctx, req.LibraryID)
	}
	if progress != nil {
		progress(5, 5, "Library deletion completed")
	}

	name := req.LibraryName
	if name == "" {
		name = stats.LibraryName
	}

	return &DeleteLibraryResult{
		LibraryID:            req.LibraryID,
		LibraryName:          name,
		DeletedMediaFiles:    stats.MediaFiles,
		DeletedItemLinks:     stats.MediaItemLinks,
		DeletedOrphanedItems: stats.OrphanedItems,
		DeletedS3Objects:     0,
		ImageCleanupDirs:     len(stats.OrphanedImageDirs),
		orphanedImageDirs:    append([]string(nil), stats.OrphanedImageDirs...),
	}, nil
}

func decodeDeleteLibraryRequest(data json.RawMessage) (DeleteLibraryRequest, error) {
	var req DeleteLibraryRequest
	if len(data) == 0 {
		return req, fmt.Errorf("missing delete library payload")
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return req, fmt.Errorf("invalid delete library request payload: %w", err)
	}
	return req, nil
}
