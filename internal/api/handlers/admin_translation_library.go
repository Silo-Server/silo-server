package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/Silo-Server/silo-server/internal/models"
)

// metadataAILibraryLookup resolves the library a prewarm targets.
type metadataAILibraryLookup interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
}

func (h *MetadataAIHandler) libraryNotConfigured() error {
	return apiError(http.StatusServiceUnavailable, "not_configured", "Metadata AI translation is not configured on this server")
}

// ensureLibrary answers 404 for an unknown library.
func (h *MetadataAIHandler) ensureLibrary(ctx context.Context, libraryID int) error {
	if h.Libraries == nil {
		return h.libraryNotConfigured()
	}
	folder, err := h.Libraries.GetByID(ctx, libraryID)
	if errors.Is(err, catalog.ErrFolderNotFound) || (err == nil && folder == nil) {
		return apiError(http.StatusNotFound, "not_found", "Library not found")
	}
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to load library")
	}
	return nil
}

// TranslateLibraryMetadata queues the prewarm of a library's missing
// localizations for language, or returns the active one.
func (h *MetadataAIHandler) TranslateLibraryMetadata(ctx context.Context, libraryID int, language string, userID int) (*translation.Job, error) {
	if language == "" {
		return nil, fieldError("target_language", "target_language is required")
	}
	if h == nil || h.service == nil {
		return nil, h.libraryNotConfigured()
	}
	if err := h.ensureLibrary(ctx, libraryID); err != nil {
		return nil, err
	}
	var requestedBy *int
	if userID != 0 {
		requestedBy = new(userID)
	}
	job, err := h.service.EnqueueLibrary(ctx, libraryID, language, requestedBy)
	if err != nil {
		switch {
		case errors.Is(err, translation.ErrNotConfigured):
			return nil, h.libraryNotConfigured()
		case errors.Is(err, translation.ErrInvalidRequest):
			return nil, fieldError("target_language", err.Error())
		}
		slog.ErrorContext(ctx, "failed to enqueue library metadata translation", "component", "api", "library_id", libraryID, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to start translation")
	}
	return job, nil
}

// ListLibraryMetadataTranslationJobs returns a library's recent prewarm jobs.
func (h *MetadataAIHandler) ListLibraryMetadataTranslationJobs(ctx context.Context, libraryID int) ([]translation.Job, error) {
	if h == nil || h.service == nil {
		return nil, h.libraryNotConfigured()
	}
	if err := h.ensureLibrary(ctx, libraryID); err != nil {
		return nil, err
	}
	jobs, err := h.service.ListJobs(ctx, translation.LibraryContentID(libraryID))
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "list_error", "Failed to list jobs")
	}
	return jobs, nil
}

// CancelLibraryMetadataTranslation cancels a prewarm job of the library.
func (h *MetadataAIHandler) CancelLibraryMetadataTranslation(ctx context.Context, libraryID int, jobID int64) error {
	if h == nil || h.service == nil {
		return h.libraryNotConfigured()
	}
	return h.CancelAdminMetadataTranslation(ctx, translation.LibraryContentID(libraryID), jobID)
}
